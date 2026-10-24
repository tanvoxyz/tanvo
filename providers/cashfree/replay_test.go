package cashfree

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
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// reply is one recorded Cashfree answer: a fixture file under
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
	Method, Path string
	Body         map[string]any
	Header       http.Header
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

// replay is an httptest Cashfree API answering "METHOD /path" routes with
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
	// Numbers keep their literal text: amounts are sent as 55.00.
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	_ = dec.Decode(&body)
	key := r.Method + " " + r.URL.Path
	rp.mu.Lock()
	rp.reqs = append(rp.reqs, request{Method: r.Method, Path: r.URL.Path, Body: body, Header: r.Header.Clone()})
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
		rp.t.Errorf("unexpected Cashfree request %s", key)
		rep = raw(http.StatusNotFound, `{"message":"not found","code":"request_invalid","type":"invalid_request_error"}`)
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
// clock, with the test link ID.
func (rp *replay) gateway(opts ...Option) *Gateway {
	base := []Option{
		WithBaseURL(rp.srv.URL),
		WithLogger(slog.New(slog.DiscardHandler)),
		WithClock(func() time.Time { return testNow }),
		WithLinkIDs(func() string { return testLinkID }),
	}
	return New(append(base, opts...)...)
}

// routeLinks is the payment links route.
const routeLinks = "POST " + pathLinks

// Test identifiers.
var (
	testOrgID      = core.MustParseID[core.OrganizationID]("11111111-2222-3333-4444-555555555555")
	testConnID     = core.MustParseID[core.IntegrationID]("0199a000-0000-7000-8000-000000000001")
	testCustomerID = core.MustParseID[core.CustomerID]("0199a000-0000-7000-8000-000000000002")
	testInvoiceID  = core.MustParseID[core.InvoiceID]("0199a000-0000-7000-8000-000000000003")
	testRequestID  = core.MustParseID[core.PaymentRequestID]("0199a000-0000-7000-8000-000000000005")
	testIntentID   = core.MustParseID[payment.IntentID]("0199a000-0000-7000-8000-000000000006")

	testNow = time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC)
)

const (
	testClientID     = "cf_test_client_id"
	testClientSecret = "cfsk_ma_prod_abc_123456"
	testLinkID       = "LINKID0000000000000000000A"
	testLinkURL      = "https://payments-test.cashfree.com/links//U1mgll3c0e9g"
	// fixtureInvoiceID is the invoice of the recorded webhook fixtures.
	fixtureInvoiceID = "06afb06b-4e54-4f8f-89c1-6d8b9907465a"
)

// connection returns a connection with the given settings and secrets JSON.
func connection(settingsJSON, secretsJSON string) payment.Connection {
	return payment.Connection{
		ID:             testConnID,
		OrganizationID: testOrgID,
		Code:           "cashfree_1",
		Provider:       integration.ProviderCashfree,
		Settings:       json.RawMessage(settingsJSON),
		Secrets:        json.RawMessage(secretsJSON),
	}
}

// defaultConn is a sandbox connection with client credentials and no
// success URL.
func defaultConn() payment.Connection {
	return connection(`{"sandbox":true}`,
		`{"client_id":"`+testClientID+`","client_secret":"`+testClientSecret+`"}`)
}

var (
	inr = money.MustCurrency("INR")
	jpy = money.MustCurrency("JPY")
	kwd = money.MustCurrency("KWD")
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
