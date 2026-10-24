package gocardless

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// reply is one recorded GoCardless answer: a fixture file under
// testdata/fixtures or an inline body.
type reply struct {
	status  int
	fixture string
	body    string
	header  http.Header
}

// fixture replies 200 with testdata/fixtures/<name>.
func fixture(name string) reply { return reply{status: http.StatusOK, fixture: name} }

// created replies 201 with testdata/fixtures/<name>.
func created(name string) reply { return reply{status: http.StatusCreated, fixture: name} }

// failFixture replies status with a recorded error fixture.
func failFixture(status int, name string) reply { return reply{status: status, fixture: name} }

// raw replies status with an inline body.
func raw(status int, body string) reply { return reply{status: status, body: body} }

// request is a request the replay server received.
type request struct {
	Method, Path   string
	Query          url.Values
	Body           map[string]any
	IdempotencyKey string
	Authorization  string
}

// str returns the string at a dotted path of the body; "" when absent.
func (r request) str(path ...string) string {
	var v any = r.Body
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = m[k]
	}
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
	var v any = r.Body
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		if v, ok = m[k]; !ok {
			return false
		}
	}
	return true
}

// replay is an httptest GoCardless API answering "METHOD /path" routes
// with recorded replies, in order, repeating the last one.
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
	_ = json.Unmarshal(b, &body)
	key := r.Method + " " + r.URL.Path
	rp.mu.Lock()
	rp.reqs = append(rp.reqs, request{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Body: body,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		Authorization:  r.Header.Get("Authorization"),
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
		rp.t.Errorf("unexpected GoCardless request %s", key)
		rep = failFixture(http.StatusNotFound, "not_found_response.json")
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
		WithOAuthURL(rp.srv.URL),
		WithLogger(slog.New(slog.DiscardHandler)),
		WithClock(func() time.Time { return testNow }),
	}
	return New(append(base, opts...)...)
}

// Test identifiers.
var (
	testOrgID        = core.MustParseID[core.OrganizationID]("11111111-2222-3333-4444-555555555555")
	testConnID       = core.MustParseID[core.IntegrationID]("0199a000-0000-7000-8000-000000000001")
	testCustomerID   = core.MustParseID[core.CustomerID]("0199a000-0000-7000-8000-000000000002")
	testInvoiceID    = core.MustParseID[core.InvoiceID]("0199a000-0000-7000-8000-000000000003")
	testPaymentID    = core.MustParseID[core.PaymentID]("0199a000-0000-7000-8000-000000000004")
	testRequestID    = core.MustParseID[core.PaymentRequestID]("0199a000-0000-7000-8000-000000000005")
	testCreditNoteID = core.MustParseID[core.CreditNoteID]("0199a000-0000-7000-8000-000000000006")

	testNow = time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC)
)

const (
	testAccessToken   = "sandbox_test_token"
	testWebhookSecret = "tanvo-gocardless-test-webhook-secret"
	testCustomer      = "CU123"
)

// connection returns a sandbox connection with the given settings JSON.
func connection(settingsJSON string) payment.Connection {
	return payment.Connection{
		ID:             testConnID,
		OrganizationID: testOrgID,
		Code:           "gocardless_1",
		Provider:       integration.ProviderGocardless,
		Settings:       json.RawMessage(settingsJSON),
		Secrets:        json.RawMessage(`{"access_token":"` + testAccessToken + `","webhook_secret":"` + testWebhookSecret + `"}`),
	}
}

// defaultConn is a connection with no settings.
func defaultConn() payment.Connection { return connection(`{}`) }

var (
	eur = money.MustCurrency("EUR")
	chf = money.MustCurrency("CHF")
)

// readFixture returns testdata/fixtures/<name>.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "fixtures", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}
