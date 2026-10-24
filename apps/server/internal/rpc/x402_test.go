package rpc_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"connectrpc.com/connect/v2"
	metricnoop "go.opentelemetry.io/otel/metric/noop"

	"github.com/tanvoxyz/tanvo/apps/server/internal/app"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/billing/x402pay"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/x402"
	"github.com/tanvoxyz/tanvo/libs/period"
	logv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/log/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/log/v1/logv1connect"
	x402v1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/x402/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/x402/v1/x402v1connect"
	x402prov "github.com/tanvoxyz/tanvo/providers/x402"
)

// The gate check setup.
//
// source: spec/requests/api/v1/x402/gate_checks_controller_spec.rb:6-22
const (
	gateAgent  = "0xf4a43B9cc729c9E4E139CB86808f48e3eD09Dcb2"
	gatePayTo  = "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed"
	gateWallet = "agent_credits"
)

type gateSetup struct {
	s          *rpctest.Server
	svc        *x402pay.Service
	plan       *catalog.Plan
	metric     *catalog.BillableMetric
	connection *x402.Connection
}

// newGateSetup starts the API with opts and creates a usage-only USD
// plan whose charge targets the wallet.
func newGateSetup(t *testing.T, opts ...rpctest.Option) *gateSetup {
	t.Helper()
	s := rpctest.New(t, opts...)
	box, err := app.NewSecretBox(rpctest.SecretKey)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := x402pay.New(s.Fixtures.Deps, box, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := &gateSetup{s: s, svc: svc}
	g.plan = s.Fixtures.Plan(period.Monthly, 0)
	g.metric = s.Fixtures.Metric("calls", catalog.AggregationTypeCount, "")
	s.Fixtures.Charge(g.plan, g.metric, catalog.ChargeModelStandard, `{"amount":"1"}`,
		func(c *catalog.Charge) { c.AcceptsTargetWallet = true })
	return g
}

// connect creates the connection: Base Sepolia paying out to gatePayTo
// unless opts change it.
func (g *gateSetup) connect(t *testing.T, opts ...func(*x402pay.ConnectionInput)) {
	t.Helper()
	in := x402pay.ConnectionInput{
		Code: "coinbase_cdp_1", Name: "Coinbase CDP",
		Networks:        []x402.Network{"eip155:84532"},
		PayoutAddresses: map[x402.Family]string{x402.FamilyEVM: gatePayTo},
		Credentials:     x402.Credentials{APIKeyID: "test-key-id", APIKeySecret: "test-key-secret"},
	}
	for _, o := range opts {
		o(&in)
	}
	c, err := g.svc.CreateConnection(t.Context(), g.s.Org.ID, in)
	if err != nil {
		t.Fatalf("CreateConnection: %v", err)
	}
	g.connection = c
}

func (g *gateSetup) request(mut ...func(*x402v1.CheckX402GateRequest_builder)) *x402v1.CheckX402GateRequest {
	b := x402v1.CheckX402GateRequest_builder{
		ConnectionCode: new(g.connection.Code), WalletCode: new(gateWallet), AgentAddress: new(gateAgent),
		Resource: new("POST /v1/generate"), PlanCode: new(g.plan.Code), BillableMetricCode: new(g.metric.Code),
		AmountCents: new(int64(1_000)), EstimatedCallCostCents: new(int64(1)),
	}
	for _, m := range mut {
		m(&b)
	}
	return b.Build()
}

func (g *gateSetup) check(t *testing.T, c rpctest.Credential, req *x402v1.CheckX402GateRequest) (*x402v1.CheckX402GateResponse, error) {
	t.Helper()
	return x402v1connect.NewX402ServiceClient(g.s.Connect(c)).CheckX402Gate(t.Context(), req)
}

func TestCheckX402Gate(t *testing.T) {
	t.Parallel()

	t.Run("with a funded agent", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/requests/api/v1/x402/gate_checks_controller_spec.rb:26-40")
		g := newGateSetup(t)
		g.connect(t)
		cust := g.s.Fixtures.Customer(func(c *customer.Customer) { c.X402AgentAddress = new(gateAgent) })
		g.s.Fixtures.Wallet(cust, "15", func(w *wallet.Wallet) { w.Code, w.X402Enabled = new(gateWallet), true })
		external := "x402_" + gateAgent + "_" + g.plan.Code
		g.s.Fixtures.Subscription(cust, g.plan, func(s *subscription.Subscription) { s.ExternalID = external })

		status, body := g.s.REST(t, g.s.APIKey, http.MethodPost, "/v1/x402/gate_checks", map[string]any{
			"connection_code": g.connection.Code, "wallet_code": gateWallet, "agent_address": gateAgent,
			"resource": "POST /v1/generate", "plan_code": g.plan.Code, "billable_metric_code": g.metric.Code,
			"amount_cents": 1_000, "estimated_call_cost_cents": 1,
		})
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		// An empty repeated field renders as [] (lago renders null).
		want := map[string]any{"balance_credits": "15", "requirements": []any{}, "external_subscription_id": external}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got = %v, want = %v", got, want)
		}
	})

	t.Run("with a stranger", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/requests/api/v1/x402/gate_checks_controller_spec.rb:42-54")
		g := newGateSetup(t)
		g.connect(t)
		resp, err := g.check(t, g.s.APIKey, g.request())
		if err != nil {
			t.Fatal(err)
		}
		if resp.GetBalanceCredits() != "0" || resp.HasExternalSubscriptionId() || len(resp.GetRequirements()) != 1 {
			t.Fatalf("got = %v, want a zero balance, no subscription and one requirement", resp)
		}
		r := resp.GetRequirements()[0]
		got := []any{r.GetScheme(), r.GetNetwork(), r.GetAsset(), r.GetAssetAddress(), r.GetExtra().AsMap(), r.GetPayTo(), r.GetAmountAtomic(), r.GetMaxTimeoutSeconds()}
		want := []any{
			"exact", "eip155:84532", "usdc", "0x036CbD53842c5426634e7929541eC2318f3dCF7e",
			map[string]any{"name": "USDC", "version": "2"},
			gatePayTo, "10000000", int32(60),
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got = %v, want = %v", got, want)
		}
	})

	t.Run("produces no API log", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/requests/api/v1/x402/gate_checks_controller_spec.rb:56-63")
		var writer *rpc.APILogWriter
		g := newGateSetup(t, func(o *app.APIOptions) {
			var err error
			if writer, err = rpc.NewAPILogWriter(o.Tx, o.Log, metricnoop.NewMeterProvider(), rpc.APILogWriterConfig{}); err != nil {
				t.Fatal(err)
			}
			o.APILogs = writer
		})
		g.connect(t)
		if _, err := g.check(t, g.s.APIKey, g.request()); err != nil {
			t.Fatal(err)
		}
		writer.Flush(t.Context())
		logs, err := logv1connect.NewLogServiceClient(g.s.Connect(g.s.APIKey)).ListApiLogs(t.Context(), logv1.ListApiLogsRequest_builder{}.Build())
		if err != nil {
			t.Fatal(err)
		}
		if n := len(logs.GetApiLogs()); n != 0 {
			t.Errorf("got api logs = %d, want = 0", n)
		}
	})

	t.Run("when the connection refuses strangers", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/requests/api/v1/x402/gate_checks_controller_spec.rb:74-82")
		g := newGateSetup(t)
		g.connect(t, func(in *x402pay.ConnectionInput) { in.AutoCreateCustomers = new(false) })
		_, err := g.check(t, g.s.APIKey, g.request())
		wantCode(t, err, connect.CodeInvalidArgument, rpc.ReasonValidationErrors)
		want := []rpc.FieldViolation{{Field: "base", Reason: x402pay.CodeBuyerNotRecognized, Description: x402pay.CodeBuyerNotRecognized}}
		if got := rpc.ErrorViolations(err); !reflect.DeepEqual(got, want) {
			t.Errorf("got violations = %v, want = %v", got, want)
		}
	})

	t.Run("when /supported is unavailable", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/requests/api/v1/x402/gate_checks_controller_spec.rb:84-98")
		cdp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/supported" {
				t.Errorf("got %s %s, want GET /supported", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("{}"))
		}))
		t.Cleanup(cdp.Close)
		g := newGateSetup(t, func(o *app.APIOptions) {
			o.X402Facilitators = x402prov.NewFacilitators(x402prov.WithBaseURL(cdp.URL), x402prov.WithLogger(slog.New(slog.DiscardHandler)))
		})
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		g.connect(t, func(in *x402pay.ConnectionInput) {
			in.Networks = []x402.Network{"solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1"}
			in.PayoutAddresses = map[x402.Family]string{x402.FamilySVM: "2wKupLR9q6wXYppw8Gr2NvWxKBUqm4PPJKkQfoxHDBg4"}
			in.Credentials.APIKeySecret = base64.StdEncoding.EncodeToString(key)
		})
		_, err = g.check(t, g.s.APIKey, g.request(func(b *x402v1.CheckX402GateRequest_builder) { b.AgentAddress = nil }))
		wantCode(t, err, connect.CodeUnavailable, rpc.ReasonThirdPartyError)
	})

	t.Run("with an unknown connection code", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/requests/api/v1/x402/gate_checks_controller_spec.rb:100-107")
		g := newGateSetup(t)
		g.connect(t)
		_, err := g.check(t, g.s.APIKey, g.request(func(b *x402v1.CheckX402GateRequest_builder) { b.ConnectionCode = new("unknown") }))
		wantCode(t, err, connect.CodeNotFound, "connection_not_found")
	})

	t.Run("requires the x402 write permission", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/requests/api/v1/x402/gate_checks_controller_spec.rb:24 (requires API permission x402 write)")
		g := newGateSetup(t)
		g.connect(t)
		d := auth.Deps{Tx: g.s.Store, Clock: g.s.Clock, IDs: core.UUIDv7Generator{}, Rand: rand.Reader}
		keyWith := func(level permission.Level) rpctest.Credential {
			k, err := auth.NewAPIKeys(d, nil).Create(t.Context(), g.s.Org.ID, nil, map[permission.APIResource]permission.Level{permission.APIResourceX402: level})
			if err != nil {
				t.Fatal(err)
			}
			return rpctest.Credential{Token: k.Secret}
		}
		if _, err := g.check(t, keyWith(permission.LevelWrite), g.request()); err != nil {
			t.Errorf("write key: got = %v, want = nil", err)
		}
		_, err := g.check(t, keyWith(permission.LevelRead), g.request())
		wantCode(t, err, connect.CodePermissionDenied, "write_action_not_allowed_for_x402")
	})
}
