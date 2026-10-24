package x402pay_test

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/x402pay"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/x402"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// gateEnv is the setup of the gate check specs: a usage-only USD plan
// whose charge on the metric targets the wallet, and a connection on
// Base Sepolia.
//
// source: spec/services/x402/gate_checks/check_service_spec.rb:10-35
type gateEnv struct {
	*env
	connection *x402.Connection
	plan       *catalog.Plan
	metric     *catalog.BillableMetric
	agent      string
	in         x402pay.GateCheckInput
}

func newGate(t *testing.T, connOpts ...func(*x402pay.ConnectionInput)) *gateEnv {
	t.Helper()
	e := newEnv(t, billingtest.Date(2026, time.March, 1))
	g := &gateEnv{env: e, agent: evmPayer}
	g.connection = e.connection([]x402.Network{baseSepolia}, map[x402.Family]string{x402.FamilyEVM: otherPayTo}, connOpts...)
	g.plan = e.h.Plan(period.Monthly, 0)
	g.metric = e.h.Metric("calls", catalog.AggregationTypeCount, "")
	e.h.Charge(g.plan, g.metric, catalog.ChargeModelStandard, `{"amount":"1"}`, targeted)
	g.in = x402pay.GateCheckInput{
		ConnectionCode: g.connection.Code, WalletCode: "agent_credits", AgentAddress: new(g.agent),
		Resource: "POST /v1/generate", PlanCode: g.plan.Code, BillableMetricCode: g.metric.Code,
		AmountCents: new(int64(1_000)), EstimatedCallCostCents: new(int64(1)),
	}
	return g
}

func targeted(c *catalog.Charge) { c.AcceptsTargetWallet = true }

// customer creates the agent's customer.
func (g *gateEnv) customer() *customer.Customer {
	return g.h.Customer(func(c *customer.Customer) { c.X402AgentAddress = new(g.agent) })
}

// funded creates the agent's wallet: 15 credits with 2 of ongoing usage
// and a stale ongoing balance of 0.
//
// source: spec/services/x402/gate_checks/check_service_spec.rb:16-19
func (g *gateEnv) funded(c *customer.Customer, opts ...func(*wallet.Wallet)) *wallet.Wallet {
	return g.wallet(c, "15", append([]func(*wallet.Wallet){func(w *wallet.Wallet) {
		w.OngoingBalanceAmount, w.CreditsOngoingBalance = 0, money.Zero
		w.OngoingUsageBalanceAmount, w.CreditsOngoingUsageBalance = 200, money.MustParse("2")
	}}, opts...)...)
}

// wallet creates an x402-enabled wallet agent_credits of credits.
func (g *gateEnv) wallet(c *customer.Customer, credits string, opts ...func(*wallet.Wallet)) *wallet.Wallet {
	return g.h.Wallet(c, credits, append([]func(*wallet.Wallet){func(w *wallet.Wallet) {
		w.Code, w.X402Enabled = new("agent_credits"), true
	}}, opts...)...)
}

// externalID is the agent's subscription external id.
func (g *gateEnv) externalID() string { return "x402_" + g.agent + "_" + g.plan.Code }

// subscription subscribes c to the plan under the agent's external id.
func (g *gateEnv) subscription(c *customer.Customer, opts ...func(*subscription.Subscription)) *subscription.Subscription {
	return g.h.Subscription(c, g.plan, append([]func(*subscription.Subscription){func(s *subscription.Subscription) {
		s.ExternalID = g.externalID()
	}}, opts...)...)
}

// pendingSub, incompleteSub and terminatedSub are subscription states.
func (g *gateEnv) pendingSub(s *subscription.Subscription) {
	s.SubscriptionAt = g.h.Now().AddDate(0, 0, 1)
}

func (g *gateEnv) incompleteSub(s *subscription.Subscription) {
	s.Status, s.StartedAt = subscription.StatusIncomplete, new(g.h.Now())
}

func (g *gateEnv) terminatedSub(s *subscription.Subscription) {
	s.Status, s.StartedAt, s.TerminatedAt = subscription.StatusTerminated, new(g.h.Now()), new(g.h.Now())
}

func (g *gateEnv) check() (*x402pay.GateCheckResult, error) {
	return g.svc.GateCheck(g.h.Ctx, g.h.OrgID(), g.in)
}

// evmRequirement is the requirement of the default connection for a top-up
// of 10 USD.
//
// source: spec/services/x402/gate_checks/check_service_spec.rb:27-33
var evmRequirement = x402pay.Requirement{
	Scheme: "exact", Network: baseSepolia, Asset: x402.AssetUSDC, AssetAddress: sepoliaUSDC,
	Extra: map[string]any{"name": "USDC", "version": "2"}, PayTo: otherPayTo, AmountAtomic: "10000000", MaxTimeoutSeconds: 60,
}

type gateWant struct {
	requirements []x402pay.Requirement
	external     string
	balance      string
}

func (g *gateEnv) want(t *testing.T, w gateWant) {
	t.Helper()
	res, err := g.check()
	if err != nil {
		t.Fatalf("GateCheck: %v", err)
	}
	if diff := cmp.Diff(w.requirements, res.Requirements); diff != "" {
		t.Errorf("requirements (-want +got):\n%s", diff)
	}
	if res.ExternalSubscriptionID != w.external {
		t.Errorf("external_subscription_id: got = %q, want = %q", res.ExternalSubscriptionID, w.external)
	}
	if w.balance != "" && res.BalanceCredits.Cmp(money.MustParse(w.balance)) != 0 {
		t.Errorf("balance_credits: got = %s, want = %s", res.BalanceCredits, w.balance)
	}
}

func (g *gateEnv) refused(t *testing.T, want string) {
	t.Helper()
	_, err := g.check()
	wantError(t, err, want)
}

func TestGateCheckFundedAgent(t *testing.T) {
	// source: spec/services/x402/gate_checks/check_service_spec.rb:43-49
	t.Run("passes on the computed base while the stored ongoing balance is stale", func(t *testing.T) {
		g := newGate(t)
		c := g.customer()
		g.funded(c)
		s := g.subscription(c)
		g.want(t, gateWant{external: s.ExternalID, balance: "13"})
		if n := len(g.fac.calls); n != 0 {
			t.Errorf("facilitator calls: got = %d, want = 0", n)
		}
	})
	// source: spec/services/x402/gate_checks/check_service_spec.rb:52-66
	for name, claimed := range map[string]string{
		"lowercase":     strings.ToLower(evmPayer),
		"uppercase hex": "0x" + strings.ToUpper(strings.TrimPrefix(evmPayer, "0x")),
	} {
		t.Run("with the agent address claimed in "+name, func(t *testing.T) {
			g := newGate(t)
			c := g.customer()
			g.funded(c)
			s := g.subscription(c)
			g.in.AgentAddress = new(claimed)
			g.want(t, gateWant{external: s.ExternalID})
		})
	}
	// source: spec/services/x402/gate_checks/check_service_spec.rb:68-77
	t.Run("with a wallet restricted to other fee types", func(t *testing.T) {
		g := newGate(t)
		c := g.customer()
		g.wallet(c, "15", func(w *wallet.Wallet) { w.AllowedFeeTypes = []core.FeeType{core.FeeTypeSubscription} })
		s := g.subscription(c)
		g.want(t, gateWant{external: s.ExternalID})
	})
	// source: spec/services/x402/gate_checks/check_service_spec.rb:79-85
	t.Run("with a pending subscription beside the active one", func(t *testing.T) {
		g := newGate(t)
		c := g.customer()
		g.funded(c)
		s := g.subscription(c)
		g.subscription(c, g.pendingSub)
		g.want(t, gateWant{external: s.ExternalID})
	})
}

func TestGateCheckFloor(t *testing.T) {
	tests := []struct {
		name, source string
		balance      int64
		credits      string
		cost         int64
		challenged   bool
		net          string
	}{
		{"below the floor", "88-102", 1_100, "11", 1, true, "9"},
		{"equal to the floor", "104-118", 1_200, "12", 1, false, "10"},
		{"with a larger per-call estimate", "120-131", 1_500, "15", 2, true, "13"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// source: spec/services/x402/gate_checks/check_service_spec.rb:{tt.source}
			g := newGate(t)
			c := g.customer()
			g.wallet(c, tt.credits, func(w *wallet.Wallet) {
				w.OngoingUsageBalanceAmount, w.CreditsOngoingUsageBalance = 200, money.MustParse("2")
			})
			s := g.subscription(c)
			g.in.EstimatedCallCostCents = new(tt.cost)
			w := gateWant{external: s.ExternalID, balance: tt.net}
			if tt.challenged {
				w.requirements = []x402pay.Requirement{evmRequirement}
			}
			g.want(t, w)
		})
	}
	// source: spec/services/x402/gate_checks/check_service_spec.rb:133-144
	t.Run("with a wallet whose rate changed", func(t *testing.T) {
		g := newGate(t)
		c := g.customer()
		g.wallet(c, "0", func(w *wallet.Wallet) { w.RateAmount = money.MustParse("2.5") })
		s := g.subscription(c)
		g.want(t, gateWant{requirements: []x402pay.Requirement{evmRequirement}, external: s.ExternalID})
	})
	t.Run("with an estimate whose headroom overflows", func(t *testing.T) {
		g := newGate(t)
		c := g.customer()
		g.funded(c)
		s := g.subscription(c)
		g.in.EstimatedCallCostCents = new(int64(1) << 62)
		g.want(t, gateWant{requirements: []x402pay.Requirement{evmRequirement}, external: s.ExternalID})
	})
}

func TestGateCheckStranger(t *testing.T) {
	refuse := func(in *x402pay.ConnectionInput) { in.AutoCreateCustomers = new(false) }
	// source: spec/services/x402/gate_checks/check_service_spec.rb:146-153
	t.Run("challenges at a zero balance and creates nothing", func(t *testing.T) {
		g := newGate(t)
		g.want(t, gateWant{requirements: []x402pay.Requirement{evmRequirement}, balance: "0"})
		customers := billingtest.Read(g.h, func(ctx context.Context, r port.Repos) (int, error) {
			_, err := r.Customers.GetByX402AgentAddress(ctx, g.h.OrgID(), evmPayer)
			if errors.Is(err, core.ErrNotFound) {
				return 0, nil
			}
			return 1, err
		})
		if customers != 0 {
			t.Errorf("customers: got = %d, want = 0", customers)
		}
	})
	// source: spec/services/x402/gate_checks/check_service_spec.rb:155-161
	t.Run("when the connection refuses strangers", func(t *testing.T) {
		newGate(t, refuse).refused(t, "base: buyer_not_recognized")
	})
	// source: spec/services/x402/gate_checks/check_service_spec.rb:164-178
	t.Run("without an agent address", func(t *testing.T) {
		g := newGate(t)
		g.in.AgentAddress = nil
		g.want(t, gateWant{requirements: []x402pay.Requirement{evmRequirement}})
		g = newGate(t, refuse)
		g.in.AgentAddress = nil
		g.refused(t, "base: buyer_not_recognized")
	})
	// source: spec/services/x402/gate_checks/check_service_spec.rb:180-193
	t.Run("a known agent when the connection refuses strangers", func(t *testing.T) {
		g := newGate(t, refuse)
		c := g.customer()
		g.funded(c)
		s := g.subscription(c)
		g.want(t, gateWant{external: s.ExternalID})
	})
}

func TestGateCheckInput(t *testing.T) {
	// source: spec/services/x402/gate_checks/check_service_spec.rb:195-232
	for shape, address := range map[string]string{
		"truncated hex":             "0xf4a43B9cc729c9E4E139CB86808f48e3eD09Dc",
		"hex without the 0x prefix": "f4a43B9cc729c9E4E139CB86808f48e3eD09Dcb2",
		"a wrong EIP-55 checksum":   "0xF4a43B9cc729c9E4E139CB86808f48e3eD09Dcb2",
		"a CAIP-10 account id":      "eip155:8453:0xf4a43B9cc729c9E4E139CB86808f48e3eD09Dcb2",
		"an empty string":           "",
		"longer than any address":   strings.Repeat("1", 45),
		"a Solana signature":        x402.Base58Encode(make([]byte, 64)),
	} {
		t.Run("with an agent address given as "+shape, func(t *testing.T) {
			g := newGate(t)
			g.in.AgentAddress = new(address)
			g.refused(t, "agent_address: invalid_format")
		})
	}
	// source: spec/services/x402/gate_checks/check_service_spec.rb:234-248
	missing := map[string]func(*x402pay.GateCheckInput){
		"plan_code":                 func(in *x402pay.GateCheckInput) { in.PlanCode = "" },
		"billable_metric_code":      func(in *x402pay.GateCheckInput) { in.BillableMetricCode = "" },
		"wallet_code":               func(in *x402pay.GateCheckInput) { in.WalletCode = "" },
		"amount_cents":              func(in *x402pay.GateCheckInput) { in.AmountCents = nil },
		"estimated_call_cost_cents": func(in *x402pay.GateCheckInput) { in.EstimatedCallCostCents = nil },
	}
	for field, drop := range missing {
		t.Run("without "+field, func(t *testing.T) {
			g := newGate(t)
			drop(&g.in)
			g.refused(t, field+": value_is_mandatory")
		})
	}
	// source: spec/services/x402/gate_checks/check_service_spec.rb:250-266
	for _, v := range []int64{0, -1} {
		g := newGate(t)
		g.in.EstimatedCallCostCents = new(v)
		g.refused(t, "estimated_call_cost_cents: invalid_value")
	}
	g := newGate(t)
	g.in.AmountCents = new(int64(0))
	g.refused(t, "amount_cents: invalid_value")
}

func TestGateCheckConnection(t *testing.T) {
	// source: spec/services/x402/gate_checks/check_service_spec.rb:268-274
	t.Run("with an unknown connection code", func(t *testing.T) {
		g := newGate(t)
		g.in.ConnectionCode = "unknown"
		g.refused(t, "connection_not_found")
	})
	// source: spec/services/x402/gate_checks/check_service_spec.rb:276-282
	t.Run("with a discarded connection", func(t *testing.T) {
		g := newGate(t)
		g.h.Do(func(ctx context.Context, r port.Repos) error {
			g.connection.DeletedAt = new(g.h.Now())
			return r.X402Connections.Update(ctx, g.connection)
		})
		g.refused(t, "connection_not_found")
	})
}

func TestGateCheckOrder(t *testing.T) {
	eur := func(g *gateEnv) {
		g.plan = g.h.Plan(period.Monthly, 0, func(p *catalog.Plan) { p.Currency = money.MustCurrency("EUR") })
		g.h.Charge(g.plan, g.metric, catalog.ChargeModelStandard, `{"amount":"1"}`, targeted)
		g.in.PlanCode = g.plan.Code
	}
	// source: spec/services/x402/gate_checks/check_service_spec.rb:284-295
	t.Run("a misconfigured route answers the route code before a stranger", func(t *testing.T) {
		g := newGate(t, func(in *x402pay.ConnectionInput) { in.AutoCreateCustomers = new(false) })
		eur(g)
		g.refused(t, "base: plan_currency_not_supported")
	})
	// source: spec/services/x402/gate_checks/check_service_spec.rb:297-305
	t.Run("a misconfigured route answers the route code before the wallet", func(t *testing.T) {
		g := newGate(t)
		eur(g)
		g.wallet(g.customer(), "15", func(w *wallet.Wallet) { w.X402Enabled = false })
		g.refused(t, "base: plan_currency_not_supported")
	})
	// source: spec/services/x402/gate_checks/check_service_spec.rb:308-319
	t.Run("with a wallet not enabled for x402", func(t *testing.T) {
		g := newGate(t)
		c := g.customer()
		g.wallet(c, "15", func(w *wallet.Wallet) { w.X402Enabled = false })
		g.subscription(c)
		g.refused(t, "base: wallet_not_x402_enabled")
	})
	// source: spec/services/x402/gate_checks/check_service_spec.rb:321-332
	t.Run("with a terminated wallet", func(t *testing.T) {
		g := newGate(t)
		c := g.customer()
		g.wallet(c, "1000", func(w *wallet.Wallet) {
			if err := w.Terminate(g.h.Now()); err != nil {
				t.Fatal(err)
			}
		})
		s := g.subscription(c)
		g.want(t, gateWant{requirements: []x402pay.Requirement{evmRequirement}, external: s.ExternalID, balance: "0"})
	})
	// source: spec/services/x402/gate_checks/check_service_spec.rb:334-347
	t.Run("with another customer's funded wallet under the same code", func(t *testing.T) {
		g := newGate(t)
		c := g.customer()
		s := g.subscription(c)
		agent := g.agent
		g.agent = evmPayout
		other := g.customer()
		g.wallet(other, "1000")
		g.subscription(other)
		g.agent = agent
		g.want(t, gateWant{requirements: []x402pay.Requirement{evmRequirement}, external: s.ExternalID, balance: "0"})
	})
	// The refusal of a customer carrying a tax error
	// (check_service_spec.rb:349-359) is not ported: Tanvo records no
	// customer error details.
}

func TestGateCheckSubscription(t *testing.T) {
	// source: spec/services/x402/gate_checks/check_service_spec.rb:361-375
	t.Run("without a subscription for the route's plan", func(t *testing.T) {
		g := newGate(t)
		c := g.customer()
		g.funded(c)
		g.want(t, gateWant{requirements: []x402pay.Requirement{evmRequirement}})
		g.subscription(c, g.terminatedSub)
		g.want(t, gateWant{requirements: []x402pay.Requirement{evmRequirement}})
	})
	// source: spec/services/x402/gate_checks/check_service_spec.rb:377-401
	for name, state := range map[string]func(*gateEnv) func(*subscription.Subscription){
		"pending":    func(g *gateEnv) func(*subscription.Subscription) { return g.pendingSub },
		"incomplete": func(g *gateEnv) func(*subscription.Subscription) { return g.incompleteSub },
	} {
		t.Run("with a "+name+" subscription", func(t *testing.T) {
			g := newGate(t)
			c := g.customer()
			g.funded(c)
			g.subscription(c, state(g))
			g.refused(t, "base: subscription_not_active")
		})
	}
}

func TestGateCheckChallenge(t *testing.T) {
	maxed := func(w *wallet.Wallet) { w.PaidTopUpMaxAmount = new(int64(500)) }
	// source: spec/services/x402/gate_checks/check_service_spec.rb:403-425
	t.Run("with a top-up above the wallet's maximum", func(t *testing.T) {
		g := newGate(t)
		c := g.customer()
		g.wallet(c, "0", maxed)
		g.subscription(c)
		g.refused(t, "amount_cents: amount_above_maximum")

		g = newGate(t)
		c = g.customer()
		g.wallet(c, "15", maxed)
		s := g.subscription(c)
		g.want(t, gateWant{external: s.ExternalID})
	})
	// source: spec/services/x402/gate_checks/check_service_spec.rb:427-444
	t.Run("with a credit purchase pending for the agent", func(t *testing.T) {
		g := newGate(t)
		pendingPurchase(t, g.env, g.connection, strings.ToLower(evmPayer), x402.SettlementKindCreditPurchase)
		g.refused(t, "base: credit_purchase_pending")

		c := g.customer()
		g.funded(c)
		s := g.subscription(c)
		g.want(t, gateWant{external: s.ExternalID})
	})
	// source: spec/services/x402/gate_checks/check_service_spec.rb:446-452
	t.Run("with a credit purchase pending for the agent in another organization", func(t *testing.T) {
		g := newGate(t)
		pendingPurchase(t, g.env, g.otherConnection(), evmPayer, x402.SettlementKindCreditPurchase)
		g.want(t, gateWant{requirements: []x402pay.Requirement{evmRequirement}})
	})
	// source: spec/services/x402/gate_checks/check_service_spec.rb:454-460
	t.Run("with an invoice payment pending from the agent", func(t *testing.T) {
		g := newGate(t)
		pendingPurchase(t, g.env, g.connection, evmPayer, x402.SettlementKindInvoicePayment)
		g.want(t, gateWant{requirements: []x402pay.Requirement{evmRequirement}})
	})
	solana := func(in *x402pay.ConnectionInput) {
		in.Networks, in.PayoutAddresses = []x402.Network{devnet}, map[x402.Family]string{x402.FamilySVM: otherSVM}
	}
	// source: spec/services/x402/gate_checks/check_service_spec.rb:462-497
	t.Run("with an EVM agent on a Solana-only connection", func(t *testing.T) {
		g := newGate(t, solana)
		g.refused(t, "base: agent_address_family_unsupported")
		if n := len(g.fac.calls); n != 0 {
			t.Errorf("facilitator calls: got = %d, want = 0", n)
		}
		c := g.customer()
		w := g.funded(c)
		s := g.subscription(c)
		g.want(t, gateWant{external: s.ExternalID})

		g.h.Do(func(ctx context.Context, r port.Repos) error {
			w.BalanceAmount, w.CreditsBalance = 0, money.Zero
			return r.Wallets.Update(ctx, w)
		})
		g.refused(t, "base: agent_address_family_unsupported")
	})
	// source: spec/services/x402/gate_checks/check_service_spec.rb:499-516
	t.Run("with a funded Solana agent", func(t *testing.T) {
		g := newGate(t, solana)
		g.agent = svmPayout
		g.in.AgentAddress = new(g.agent)
		c := g.customer()
		g.funded(c)
		g.subscription(c)
		g.want(t, gateWant{external: "x402_" + svmPayout + "_" + g.plan.Code})
		if n := len(g.fac.calls); n != 0 {
			t.Errorf("facilitator calls: got = %d, want = 0", n)
		}
	})
	// source: spec/services/x402/gate_checks/check_service_spec.rb:518-527
	t.Run("without an agent address on a Solana-only connection", func(t *testing.T) {
		g := newGate(t, solana)
		g.in.AgentAddress = nil
		res, err := g.check()
		if err != nil {
			t.Fatalf("GateCheck: %v", err)
		}
		if len(res.Requirements) != 1 || res.Requirements[0].Network != devnet {
			t.Errorf("requirements: got = %+v, want = one on %s", res.Requirements, devnet)
		}
	})
	both := func(in *x402pay.ConnectionInput) {
		in.Networks = []x402.Network{baseSepolia, devnet}
		in.PayoutAddresses = map[x402.Family]string{x402.FamilyEVM: otherPayTo, x402.FamilySVM: otherSVM}
	}
	// source: spec/services/x402/gate_checks/check_service_spec.rb:529-549
	t.Run("with a connection offering both families", func(t *testing.T) {
		g := newGate(t, both)
		res, err := g.check()
		if err != nil {
			t.Fatalf("GateCheck: %v", err)
		}
		var networks []x402.Network
		for _, r := range res.Requirements {
			networks = append(networks, r.Network)
		}
		if diff := cmp.Diff([]x402.Network{baseSepolia, devnet}, networks); diff != "" {
			t.Errorf("networks (-want +got):\n%s", diff)
		}
		g.fac.supported = []func() (x402.SupportedResult, error){func() (x402.SupportedResult, error) {
			return x402.SupportedResult{}, &x402.FacilitatorError{Kind: x402.ErrFacilitatorUnavailable, Message: "supported: HTTP 503"}
		}}
		_, err = g.check()
		var tp *x402pay.ThirdPartyError
		if !errors.As(err, &tp) || tp.Code != "unavailable_error" || tp.ThirdParty != x402.FacilitatorCoinbaseCDP {
			t.Errorf("error: got = %v, want = a coinbase_cdp unavailable_error", err)
		}
	})
}

// otherConnection creates a connection of another organization.
func (e *env) otherConnection() *x402.Connection {
	e.t.Helper()
	o := organization.New(core.NewID[core.OrganizationID](e.h.IDs), "Other", e.h.Now())
	e.h.Do(func(ctx context.Context, r port.Repos) error { return r.Organizations.Create(ctx, o) })
	c, err := e.svc.CreateConnection(e.h.Ctx, o.ID, x402pay.ConnectionInput{
		Code: "other", Name: "Other", Networks: []x402.Network{baseSepolia},
		PayoutAddresses: map[x402.Family]string{x402.FamilyEVM: otherPayTo},
		Credentials:     x402.Credentials{APIKeyID: "other-key-id", APIKeySecret: "other-key-secret"},
	})
	if err != nil {
		e.t.Fatalf("CreateConnection: %v", err)
	}
	return c
}

// pendingPurchase records a pending settlement of kind paid by payer
// through c; options run before it is stored.
func pendingPurchase(t *testing.T, e *env, c *x402.Connection, payer string, kind x402.SettlementKind, opts ...func(*x402.Settlement)) *x402.Settlement {
	t.Helper()
	at := e.h.Now().Add(5 * time.Minute)
	s := &x402.Settlement{
		ID: core.NewID[core.X402SettlementID](e.h.IDs), OrganizationID: c.OrganizationID, ConnectionID: c.ID,
		Kind: kind, Status: x402.SettlementStatusPending, SettledBy: x402.SettledByTanvo, Network: baseSepolia,
		Asset: sepoliaUSDC, PayerAddress: payer, PayeeAddress: c.PayoutAddresses[x402.FamilyEVM],
		SettledAmountAtomic: big.NewInt(1_000_000), SettledAmountCents: 100, ReconcileAfter: &at,
		PaymentDigest: core.NewID[core.X402SettlementID](e.h.IDs).String(), Payload: x402.Object{},
		CreatedAt: e.h.Now(), UpdatedAt: e.h.Now(),
	}
	if kind == x402.SettlementKindCreditPurchase {
		s.PurchaseSettings = x402.Object{"plan_code": "agent_api", "wallet_code": "agent_credits"}
	} else {
		s.InvoiceID = e.h.StoredInvoice(e.h.Customer()).ID
	}
	for _, o := range opts {
		o(s)
	}
	s.Normalize()
	e.h.Do(func(ctx context.Context, r port.Repos) error { return r.X402Settlements.Create(ctx, s) })
	return s
}
