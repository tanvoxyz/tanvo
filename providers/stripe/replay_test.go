package stripe

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// reply is one recorded Stripe answer: a fixture file under
// testdata/fixtures or an inline JSON body.
type reply struct {
	status  int
	fixture string
	body    string
	header  map[string]string
}

// fixture replies 200 with testdata/fixtures/<name>.
func fixture(name string) reply { return reply{status: http.StatusOK, fixture: name} }

// ok replies 200 with an inline JSON body.
func ok(body string) reply { return reply{status: http.StatusOK, body: body} }

// fail replies status with a Stripe error object.
func fail(status int, typ, code, msg string) reply {
	e := map[string]any{"type": typ, "message": msg}
	if code != "" {
		e["code"] = code
	}
	b, _ := json.Marshal(map[string]any{"error": e})
	return reply{status: status, body: string(b)}
}

// failFixture replies status with a recorded error fixture.
func failFixture(status int, name string) reply { return reply{status: status, fixture: name} }

// request is a request the replay server received.
type request struct {
	Method, Path   string
	Form           url.Values
	IdempotencyKey string
	Authorization  string
}

// replay is an httptest Stripe API answering "METHOD /path" routes with
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
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	for k, v := range r.URL.Query() {
		form[k] = append(form[k], v...)
	}
	key := r.Method + " " + r.URL.Path
	rp.mu.Lock()
	rp.reqs = append(rp.reqs, request{
		Method: r.Method, Path: r.URL.Path, Form: form,
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
		rp.t.Errorf("unexpected Stripe request %s", key)
		rep = fail(http.StatusNotFound, "invalid_request_error", "resource_missing", "no route "+key)
	}
	b := []byte(rep.body)
	if rep.fixture != "" {
		var err error
		if b, err = os.ReadFile(filepath.Join("testdata", "fixtures", rep.fixture)); err != nil {
			rp.t.Errorf("read fixture: %v", err)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Request-Id", "req_test")
	for k, v := range rep.header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(rep.status)
	_, _ = w.Write(b)
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

// gateway returns a gateway talking to the replay server.
func (rp *replay) gateway(opts ...Option) *Gateway {
	base := []Option{
		WithBaseURL(rp.srv.URL),
		withoutRetrySleep(),
		WithLogger(slog.New(slog.DiscardHandler)),
	}
	return New(append(base, opts...)...)
}

// Test identifiers.
var (
	testOrgID         = core.MustParseID[core.OrganizationID]("11111111-2222-3333-4444-555555555555")
	testConnID        = core.MustParseID[core.IntegrationID]("0199a000-0000-7000-8000-000000000001")
	testCustomerID    = core.MustParseID[core.CustomerID]("0199a000-0000-7000-8000-000000000002")
	testInvoiceID     = core.MustParseID[core.InvoiceID]("0199a000-0000-7000-8000-000000000003")
	testPaymentID     = core.MustParseID[core.PaymentID]("0199a000-0000-7000-8000-000000000004")
	testRequestID     = core.MustParseID[core.PaymentRequestID]("0199a000-0000-7000-8000-000000000005")
	testCreditNoteID  = core.MustParseID[core.CreditNoteID]("0199a000-0000-7000-8000-000000000006")
	testEntityID      = core.MustParseID[core.BillingEntityID]("0199a000-0000-7000-8000-000000000007")
	testIntentID      = core.MustParseID[payment.IntentID]("0199a000-0000-7000-8000-000000000008")
	testWebhookSecret = "whsec_test_secret"
)

// connection returns a test-mode connection with the given settings JSON.
func connection(settingsJSON string) payment.Connection {
	return payment.Connection{
		ID:             testConnID,
		OrganizationID: testOrgID,
		Code:           "stripe_1",
		Provider:       integration.ProviderStripe,
		Settings:       json.RawMessage(settingsJSON),
		Secrets:        json.RawMessage(`{"secret_key":"sk_test_123","webhook_secret":"` + testWebhookSecret + `"}`),
	}
}

var (
	eur = money.MustCurrency("EUR")
	usd = money.MustCurrency("USD")
	gbp = money.MustCurrency("GBP")
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

// formHas reports the parameter key, or a nested key under it, in the
// form; for assertions on absence.
func formHas(f url.Values, key string) bool {
	for k := range f {
		if k == key || strings.HasPrefix(k, key+"[") {
			return true
		}
	}
	return false
}

// checkForm compares form values; "" wants the key absent.
func checkForm(t *testing.T, f url.Values, want map[string]string) {
	t.Helper()
	for k, w := range want {
		got, present := f[k]
		switch {
		case w == "" && present:
			t.Errorf("form %s: got = %q, want = absent", k, got)
		case w != "" && (!present || got[0] != w):
			t.Errorf("form %s: got = %q, want = %q", k, got, w)
		}
	}
}
