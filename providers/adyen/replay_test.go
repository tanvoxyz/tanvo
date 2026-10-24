package adyen

import (
	"encoding/hex"
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
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// reply is one recorded Adyen answer: a fixture file under
// testdata/fixtures or an inline JSON body.
type reply struct {
	status  int
	fixture string
	body    string
}

// fixture replies 200 with testdata/fixtures/<name>.
func fixture(name string) reply { return reply{status: http.StatusOK, fixture: name} }

// ok replies 200 with an inline JSON body.
func ok(body string) reply { return reply{status: http.StatusOK, body: body} }

// fail replies status with an Adyen error object.
func fail(status int, errorType, code, msg string) reply {
	b, _ := json.Marshal(map[string]any{
		"status": status, "errorCode": code, "message": msg, "errorType": errorType, "pspReference": "ERRPSP",
	})
	return reply{status: status, body: string(b)}
}

// failFixture replies status with a recorded error fixture.
func failFixture(status int, name string) reply { return reply{status: status, fixture: name} }

// request is a request the replay server received.
type request struct {
	Method, Path   string
	Body           map[string]any
	IdempotencyKey string
	APIKey         string
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

// replay is an httptest Adyen Checkout API answering "METHOD /path"
// routes with recorded replies, in order, repeating the last one.
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
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	key := r.Method + " " + r.URL.Path
	rp.mu.Lock()
	rp.reqs = append(rp.reqs, request{
		Method: r.Method, Path: r.URL.Path, Body: body,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		APIKey:         r.Header.Get("X-API-Key"),
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
		rp.t.Errorf("unexpected Adyen request %s", key)
		rep = fail(http.StatusNotFound, "validation", "000", "no route "+key)
	}
	b := []byte(rep.body)
	if rep.fixture != "" {
		var err error
		if b, err = os.ReadFile(filepath.Join("testdata", "fixtures", rep.fixture)); err != nil {
			rp.t.Errorf("read fixture: %v", err)
		}
	}
	w.Header().Set("Content-Type", "application/json")
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

// Test identifiers.
var (
	testOrgID        = core.MustParseID[core.OrganizationID]("11111111-2222-3333-4444-555555555555")
	testConnID       = core.MustParseID[core.IntegrationID]("0199a000-0000-7000-8000-000000000001")
	testCustomerID   = core.MustParseID[core.CustomerID]("0199a000-0000-7000-8000-000000000002")
	testInvoiceID    = core.MustParseID[core.InvoiceID]("0199a000-0000-7000-8000-000000000003")
	testPaymentID    = core.MustParseID[core.PaymentID]("0199a000-0000-7000-8000-000000000004")
	testRequestID    = core.MustParseID[core.PaymentRequestID]("0199a000-0000-7000-8000-000000000005")
	testCreditNoteID = core.MustParseID[core.CreditNoteID]("0199a000-0000-7000-8000-000000000006")
	testEntityID     = core.MustParseID[core.BillingEntityID]("0199a000-0000-7000-8000-000000000007")
	testIntentID     = core.MustParseID[payment.IntentID]("0199a000-0000-7000-8000-000000000008")

	testNow = time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC)

	// testHMACKey is the connection's hex HMAC key.
	testHMACKey = hex.EncodeToString([]byte("tanvo-adyen-test-hmac-key-012345"))
)

const (
	testAPIKey          = "AQEx_test_key"
	testMerchantAccount = "TanvoTest"
	testExternalID      = "cus_ext_1"
)

// connection returns a test-environment connection with the given
// settings JSON.
func connection(settingsJSON string) payment.Connection {
	return payment.Connection{
		ID:             testConnID,
		OrganizationID: testOrgID,
		Code:           "adyen_1",
		Provider:       integration.ProviderAdyen,
		Settings:       json.RawMessage(settingsJSON),
		Secrets:        json.RawMessage(`{"api_key":"` + testAPIKey + `","hmac_key":"` + testHMACKey + `"}`),
	}
}

// defaultConn is a connection with a merchant account and no options.
func defaultConn() payment.Connection {
	return connection(`{"merchant_account":"` + testMerchantAccount + `"}`)
}

var (
	eur = money.MustCurrency("EUR")
	usd = money.MustCurrency("USD")
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
