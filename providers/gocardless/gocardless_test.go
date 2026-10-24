package gocardless

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	gc "github.com/gocardless/gocardless-pro-go/v6"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

const (
	routeMandates           = "GET /mandates"
	routePayments           = "POST /payments"
	routeRefunds            = "POST /refunds"
	routeCustomers          = "POST /customers"
	routeBillingRequests    = "POST /billing_requests"
	routeBillingRequestFlow = "POST /billing_request_flows"
	routeToken              = "POST /oauth/access_token"
)

func TestValidateConnection(t *testing.T) {
	const src = "spec/models/payment_providers/gocardless_provider_spec.rb:"
	const token = `{"access_token":"sandbox_1"}`
	tests := []struct {
		source   string
		settings string
		secrets  string
		field    string // "" is valid
		code     string
	}{
		{source: src + "28", settings: `{"success_redirect_url":"https://example.com/return"}`, secrets: token},
		{source: src + "36", settings: `{"success_redirect_url":"my-app://your.package.name?param=12&p=7"}`, secrets: token, field: "success_redirect_url", code: "url_invalid"},
		{source: src + "44", settings: `{}`, secrets: token},
		{source: src + "52", settings: `{"success_redirect_url":""}`, secrets: token, field: "success_redirect_url", code: "url_invalid"},
		{source: src + "61", settings: `{"success_redirect_url":"your.package.name?param=12&p=7"}`, secrets: token, field: "success_redirect_url", code: "url_invalid"},
		{source: src + "69", settings: `{"success_redirect_url":"https://"}`, secrets: token, field: "success_redirect_url", code: "url_invalid"},
		{source: src + "77", settings: `{"success_redirect_url":"invalidurl"}`, secrets: token, field: "success_redirect_url", code: "url_invalid"},
		{source: src + "10", settings: `{"success_redirect_url":"https://acme.example/` + strings.Repeat("a", 1024) + `"}`, secrets: token, field: "success_redirect_url", code: core.CodeTooLong},
		// source: spec/services/payment_providers/gocardless_service_spec.rb:129
		{source: "spec/services/payment_providers/gocardless_service_spec.rb:129", settings: `{}`, secrets: `{}`, field: "access_token", code: core.CodeRequired},
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
	if err := g.ValidateConnection(connection(`[`)); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("malformed settings: got = %v, want = %v", err, core.ErrInvalidValue)
	}
}

func TestSettings(t *testing.T) {
	// source: spec/models/payment_providers/gocardless_provider_spec.rb:86
	// source: spec/models/payment_providers/gocardless_provider_spec.rb:97
	// source: spec/services/payment_provider_customers/gocardless_service_spec.rb:146
	// source: spec/services/payment_provider_customers/gocardless_service_spec.rb:154
	conn := connection(`{"success_redirect_url":"https://acme.example/r"}`)
	conn.Secrets = json.RawMessage(`{"access_token":"foo_bar","webhook_secret":"s"}`)
	st, sc, err := parseConn(conn)
	if err != nil {
		t.Fatalf("parseConn: got = %v, want = nil", err)
	}
	if st.successURL() != "https://acme.example/r" || sc.AccessToken != "foo_bar" || sc.WebhookSecret != "s" {
		t.Errorf("connection: got = %+v %+v, want = every field read", st, sc)
	}
	for _, s := range []string{`{}`, `{"success_redirect_url":""}`} {
		st, _, _ := parseConn(connection(s))
		if got := st.successURL(); got != DefaultSuccessRedirectURL {
			t.Errorf("success URL of %s: got = %q, want = %q", s, got, DefaultSuccessRedirectURL)
		}
	}
	conn.Secrets = json.RawMessage(`[`)
	if _, _, err := parseConn(conn); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("malformed secrets: got = %v, want = %v", err, core.ErrInvalidValue)
	}
}

func TestEnvironment(t *testing.T) {
	// source: spec/services/payment_providers/gocardless/payments/cancel_service_spec.rb:42
	g := New()
	tests := []struct {
		token string
		live  bool
		want  string
	}{
		{"sandbox_abc", false, gc.SandboxEndpoint},
		{"live_abc", true, gc.LiveEndpoint},
	}
	for _, tt := range tests {
		sc := secrets{AccessToken: tt.token}
		if sc.live() != tt.live {
			t.Errorf("live of %q: got = %v, want = %v", tt.token, sc.live(), tt.live)
		}
		if got := g.endpoint(sc); got != tt.want {
			t.Errorf("endpoint of %q: got = %q, want = %q", tt.token, got, tt.want)
		}
	}
	// The connection's token reaches GoCardless as a bearer token.
	rp := newReplay(t, map[string][]reply{routeMandates: {fixture("mandates_empty_response.json")}})
	if _, err := rp.gateway().ListPaymentMethods(context.Background(), defaultConn(), testCustomer); err != nil {
		t.Fatalf("ListPaymentMethods: got = %v, want = nil", err)
	}
	if got := rp.only(routeMandates).Authorization; got != "Bearer "+testAccessToken {
		t.Errorf("Authorization: got = %q, want = %q", got, "Bearer "+testAccessToken)
	}
}

func TestRegisterWebhook(t *testing.T) {
	// source: spec/services/payment_providers/gocardless_service_spec.rb:33
	g := New()
	conn := defaultConn()
	conn.Secrets = json.RawMessage(`{"access_token":"tok","extra":1}`)
	up, err := g.RegisterWebhook(context.Background(), conn, "https://tanvo.example/webhooks")
	if err != nil {
		t.Fatalf("RegisterWebhook: got = %v, want = nil", err)
	}
	if up.Settings != nil {
		t.Errorf("settings: got = %s, want = unchanged", up.Settings)
	}
	var got map[string]any
	if err := json.Unmarshal(up.Secrets, &got); err != nil {
		t.Fatalf("secrets: %v", err)
	}
	secret, _ := got["webhook_secret"].(string)
	if !regexp.MustCompile(`^[A-Za-z0-9]{50}$`).MatchString(secret) {
		t.Errorf("webhook_secret: got = %q, want = 50 alphanumeric characters", secret)
	}
	if got["access_token"] != "tok" || got["extra"] != float64(1) {
		t.Errorf("secrets: got = %v, want = the other keys kept", got)
	}

	// source: spec/services/payment_providers/gocardless_service_spec.rb:95
	// An existing secret is kept.
	up, err = g.RegisterWebhook(context.Background(), defaultConn(), "https://tanvo.example/webhooks")
	if err != nil || up.Secrets != nil || up.Settings != nil {
		t.Errorf("existing secret: got = %+v, %v, want = no update", up, err)
	}
	if err := g.RefreshWebhook(context.Background(), defaultConn(), "https://tanvo.example/webhooks"); err != nil {
		t.Errorf("RefreshWebhook: got = %v, want = nil", err)
	}
	conn.Secrets = json.RawMessage(`[`)
	if _, err := g.RegisterWebhook(context.Background(), conn, ""); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("malformed secrets: got = %v, want = %v", err, core.ErrInvalidValue)
	}
}

func TestMissingAccessToken(t *testing.T) {
	rp := newReplay(t, map[string][]reply{})
	conn := defaultConn()
	conn.Secrets = json.RawMessage(`{}`)
	_, err := rp.gateway().CreatePayment(context.Background(), conn, invoicePayment())
	if !errors.Is(err, payment.ErrAuthentication) {
		t.Errorf("error: got = %v, want = %v", err, payment.ErrAuthentication)
	}
	if rp.count() != 0 {
		t.Errorf("requests: got = %d, want = 0", rp.count())
	}
}

func TestUnsupported(t *testing.T) {
	g := New()
	ctx := context.Background()
	conn := defaultConn()
	calls := map[string]func() error{
		"SetDefaultPaymentMethod": func() error { return g.SetDefaultPaymentMethod(ctx, conn, "cus", "md") },
		"DetachPaymentMethod":     func() error { return g.DetachPaymentMethod(ctx, conn, "md") },
		"GetPayment":              func() error { _, err := g.GetPayment(ctx, conn, "pm"); return err },
		"UpdatePaymentReference":  func() error { return g.UpdatePaymentReference(ctx, conn, "pm", "INV-1") },
		"Authorize":               func() error { _, err := g.Authorize(ctx, conn, payment.AuthorizeParams{}); return err },
		"CreatePaymentURL": func() error {
			_, err := g.CreatePaymentURL(ctx, conn, payment.PaymentURLParams{})
			return err
		},
		"ExpirePaymentURL": func() error { return g.ExpirePaymentURL(ctx, conn, "s") },
	}
	for name, f := range calls {
		if err := f(); !errors.Is(err, payment.ErrUnsupported) {
			t.Errorf("%s: got = %v, want = %v", name, err, payment.ErrUnsupported)
		}
	}
	if g.DefaultMethodTypes() != nil || g.ValidateMethodTypes([]string{"card"}) != nil {
		t.Errorf("method types: got = %v, want = none and any accepted", g.DefaultMethodTypes())
	}
	if g.Provider() != Provider {
		t.Errorf("Provider: got = %q, want = %q", g.Provider(), Provider)
	}
}

func TestRateLimit(t *testing.T) {
	rp := newReplay(t, map[string][]reply{routeMandates: {fixture("mandates_empty_response.json")}})
	// 20 requests per second with a burst of 20: the 22nd call waits for
	// two tokens, about 100ms.
	g := rp.gateway(WithRateLimit(20))
	start := time.Now()
	for range 22 {
		if _, err := g.ListPaymentMethods(context.Background(), defaultConn(), testCustomer); err != nil {
			t.Fatalf("ListPaymentMethods: got = %v, want = nil", err)
		}
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Errorf("elapsed: got = %v, want = at least 80ms of throttling", elapsed)
	}
	before := rp.count()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.ListPaymentMethods(ctx, defaultConn(), testCustomer); !errors.Is(err, context.Canceled) {
		t.Errorf("error: got = %v, want = %v", err, context.Canceled)
	}
	if rp.count() != before {
		t.Errorf("requests: got = %d, want = %d", rp.count(), before)
	}
}

func TestLimiters(t *testing.T) {
	l := newLimiters(100)
	other := core.MustParseID[core.IntegrationID]("0199a000-0000-7000-8000-0000000000ff")
	get := func(ctx context.Context) any { return ctx.Value(limiterCtxKey{}) }
	ctx := context.Background()
	a, b := get(l.with(ctx, testConnID)), get(l.with(ctx, testConnID))
	if a == nil || a != b {
		t.Errorf("same connection: got = %p and %p, want = one limiter", a, b)
	}
	if c := get(l.with(ctx, other)); c == a {
		t.Errorf("two connections: got = one limiter, want = two")
	}
	if c := get(newLimiters(0).with(ctx, testConnID)); c != nil {
		t.Errorf("disabled: got = %v, want = no limiter", c)
	}
}

func TestTracing(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	rp := newReplay(t, map[string][]reply{routeMandates: {failFixture(http.StatusUnauthorized, "authentication_failed_response.json")}})
	_, _ = rp.gateway(WithTracerProvider(tp)).ListPaymentMethods(context.Background(), defaultConn(), testCustomer)
	var op sdktrace.ReadOnlySpan
	httpSpans := 0
	for _, s := range rec.Ended() {
		if s.Name() == "gocardless.list_payment_methods" {
			op = s
		} else {
			httpSpans++
		}
	}
	if op == nil {
		t.Fatalf("spans: got = %d without gocardless.list_payment_methods, want = the operation span", len(rec.Ended()))
	}
	if httpSpans == 0 {
		t.Errorf("HTTP spans: got = 0, want = the instrumented request")
	}
	attrs := map[attribute.Key]string{}
	for _, a := range op.Attributes() {
		attrs[a.Key] = a.Value.String()
	}
	want := map[attribute.Key]string{
		"payment.provider":      "gocardless",
		"tanvo.organization.id": testOrgID.String(),
		"tanvo.integration.id":  testConnID.String(),
		"payment.error.kind":    "authentication",
		"payment.error.code":    "access_token_not_found",
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

func TestAlphanumeric(t *testing.T) {
	a, b := alphanumeric(webhookSecretLength), alphanumeric(webhookSecretLength)
	if len(a) != webhookSecretLength || a == b {
		t.Errorf("secrets: got = %q and %q, want = two distinct %d-character secrets", a, b, webhookSecretLength)
	}
}
