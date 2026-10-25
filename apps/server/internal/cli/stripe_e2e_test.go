package cli_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Stripe objects of the fake Stripe server, as in lago's shared context
// "with Stripe configured for customer" (spec/support/shared_context/stripe_customer.rb).
const (
	stripeCustomerID  = "cus_123456789"
	stripeMethodID    = "pm_123456"
	stripeWebhookCode = "whsec_tanvo_e2e"
)

// stripeFixtures holds the recorded Stripe responses of providers/stripe.
var stripeFixtures = filepath.Join("..", "..", "..", "..", "providers", "stripe", "testdata", "fixtures")

// fakeStripe is a Stripe API answering the requests of a payment
// connection: webhook endpoint registration, the customer and its
// payment method, payment intents (accepted as processing, or declined
// with the recorded card_declined error), checkout sessions and refunds.
type fakeStripe struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// decline makes payment intents fail with card_declined.
	decline bool
	// intents are the created payment intents' forms, by ID.
	intents map[string]url.Values
	order   []string
	refunds []url.Values
}

func newFakeStripe(t *testing.T) *fakeStripe {
	t.Helper()
	f := &fakeStripe{t: t, intents: map[string]url.Values{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeStripe) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Request-Id", "req_e2e")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	method := map[string]any{
		"id": stripeMethodID, "object": "payment_method", "type": "card", "customer": stripeCustomerID,
		"card": map[string]any{"brand": "visa", "last4": "4242", "exp_month": 12, "exp_year": 2030},
	}
	switch p := r.URL.Path; {
	case r.Method == http.MethodPost && p == "/v1/webhook_endpoints":
		reply(http.StatusOK, map[string]any{"id": "we_e2e", "object": "webhook_endpoint", "secret": stripeWebhookCode, "url": form.Get("url"), "status": "enabled"})
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/v1/webhook_endpoints/"):
		reply(http.StatusOK, map[string]any{"id": "we_e2e", "object": "webhook_endpoint", "url": form.Get("url"), "status": "enabled"})
	case r.Method == http.MethodGet && p == "/v1/customers/"+stripeCustomerID:
		reply(http.StatusOK, map[string]any{
			"id": stripeCustomerID, "object": "customer",
			"invoice_settings": map[string]any{"default_payment_method": stripeMethodID},
		})
	case r.Method == http.MethodPost && p == "/v1/customers/"+stripeCustomerID:
		reply(http.StatusOK, map[string]any{"id": stripeCustomerID, "object": "customer"})
	case r.Method == http.MethodGet && p == "/v1/customers/"+stripeCustomerID+"/payment_methods":
		reply(http.StatusOK, map[string]any{"object": "list", "data": []any{method}, "has_more": false, "url": p})
	case r.Method == http.MethodGet && (p == "/v1/payment_methods/"+stripeMethodID || p == "/v1/customers/"+stripeCustomerID+"/payment_methods/"+stripeMethodID):
		reply(http.StatusOK, method)
	case r.Method == http.MethodPost && p == "/v1/payment_intents":
		id := fmt.Sprintf("pi_e2e_%d", len(f.order)+1)
		f.intents[id], f.order = form, append(f.order, id)
		if f.decline {
			// The recorded card_declined error, on this intent.
			b, err := os.ReadFile(filepath.Join(stripeFixtures, "payment_intent_card_declined_response.json"))
			if err != nil {
				f.t.Errorf("fixture: %v", err)
			}
			var e map[string]map[string]any
			if err := json.Unmarshal(b, &e); err != nil {
				f.t.Errorf("fixture: %v", err)
			}
			e["error"]["payment_intent"] = f.intent(id, "requires_payment_method")
			reply(http.StatusPaymentRequired, e)
			return
		}
		reply(http.StatusOK, f.intent(id, "processing"))
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/v1/payment_intents/"):
		id := strings.TrimPrefix(p, "/v1/payment_intents/")
		if _, ok := f.intents[id]; !ok {
			reply(http.StatusNotFound, map[string]any{"error": map[string]any{"type": "invalid_request_error", "code": "resource_missing"}})
			return
		}
		reply(http.StatusOK, f.intent(id, "processing"))
	case r.Method == http.MethodPost && p == "/v1/checkout/sessions":
		reply(http.StatusOK, map[string]any{"id": "cs_test_123", "object": "checkout.session", "url": "https://stripe.com/checkout/session/cs_test_123"})
	case r.Method == http.MethodPost && p == "/v1/refunds":
		f.refunds = append(f.refunds, form)
		amount, _ := strconv.ParseInt(form.Get("amount"), 10, 64)
		reply(http.StatusOK, map[string]any{
			"id": fmt.Sprintf("re_e2e_%d", len(f.refunds)), "object": "refund", "status": "succeeded",
			"amount": amount, "currency": "eur", "payment_intent": form.Get("payment_intent"),
		})
	default:
		f.t.Errorf("unexpected Stripe request %s %s (%s)", r.Method, p, body)
		reply(http.StatusNotFound, map[string]any{"error": map[string]any{"type": "invalid_request_error", "code": "resource_missing", "message": "no route"}})
	}
}

// intent returns the payment intent id in status, with the amount and
// metadata of its creation. The lock is held.
func (f *fakeStripe) intent(id, status string) map[string]any {
	form := f.intents[id]
	amount, _ := strconv.ParseInt(form.Get("amount"), 10, 64)
	meta := map[string]string{}
	for k, v := range form {
		if name, ok := strings.CutPrefix(k, "metadata["); ok && len(v) > 0 {
			meta[strings.TrimSuffix(name, "]")] = v[0]
		}
	}
	pi := map[string]any{
		"id": id, "object": "payment_intent", "status": status, "amount": amount, "amount_received": amount,
		"currency": form.Get("currency"), "customer": form.Get("customer"), "payment_method": form.Get("payment_method"),
		"metadata": meta, "latest_charge": "ch_" + id,
	}
	if status == "requires_payment_method" {
		pi["amount_received"] = 0
		pi["last_payment_error"] = map[string]any{"type": "card_error", "code": "card_declined", "message": "Your card was declined."}
	}
	return pi
}

// intentsCount returns the number of payment intents created.
func (f *fakeStripe) intentsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.order)
}

// lastIntent returns the ID of the last created payment intent.
func (f *fakeStripe) lastIntent() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.order) == 0 {
		f.t.Fatal("payment intents: got = none, want = one")
	}
	return f.order[len(f.order)-1]
}

// event returns a signed Stripe event of type typ on the payment intent
// id, at the instant at.
func (f *fakeStripe) event(typ, id string, at time.Time) (payload []byte, signature string) {
	f.mu.Lock()
	obj := f.intent(id, map[string]string{"payment_intent.succeeded": "succeeded", "payment_intent.payment_failed": "requires_payment_method"}[typ])
	f.mu.Unlock()
	payload, err := json.Marshal(map[string]any{
		"id": "evt_" + id, "object": "event", "api_version": "2025-04-30.basil", "created": at.Unix(), "livemode": false,
		"type": typ, "data": map[string]any{"object": obj},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(stripeWebhookCode))
	mac.Write([]byte(ts + "."))
	mac.Write(payload)
	return payload, "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// stripeScenario starts a scenario with a Stripe connection on the fake
// Stripe server and a customer connected to its Stripe customer.
// environ is added to the server's environment.
func stripeScenario(t *testing.T, at time.Time, customer map[string]any, environ ...string) (*scenario, *fakeStripe, string) {
	t.Helper()
	f := newFakeStripe(t)
	s := newScenario(t, at, scenarioOptions{stripeURL: f.srv.URL, environ: append([]string{"TANVO_API_URL=https://api.tanvo.test"}, environ...)})
	conn := s.admin(http.MethodPost, "/v1/integrations", map[string]any{
		"code": "stripe", "name": "Stripe", "stripe": map[string]any{"secret_key": "sk_test_e2e", "success_redirect_url": "https://acme.test/ok"},
	}).str("integration.id")
	s.idle()
	s.api(http.MethodPost, "/v1/customers", customer)
	s.api(http.MethodPut, "/v1/customers/external_id%3A"+customer["external_id"].(string)+"/connections/"+conn, map[string]any{
		"integration_id": conn, "external_customer_id": stripeCustomerID, "provider_payment_methods": []string{"card"},
	})
	s.idle()
	return s, f, conn
}

// webhook posts a provider webhook to the connection.
func (s *scenario) webhook(conn string, payload []byte, signature string) {
	s.t.Helper()
	req, err := http.NewRequestWithContext(s.t.Context(), http.MethodPost, s.base+"/webhooks/stripe/"+conn, bytes.NewReader(payload))
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", signature)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		s.t.Fatalf("POST /webhooks/stripe/%s: got status = %d (%s), want = 200", conn, res.StatusCode, b)
	}
}

// webhookPayloads returns the payloads of the organization's domain
// events of type typ.
func (s *scenario) webhookPayloads(typ string) []jsonValue {
	s.t.Helper()
	rows, err := s.pool.Query(s.t.Context(), "SELECT payload::text FROM domain_events WHERE organization_id = $1 AND event_type = $2 ORDER BY occurred_at, id", s.org, typ)
	if err != nil {
		s.t.Fatal(err)
	}
	defer rows.Close()
	var out []jsonValue
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			s.t.Fatal(err)
		}
		var v any
		if err := json.Unmarshal([]byte(p), &v); err != nil {
			s.t.Fatal(err)
		}
		out = append(out, jsonValue{v: v})
	}
	return out
}

// TestScenarioStripePaymentFailure charges the in-advance invoice of a
// new subscription with Stripe, which declines the card: the
// invoice.payment_failure webhook carries Stripe's error.
//
// source: spec/scenarios/invoices/invoice_payments_spec.rb:38
func TestScenarioStripePaymentFailure(t *testing.T) {
	t.Parallel()
	s, f, _ := stripeScenario(t, time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC), map[string]any{
		"external_id": "cust", "name": "Customer", "net_payment_term_days": 2,
	})
	f.mu.Lock()
	f.decline = true
	f.mu.Unlock()
	s.plan("plan", "INTERVAL_MONTHLY", "149", true)
	s.api(http.MethodPost, "/v1/subscriptions", map[string]any{"external_id": "sub_payment-failed", "customer_id": "external_id:cust", "plan_id": "code:plan"})
	s.idle()
	s.bill(s.clock.Now())

	invs := s.waitInvoices("cust", 1)
	if got := invs[0].str("payment_status"); got != "PAYMENT_STATUS_FAILED" {
		t.Errorf("invoice payment status: got = %s, want = PAYMENT_STATUS_FAILED", got)
	}
	failures := s.webhookPayloads("invoice.payment_failure")
	if len(failures) == 0 {
		t.Fatalf("invoice.payment_failure webhooks: got = none, want = one (webhooks: %v)", s.webhookTypes())
	}
	got := failures[0].String()
	for _, want := range []string{`"card_declined"`, `"Your card was declined."`} {
		if !strings.Contains(got, want) {
			t.Errorf("invoice.payment_failure webhook: got = %s, want = containing %s", got, want)
		}
	}
}

// TestScenarioStripePaymentAndRefund charges an in-advance invoice with
// Stripe, settles it with the payment_intent.succeeded webhook and
// refunds part of it through a credit note, which Stripe refunds.
//
// source: spec/services/payment_providers/stripe/webhooks/payment_intent_succeeded_service_spec.rb
// source: spec/services/credit_notes/refunds/stripe_service_spec.rb
func TestScenarioStripePaymentAndRefund(t *testing.T) {
	t.Parallel()
	s, f, conn := stripeScenario(t, time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC), map[string]any{"external_id": "cust", "name": "Customer"})
	s.plan("plan", "INTERVAL_MONTHLY", "149", true)
	s.api(http.MethodPost, "/v1/subscriptions", map[string]any{"external_id": "sub", "customer_id": "external_id:cust", "plan_id": "code:plan"})
	s.idle()

	invs := s.waitInvoices("cust", 1)
	inv := invs[0]
	pi := f.lastIntent()
	f.mu.Lock()
	form := f.intents[pi]
	f.mu.Unlock()
	if got := form.Get("amount"); got != "14900" {
		t.Errorf("payment intent amount: got = %s, want = 14900", got)
	}
	if got := form.Get("customer"); got != stripeCustomerID {
		t.Errorf("payment intent customer: got = %s, want = %s", got, stripeCustomerID)
	}
	if got := form.Get("payment_method"); got != stripeMethodID {
		t.Errorf("payment intent method: got = %s, want = %s", got, stripeMethodID)
	}
	if got := inv.str("payment_status"); got != "PAYMENT_STATUS_PENDING" {
		t.Errorf("invoice payment status before the webhook: got = %s, want = PAYMENT_STATUS_PENDING", got)
	}

	payload, sig := f.event("payment_intent.succeeded", pi, s.clock.Now())
	s.webhook(conn, payload, sig)
	s.idle()
	inv = s.api(http.MethodGet, "/v1/invoices/"+inv.str("id"), nil).at("invoice")
	if got := inv.str("payment_status"); got != "PAYMENT_STATUS_SUCCEEDED" {
		t.Errorf("invoice payment status: got = %s, want = PAYMENT_STATUS_SUCCEEDED", got)
	}
	if got := inv.cents("total_paid_amount"); got != 14_900 {
		t.Errorf("invoice paid: got = %d, want = 14900", got)
	}
	if !slices.Contains(s.webhookTypes(), "payment.succeeded") && !slices.Contains(s.webhookTypes(), "invoice.payment_status_updated") {
		t.Errorf("webhooks: got = %v, want = containing invoice.payment_status_updated", s.webhookTypes())
	}

	fee := inv.str("fees.0.id")
	cn := s.api(http.MethodPost, "/v1/credit_notes", map[string]any{
		"invoice_id": inv.str("id"), "reason": "CREDIT_NOTE_REASON_OTHER", "refund_amount": money("50"),
		"items": []map[string]any{{"fee_id": fee, "amount": "50"}},
	}).str("credit_note.id")
	s.idle()
	f.mu.Lock()
	refunds := f.refunds
	f.mu.Unlock()
	if len(refunds) != 1 {
		t.Fatalf("Stripe refunds: got = %d, want = 1", len(refunds))
	}
	if got := refunds[0].Get("payment_intent"); got != pi {
		t.Errorf("refund payment intent: got = %s, want = %s", got, pi)
	}
	if got := refunds[0].Get("amount"); got != "5000" {
		t.Errorf("refund amount: got = %s, want = 5000", got)
	}
	got := s.api(http.MethodGet, "/v1/credit_notes/"+cn, nil).at("credit_note")
	if st := got.str("refund_status"); st != "REFUND_STATUS_SUCCEEDED" {
		t.Errorf("credit note refund status: got = %s, want = REFUND_STATUS_SUCCEEDED", st)
	}
	s.noFailedJobs()
}
