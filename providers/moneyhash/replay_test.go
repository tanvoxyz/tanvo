package moneyhash

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// reply is one recorded MoneyHash answer: a fixture file under
// testdata/fixtures or an inline body.
type reply struct {
	status  int
	fixture string
	body    string
	header  http.Header
}

// fixture replies 200 with testdata/fixtures/<name>.
func fixture(name string) reply { return reply{status: http.StatusOK, fixture: name} }

// failFixture replies status with a recorded error fixture.
func failFixture(status int, name string) reply { return reply{status: status, fixture: name} }

// raw replies status with an inline body.
func raw(status int, body string) reply { return reply{status: status, body: body} }

// request is a request the replay server received.
type request struct {
	Method, Path   string
	Body           map[string]any
	IdempotencyKey string
	APIKey         string
}

// str returns the string at a dotted path of the body; "" when absent.
func (r request) str(path ...string) string {
	v, _ := r.at(path...)
	switch v := v.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// has reports a key at a dotted path of the body.
func (r request) has(path ...string) bool {
	_, ok := r.at(path...)
	return ok
}

func (r request) at(path ...string) (any, bool) {
	var v any = r.Body
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[k]; !ok {
			return nil, false
		}
	}
	return v, true
}

// replay is an httptest MoneyHash API answering "METHOD /path" routes with
// recorded replies, in order, repeating the last one.
type replay struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	routes map[string][]reply
	reqs   []request
}

func newReplay(t *testing.T, routes map[string][]reply) *replay {
	t.Helper()
	rp := &replay{t: t, routes: routes}
	rp.srv = httptest.NewServer(http.HandlerFunc(rp.serve))
	t.Cleanup(rp.srv.Close)
	return rp
}

func (rp *replay) serve(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var body map[string]any
	// Numbers keep their literal text: amounts are sent as "5.00".
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	_ = dec.Decode(&body)
	key := r.Method + " " + r.URL.Path
	rp.mu.Lock()
	rp.reqs = append(rp.reqs, request{
		Method: r.Method, Path: r.URL.Path, Body: body,
		IdempotencyKey: r.Header.Get(headerIdempotency),
		APIKey:         r.Header.Get(headerAPIKey),
	})
	seq, found := rp.routes[key]
	var rep reply
	if found && len(seq) > 0 {
		rep = seq[0]
		if len(seq) > 1 {
			rp.routes[key] = seq[1:]
		}
	}
	rp.mu.Unlock()
	if !found {
		rp.t.Errorf("unexpected MoneyHash request %s", key)
		rep = raw(http.StatusNotFound, `{"status":{"code":404,"message":"Not found.","errors":[]},"data":{}}`)
	}
	out := []byte(rep.body)
	if rep.fixture != "" {
		var err error
		if out, err = os.ReadFile(filepath.Join("testdata", "fixtures", rep.fixture)); err != nil {
			rp.t.Errorf("read fixture: %v", err)
		}
	}
	for k, vs := range rep.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(rep.status)
	_, _ = w.Write(out)
}

// requests returns the received requests to "METHOD /path".
func (rp *replay) requests(key string) []request {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	var out []request
	for _, r := range rp.reqs {
		if r.Method+" "+r.Path == key {
			out = append(out, r)
		}
	}
	return out
}

// only returns the single request to key.
func (rp *replay) only(key string) request {
	rp.t.Helper()
	rs := rp.requests(key)
	if len(rs) != 1 {
		rp.t.Fatalf("requests to %s: got = %d, want = 1", key, len(rs))
	}
	return rs[0]
}

// count returns the number of requests received.
func (rp *replay) count() int {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return len(rp.reqs)
}

// gateway returns a gateway talking to the replay server at the test
// clock.
func (rp *replay) gateway(opts ...Option) *Gateway {
	base := []Option{
		WithBaseURL(rp.srv.URL),
		WithLogger(slog.New(slog.DiscardHandler)),
		WithClock(func() time.Time { return testNow }),
	}
	return New(append(base, opts...)...)
}

// Routes of the MoneyHash API.
const (
	routeIntents      = "POST " + pathIntents
	routeCustomers    = "POST " + pathCustomers
	routeSignatureKey = "GET " + pathSignatureKey
)

// Test identifiers.
var (
	testOrgID      = core.MustParseID[core.OrganizationID]("11111111-2222-3333-4444-555555555555")
	testConnID     = core.MustParseID[core.IntegrationID]("0199a000-0000-7000-8000-000000000001")
	testCustomerID = core.MustParseID[core.CustomerID]("0199a000-0000-7000-8000-000000000002")
	testInvoiceID  = core.MustParseID[core.InvoiceID]("0199a000-0000-7000-8000-000000000003")
	testPaymentID  = core.MustParseID[core.PaymentID]("0199a000-0000-7000-8000-000000000004")
	testRequestID  = core.MustParseID[core.PaymentRequestID]("0199a000-0000-7000-8000-000000000005")
	testIntentID   = core.MustParseID[payment.IntentID]("0199a000-0000-7000-8000-000000000006")

	testNow = time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC)
)

const (
	testAPIKey       = "mh-test-api-key"
	testSignatureKey = "test_signature_key"
	testFlowID       = "xgXlXgX"
	testCustomer     = "c4fc7f2f-e46b-42d1-842b-c246cf1a597d"
	testCard         = "card_token_001"
	testWebhookURL   = "https://tanvo.example/webhooks/moneyhash/11111111-2222-3333-4444-555555555555?code=mh-tanvo"
)

// connection returns a connection with the given settings and secrets JSON.
func connection(settingsJSON, secretsJSON string) payment.Connection {
	return payment.Connection{
		ID:             testConnID,
		OrganizationID: testOrgID,
		Code:           "mh-tanvo",
		Provider:       integration.ProviderMoneyhash,
		Settings:       json.RawMessage(settingsJSON),
		Secrets:        json.RawMessage(secretsJSON),
		WebhookURL:     testWebhookURL,
	}
}

// defaultConn is a sandbox connection with a flow, an API key and a
// signature key.
func defaultConn() payment.Connection {
	return connection(`{"flow_id":"`+testFlowID+`","sandbox":true}`,
		`{"api_key":"`+testAPIKey+`","signature_key":"`+testSignatureKey+`"}`)
}

var (
	usd = money.MustCurrency("USD")
	jpy = money.MustCurrency("JPY")
	kwd = money.MustCurrency("KWD")
)

// testCustomerParams describes a business customer with every field set.
func testCustomerParams() payment.CustomerParams {
	return payment.CustomerParams{
		CustomerID:              testCustomerID,
		ExternalID:              "ext_cust_004",
		Name:                    "Ahmed Shahwan",
		CompanyName:             "Shahwan Trading",
		Firstname:               "Ahmed",
		Lastname:                "Shahwan",
		Email:                   "a.shahwan@moneyhash.io",
		Phone:                   "+201087654321",
		Type:                    customer.TypeCompany,
		LegalName:               "legal name cust00x3",
		LegalNumber:             "LN-1",
		TaxIdentificationNumber: "76543456",
		Currency:                usd,
		Address: core.Address{
			Line1: "first line addr", Line2: "second line", City: "Cairo",
			Zipcode: "11511", Country: "EG",
		},
	}
}

// readFixture returns testdata/fixtures/<name>.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "fixtures", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}
