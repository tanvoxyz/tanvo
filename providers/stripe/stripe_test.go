package stripe

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

func TestValidateConnection(t *testing.T) {
	const src = "spec/models/payment_providers/stripe_provider_spec.rb:"
	tests := []struct {
		source   string
		settings string
		secrets  string
		field    string // "" is valid
		code     string
	}{
		{source: src + "20", settings: `{}`, secrets: `{"secret_key":"sk_test_123"}`},
		{source: src + "45", settings: `{"success_redirect_url":"https://acme.example/thanks"}`, secrets: `{"secret_key":"sk_test_123"}`},
		{source: src + "90", settings: `{"supports_3ds":true,"require_terms_of_service_consent":true,"webhook_endpoint_id":"we_1"}`, secrets: `{"secret_key":"sk_test_123","webhook_secret":"whsec_1"}`},
		{source: src + "10", settings: `{"success_redirect_url":"https://acme.example/` + strings.Repeat("a", 1024) + `"}`, secrets: `{"secret_key":"sk_test_123"}`, field: "success_redirect_url", code: core.CodeTooLong},
		{source: "app/models/payment_providers/stripe_provider.rb (url validation)", settings: `{"success_redirect_url":"not a url"}`, secrets: `{"secret_key":"sk_test_123"}`, field: "success_redirect_url", code: "url_invalid"},
		{source: "app/models/payment_providers/stripe_provider.rb secret_key", settings: `{}`, secrets: `{}`, field: "secret_key", code: core.CodeRequired},
	}
	g := New()
	for _, tt := range tests {
		conn := connection(tt.settings)
		conn.Secrets = json.RawMessage(tt.secrets)
		err := g.ValidateConnection(conn)
		if tt.field == "" {
			if err != nil {
				t.Errorf("%s: ValidateConnection: got = %v, want = nil", tt.source, err)
			}
			continue
		}
		var ve *core.ValidationError
		if !errors.As(err, &ve) || !ve.Has(tt.field, tt.code) {
			t.Errorf("%s: ValidateConnection: got = %v, want = %s: %s", tt.source, err, tt.field, tt.code)
		}
	}
}

func TestSettingsDefaults(t *testing.T) {
	// source: spec/models/payment_providers/stripe_provider_spec.rb:95 (require_terms_of_service_consent defaults to false)
	// source: spec/services/payment_provider_customers/stripe_service_spec.rb:636 (default success redirect URL)
	st, _, err := parseConn(connection(`{}`))
	if err != nil {
		t.Fatalf("parseConn: got = %v, want = nil", err)
	}
	if st.RequireTermsOfServiceConsent || st.Supports3DS {
		t.Errorf("settings: got = %+v, want = consent and 3DS off", st)
	}
	if got := st.successURL(); got != DefaultSuccessRedirectURL {
		t.Errorf("success URL: got = %q, want = %q", got, DefaultSuccessRedirectURL)
	}
}

func TestMissingSecretKey(t *testing.T) {
	rp := newReplay(t, map[string][]reply{})
	conn := connection(`{}`)
	conn.Secrets = json.RawMessage(`{}`)
	_, err := rp.gateway().GetPayment(context.Background(), conn, "pi_1")
	if !errors.Is(err, payment.ErrAuthentication) {
		t.Errorf("error: got = %v, want = %v", err, payment.ErrAuthentication)
	}
	if rp.count() != 0 {
		t.Errorf("requests: got = %d, want = 0", rp.count())
	}
}

func TestNetworkRetries(t *testing.T) {
	const route = "GET /v1/payment_intents/pi_1"
	lockTimeout := fail(http.StatusTooManyRequests, "invalid_request_error", "lock_timeout", "lock timeout")
	tests := []struct {
		name      string
		retries   int
		replies   []reply
		wantCalls int
		wantKind  payment.ErrorKind
	}{
		{"lock timeout retried", DefaultMaxNetworkRetries, []reply{lockTimeout, ok(intentJSON("succeeded"))}, 2, ""},
		{"retries exhausted", 1, []reply{lockTimeout}, 2, payment.KindRateLimited},
		{"rate limit not retried", DefaultMaxNetworkRetries, []reply{fail(http.StatusTooManyRequests, "invalid_request_error", "rate_limit", "slow down")}, 1, payment.KindRateLimited},
		{"server error not retried", DefaultMaxNetworkRetries, []reply{fail(http.StatusInternalServerError, "api_error", "", "boom")}, 1, payment.KindUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rp := newReplay(t, map[string][]reply{route: tt.replies})
			_, err := rp.gateway(WithMaxNetworkRetries(tt.retries)).GetPayment(context.Background(), connection(`{}`), "pi_1")
			if k := payment.ErrorKindOf(err); k != tt.wantKind {
				t.Errorf("error kind: got = %q (%v), want = %q", k, err, tt.wantKind)
			}
			if n := rp.count(); n != tt.wantCalls {
				t.Errorf("calls: got = %d, want = %d", n, tt.wantCalls)
			}
		})
	}
}

// flakyTransport fails its first n requests with a connection error.
type flakyTransport struct {
	n    atomic.Int32
	next http.RoundTripper
}

func (f *flakyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.n.Add(-1) >= 0 {
		return nil, errors.New("connection reset by peer")
	}
	return f.next.RoundTrip(r)
}

func TestConnectionErrors(t *testing.T) {
	// source: app/jobs/invoices/payments/create_job.rb:16 (connection errors are retried with the idempotency key)
	t.Run("retried in process", func(t *testing.T) {
		rp := newReplay(t, map[string][]reply{routeIntents: {ok(intentJSON("succeeded"))}, routeMethod123456: {ok(`{"id":"pm_123456","object":"payment_method"}`)}})
		ft := &flakyTransport{next: http.DefaultTransport}
		ft.n.Store(1)
		p := invoicePayment()
		if _, err := rp.gateway(WithHTTPClient(&http.Client{Transport: ft})).CreatePayment(context.Background(), connection(`{}`), p); err != nil {
			t.Fatalf("CreatePayment: got = %v, want = nil", err)
		}
		if req := rp.only(routeIntents); req.IdempotencyKey != p.IdempotencyKey() {
			t.Errorf("Idempotency-Key: got = %q, want = %q", req.IdempotencyKey, p.IdempotencyKey())
		}
	})
	t.Run("retryable once exhausted", func(t *testing.T) {
		rp := newReplay(t, map[string][]reply{})
		ft := &flakyTransport{next: http.DefaultTransport}
		ft.n.Store(10)
		_, err := rp.gateway(WithHTTPClient(&http.Client{Transport: ft}), WithMaxNetworkRetries(1)).GetPayment(context.Background(), connection(`{}`), "pi_1")
		if k := payment.ErrorKindOf(err); k != payment.KindUnavailable || !errors.Is(err, payment.ErrRetryable) {
			t.Errorf("error: got = %q (%v), want = unavailable and retryable", k, err)
		}
		if got := ft.n.Load(); got != 8 {
			t.Errorf("attempts: got = %d, want = 2", 10-got)
		}
	})
}

func TestRateLimit(t *testing.T) {
	const route = "GET /v1/payment_intents/pi_1"
	rp := newReplay(t, map[string][]reply{route: {ok(intentJSON("succeeded"))}})
	// 20 requests per second with a burst of 20: the 22nd call waits for
	// two tokens, about 100ms.
	g := rp.gateway(WithRateLimit(0, 20))
	start := time.Now()
	for range 22 {
		if _, err := g.GetPayment(context.Background(), connection(`{}`), "pi_1"); err != nil {
			t.Fatalf("GetPayment: got = %v, want = nil", err)
		}
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Errorf("elapsed: got = %v, want = at least 80ms of throttling", elapsed)
	}

	// A canceled wait fails the call without a request.
	before := rp.count()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.GetPayment(ctx, connection(`{}`), "pi_1"); !errors.Is(err, context.Canceled) {
		t.Errorf("error: got = %v, want = %v", err, context.Canceled)
	}
	if rp.count() != before {
		t.Errorf("requests: got = %d, want = %d", rp.count(), before)
	}
}

func TestLimiters(t *testing.T) {
	l := newLimiters(100, 25)
	other := core.MustParseID[core.IntegrationID]("0199a000-0000-7000-8000-0000000000ff")
	get := func(ctx context.Context) any { return ctx.Value(limiterCtxKey{}) }
	ctx := context.Background()
	a, b := get(l.with(ctx, testConnID, false)), get(l.with(ctx, testConnID, false))
	if a == nil || a != b {
		t.Errorf("same connection: got = %p and %p, want = one limiter", a, b)
	}
	if c := get(l.with(ctx, testConnID, true)); c == a {
		t.Errorf("live and test keys: got = one limiter, want = two")
	}
	if c := get(l.with(ctx, other, false)); c == a {
		t.Errorf("two connections: got = one limiter, want = two")
	}
	if c := get(newLimiters(0, 0).with(ctx, testConnID, false)); c != nil {
		t.Errorf("disabled: got = %v, want = no limiter", c)
	}
	if !(secrets{SecretKey: "sk_live_1"}).live() || !(secrets{SecretKey: "rk_live_1"}).live() || (secrets{SecretKey: "sk_test_1"}).live() {
		t.Errorf("live: got = wrong mode detection, want = sk_live_ and rk_live_ only")
	}
}

func TestTracing(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	rp := newReplay(t, map[string][]reply{"GET /v1/payment_intents/pi_1": {fail(http.StatusPaymentRequired, "card_error", "card_declined", "Your card was declined.")}})
	_, _ = rp.gateway(WithTracerProvider(tp)).GetPayment(context.Background(), connection(`{}`), "pi_1")
	var op sdktrace.ReadOnlySpan
	httpSpans := 0
	for _, s := range rec.Ended() {
		if s.Name() == "stripe.get_payment" {
			op = s
		} else {
			httpSpans++
		}
	}
	if op == nil {
		t.Fatalf("spans: got = %d without stripe.get_payment, want = the operation span", len(rec.Ended()))
	}
	if httpSpans == 0 {
		t.Errorf("HTTP spans: got = 0, want = the instrumented request")
	}
	attrs := map[attribute.Key]string{}
	for _, a := range op.Attributes() {
		attrs[a.Key] = a.Value.String()
	}
	want := map[attribute.Key]string{
		"payment.provider":      "stripe",
		"tanvo.organization.id": testOrgID.String(),
		"tanvo.integration.id":  testConnID.String(),
		"payment.error.kind":    "card_declined",
		"payment.error.code":    "card_declined",
	}
	for k, w := range want {
		if attrs[k] != w {
			t.Errorf("attribute %s: got = %q, want = %q", k, attrs[k], w)
		}
	}
	if op.Status().Code.String() != "Error" {
		t.Errorf("status: got = %v, want = Error", op.Status())
	}
}
