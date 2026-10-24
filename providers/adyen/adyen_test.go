package adyen

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

func TestValidateConnection(t *testing.T) {
	const src = "spec/models/payment_providers/adyen_provider_spec.rb:"
	const keys = `{"api_key":"AQEx_1","hmac_key":"00ff"}`
	const merchant = `"merchant_account":"TestECOM"`
	tests := []struct {
		source   string
		settings string
		secrets  string
		field    string // "" is valid
		code     string
	}{
		{source: src + "26", settings: `{` + merchant + `,"success_redirect_url":"https://example.com"}`, secrets: keys},
		{source: src + "34", settings: `{` + merchant + `,"success_redirect_url":"my-app://your.package.name"}`, secrets: keys},
		{source: src + "42", settings: `{` + merchant + `}`, secrets: keys},
		{source: src + "50", settings: `{` + merchant + `,"success_redirect_url":""}`, secrets: keys, field: "success_redirect_url", code: "url_invalid"},
		{source: src + "59", settings: `{` + merchant + `,"success_redirect_url":"example.com"}`, secrets: keys, field: "success_redirect_url", code: "url_invalid"},
		{source: src + "67", settings: `{` + merchant + `,"success_redirect_url":"https://"}`, secrets: keys, field: "success_redirect_url", code: "url_invalid"},
		{source: src + "75", settings: `{` + merchant + `,"success_redirect_url":"invalid"}`, secrets: keys, field: "success_redirect_url", code: "url_invalid"},
		{source: "app/models/payment_providers/adyen_provider.rb:15 (length)", settings: `{` + merchant + `,"success_redirect_url":"https://acme.example/` + strings.Repeat("a", 1024) + `"}`, secrets: keys, field: "success_redirect_url", code: core.CodeTooLong},
		{source: "app/models/payment_providers/adyen_provider.rb:14 (api_key)", settings: `{` + merchant + `}`, secrets: `{}`, field: "api_key", code: core.CodeRequired},
		{source: "app/models/payment_providers/adyen_provider.rb:14 (merchant_account)", settings: `{}`, secrets: keys, field: "merchant_account", code: core.CodeRequired},
		{source: "app/models/payment_providers/adyen_provider.rb:18 (hmac_key optional)", settings: `{` + merchant + `}`, secrets: `{"api_key":"AQEx_1"}`},
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

func TestSettings(t *testing.T) {
	// source: spec/models/payment_providers/adyen_provider_spec.rb:88
	// source: spec/models/payment_providers/adyen_provider_spec.rb:98
	// source: spec/models/payment_providers/adyen_provider_spec.rb:108
	// source: spec/models/payment_providers/adyen_provider_spec.rb:118
	// source: spec/models/payment_providers/adyen_provider_spec.rb:128
	conn := connection(`{"merchant_account":"TestECOM","live_prefix":"abc-Acme","success_redirect_url":"https://acme.example/r"}`)
	conn.Secrets = json.RawMessage(`{"api_key":"AQEx_1","hmac_key":"00ff"}`)
	st, sc, err := parseConn(conn)
	if err != nil {
		t.Fatalf("parseConn: got = %v, want = nil", err)
	}
	if st.MerchantAccount != "TestECOM" || st.LivePrefix != "abc-Acme" || st.successURL() != "https://acme.example/r" || !st.live() {
		t.Errorf("settings: got = %+v, want = every field read and live", st)
	}
	if sc.APIKey != "AQEx_1" || sc.HMACKey != "00ff" {
		t.Errorf("secrets: got = %+v, want = api and hmac keys", sc)
	}

	// source: spec/services/payment_provider_customers/adyen_service_spec.rb:137
	// source: spec/services/payment_provider_customers/adyen_service_spec.rb:145
	for _, tt := range []struct{ settings, want string }{
		{`{"success_redirect_url":"https://acme.example/thanks"}`, "https://acme.example/thanks"},
		{`{}`, DefaultSuccessRedirectURL},
	} {
		st, _, _ := parseConn(connection(tt.settings))
		if got := st.successURL(); got != tt.want {
			t.Errorf("success URL of %s: got = %q, want = %q", tt.settings, got, tt.want)
		}
		if st.live() {
			t.Errorf("live of %s: got = true, want = false without a live prefix", tt.settings)
		}
	}
	if _, _, err := parseConn(connection(`[`)); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("malformed settings: got = %v, want = %v", err, core.ErrInvalidValue)
	}
}

func TestEnvironment(t *testing.T) {
	g := New()
	tests := []struct {
		settings settings
		want     string
	}{
		{settings{}, "https://checkout-test.adyen.com/checkout/v71"},
		{settings{LivePrefix: "1797a841fbb37ca7-AdyenDemo"}, "https://1797a841fbb37ca7-AdyenDemo-checkout-live.adyenpayments.com/checkout/v71"},
	}
	for _, tt := range tests {
		c := g.client(tt.settings, secrets{APIKey: "k"})
		if got := c.PaymentsApi.BasePath(); got != tt.want {
			t.Errorf("endpoint of %+v: got = %q, want = %q", tt.settings, got, tt.want)
		}
	}
}

func TestApplicationInfo(t *testing.T) {
	// source: spec/lib/lago/adyen/params_spec.rb:28
	// source: spec/lib/lago/adyen/params_spec.rb:36
	// source: spec/lib/lago/adyen/params_spec.rb:53
	// Every request carries the integration's application info, merged with
	// its own parameters.
	rp := newReplay(t, map[string][]reply{routePaymentLinks: {fixture("payment_links_response.json")}})
	if _, err := rp.gateway().SetupURL(context.Background(), defaultConn(), setupParams()); err != nil {
		t.Fatalf("SetupURL: got = %v, want = nil", err)
	}
	req := rp.only(routePaymentLinks)
	for _, path := range [][]string{
		{"applicationInfo", "externalPlatform", "name"},
		{"applicationInfo", "externalPlatform", "integrator"},
		{"applicationInfo", "merchantApplication", "name"},
	} {
		if got := req.str(path...); got != "Tanvo" {
			t.Errorf("%s: got = %q, want = %q", strings.Join(path, "."), got, "Tanvo")
		}
	}
	if got := req.str("merchantAccount"); got != testMerchantAccount {
		t.Errorf("merchantAccount: got = %q, want = %q", got, testMerchantAccount)
	}
	if req.APIKey != testAPIKey {
		t.Errorf("X-API-Key: got = %q, want = %q", req.APIKey, testAPIKey)
	}
}

func TestMissingAPIKey(t *testing.T) {
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
		"RegisterWebhook": func() error { _, err := g.RegisterWebhook(ctx, conn, "https://x.example"); return err },
		"RefreshWebhook":  func() error { return g.RefreshWebhook(ctx, conn, "https://x.example") },
		// source: spec/services/payment_provider_customers/adyen_service_spec.rb:124
		"UpdateCustomer":          func() error { return g.UpdateCustomer(ctx, conn, "cus", payment.CustomerParams{}) },
		"CreateCustomer":          func() error { _, err := g.CreateCustomer(ctx, conn, payment.CustomerParams{}); return err },
		"PaymentMethod":           func() error { _, err := g.PaymentMethod(ctx, conn, "pm"); return err },
		"SetDefaultPaymentMethod": func() error { return g.SetDefaultPaymentMethod(ctx, conn, "cus", "pm") },
		"DetachPaymentMethod":     func() error { return g.DetachPaymentMethod(ctx, conn, "pm") },
		"GetPayment":              func() error { _, err := g.GetPayment(ctx, conn, "psp"); return err },
		"UpdatePaymentReference":  func() error { return g.UpdatePaymentReference(ctx, conn, "psp", "INV-1") },
		"Authorize":               func() error { _, err := g.Authorize(ctx, conn, payment.AuthorizeParams{}); return err },
		"ExpirePaymentURL":        func() error { return g.ExpirePaymentURL(ctx, conn, "s") },
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
	rp := newReplay(t, map[string][]reply{routePaymentMethods: {fixture("payment_methods_response.json")}})
	// 20 requests per second with a burst of 20: the 22nd call waits for
	// two tokens, about 100ms.
	g := rp.gateway(WithRateLimit(0, 20))
	start := time.Now()
	for range 22 {
		if _, err := g.ListPaymentMethods(context.Background(), defaultConn(), testExternalID); err != nil {
			t.Fatalf("ListPaymentMethods: got = %v, want = nil", err)
		}
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Errorf("elapsed: got = %v, want = at least 80ms of throttling", elapsed)
	}
	before := rp.count()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.ListPaymentMethods(ctx, defaultConn(), testExternalID); !errors.Is(err, context.Canceled) {
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
		t.Errorf("live and test: got = one limiter, want = two")
	}
	if c := get(l.with(ctx, other, false)); c == a {
		t.Errorf("two connections: got = one limiter, want = two")
	}
	if c := get(newLimiters(0, 0).with(ctx, testConnID, false)); c != nil {
		t.Errorf("disabled: got = %v, want = no limiter", c)
	}
}

func TestTracing(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	rp := newReplay(t, map[string][]reply{routePaymentMethods: {fail(http.StatusUnauthorized, "security", "000", "HTTP Status Response - Unauthorized")}})
	_, _ = rp.gateway(WithTracerProvider(tp)).ListPaymentMethods(context.Background(), defaultConn(), testExternalID)
	var op sdktrace.ReadOnlySpan
	httpSpans := 0
	for _, s := range rec.Ended() {
		if s.Name() == "adyen.list_payment_methods" {
			op = s
		} else {
			httpSpans++
		}
	}
	if op == nil {
		t.Fatalf("spans: got = %d without adyen.list_payment_methods, want = the operation span", len(rec.Ended()))
	}
	if httpSpans == 0 {
		t.Errorf("HTTP spans: got = 0, want = the instrumented request")
	}
	attrs := map[attribute.Key]string{}
	for _, a := range op.Attributes() {
		attrs[a.Key] = a.Value.String()
	}
	want := map[attribute.Key]string{
		"payment.provider":      "adyen",
		"tanvo.organization.id": testOrgID.String(),
		"tanvo.integration.id":  testConnID.String(),
		"payment.error.kind":    "authentication",
		"payment.error.code":    "security",
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
