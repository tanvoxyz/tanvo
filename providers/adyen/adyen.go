package adyen

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	adyensdk "github.com/adyen/adyen-go-api-library/v21/src/adyen"
	"github.com/adyen/adyen-go-api-library/v21/src/checkout"
	"github.com/adyen/adyen-go-api-library/v21/src/common"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"golang.org/x/time/rate"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Provider is the vendor this adapter serves.
const Provider = integration.ProviderAdyen

const (
	// instrumentationName names the tracer and meter.
	instrumentationName = "github.com/tanvoxyz/tanvo/providers/adyen"

	// DefaultSuccessRedirectURL is where shoppers return after a payment
	// link when the connection sets no success_redirect_url (lago
	// AdyenProvider::SUCCESS_REDIRECT_URL).
	DefaultSuccessRedirectURL = "https://www.adyen.com/"

	// MaxSuccessRedirectURLLength bounds success_redirect_url (lago
	// adyen_provider.rb:15 validates length ≤ 1024).
	MaxSuccessRedirectURLLength = 1024

	// DefaultLiveRate and DefaultTestRate are the per-connection request
	// rates, in requests per second, of live and test connections. Adyen
	// publishes no fixed per-merchant limit for the Checkout API and
	// answers 429 when it throttles; these are conservative defaults, not
	// documented figures. Lago has no client-side throttle.
	DefaultLiveRate = 50
	// DefaultTestRate is the test-environment rate; see DefaultLiveRate.
	DefaultTestRate = 10

	// defaultTimeout is the HTTP timeout of a call (the SDK's default).
	defaultTimeout = 60 * time.Second
)

// Gateway is the Adyen [payment.Gateway]. It is safe for concurrent use;
// one Gateway serves every Adyen connection.
type Gateway struct {
	http    *http.Client
	baseURL string
	tracer  trace.Tracer
	now     func() time.Time
	logger  *slog.Logger
	limits  *limiters
}

var (
	_ payment.Gateway             = (*Gateway)(nil)
	_ payment.WebhookAcknowledger = (*Gateway)(nil)
)

type config struct {
	httpClient     *http.Client
	baseURL        string
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
	now            func() time.Time
	logger         *slog.Logger
	liveRate       rate.Limit
	testRate       rate.Limit
}

// Option configures a [Gateway].
type Option func(*config)

// WithHTTPClient sets the HTTP client whose transport carries the calls;
// the gateway wraps its transport with rate limiting and instrumentation.
func WithHTTPClient(c *http.Client) Option { return func(o *config) { o.httpClient = c } }

// WithBaseURL sets the Checkout API base URL, version included (an
// httptest server in tests); by default it follows the connection's
// environment.
func WithBaseURL(u string) Option { return func(o *config) { o.baseURL = u } }

// WithTracerProvider sets the OpenTelemetry tracer provider; no-op by
// default.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(o *config) { o.tracerProvider = tp }
}

// WithMeterProvider sets the OpenTelemetry meter provider of the HTTP
// client metrics; no-op by default.
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(o *config) { o.meterProvider = mp }
}

// WithClock sets the time source of link expirations; time.Now by
// default.
func WithClock(now func() time.Time) Option { return func(o *config) { o.now = now } }

// WithLogger sets the logger; slog.Default() by default.
func WithLogger(l *slog.Logger) Option { return func(o *config) { o.logger = l } }

// WithRateLimit sets the per-connection request rates of live and test
// connections, in requests per second; 0 disables throttling of that
// environment.
func WithRateLimit(live, test float64) Option {
	return func(o *config) { o.liveRate, o.testRate = rate.Limit(live), rate.Limit(test) }
}

// New returns an Adyen gateway.
//
// Calls are not retried in process: a failure worth retrying is a
// [payment.ErrRetryable] provider error, and the caller's job retries it
// with the same idempotency key, as lago's jobs do
// (adyen/payments/create_service.rb:46 re-raises connection failures for
// the job).
func New(opts ...Option) *Gateway {
	c := config{liveRate: DefaultLiveRate, testRate: DefaultTestRate}
	for _, o := range opts {
		o(&c)
	}
	if c.tracerProvider == nil {
		c.tracerProvider = tracenoop.NewTracerProvider()
	}
	if c.meterProvider == nil {
		c.meterProvider = metricnoop.NewMeterProvider()
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.logger == nil {
		c.logger = slog.Default()
	}
	base := http.DefaultTransport
	timeout := defaultTimeout
	if c.httpClient != nil {
		if c.httpClient.Transport != nil {
			base = c.httpClient.Transport
		}
		if c.httpClient.Timeout != 0 {
			timeout = c.httpClient.Timeout
		}
	}
	limits := newLimiters(c.liveRate, c.testRate)
	return &Gateway{
		http: &http.Client{
			Timeout: timeout,
			// The limiter wraps the instrumented transport, so waiting for
			// a token is not counted as HTTP latency.
			Transport: limits.transport(otelhttp.NewTransport(base,
				otelhttp.WithTracerProvider(c.tracerProvider),
				otelhttp.WithMeterProvider(c.meterProvider),
			)),
		},
		baseURL: c.baseURL,
		tracer:  c.tracerProvider.Tracer(instrumentationName),
		now:     c.now,
		logger:  c.logger,
		limits:  limits,
	}
}

// Provider implements [payment.ConnectionGateway].
func (g *Gateway) Provider() integration.Provider { return Provider }

// settings is the connection's settings object.
type settings struct {
	MerchantAccount string `json:"merchant_account,omitempty"`
	LivePrefix      string `json:"live_prefix,omitempty"`
	// SuccessRedirectURL is a pointer: an empty string is invalid while an
	// absent one is the default (lago adyen_provider.rb:15 allow_nil).
	SuccessRedirectURL *string `json:"success_redirect_url,omitempty"`
}

// successURL returns the configured return URL or the default.
func (s settings) successURL() string {
	if s.SuccessRedirectURL != nil && *s.SuccessRedirectURL != "" {
		return *s.SuccessRedirectURL
	}
	return DefaultSuccessRedirectURL
}

// live reports a connection to Adyen's live environment: one with a live
// URL prefix, which live Checkout endpoints require. Lago also requires
// Rails' production environment (adyen_provider.rb:20-26); Tanvo has no
// such deployment mode, so the connection's settings decide.
func (s settings) live() bool { return s.LivePrefix != "" }

// secrets is the connection's decrypted secrets object.
type secrets struct {
	APIKey  string `json:"api_key"`
	HMACKey string `json:"hmac_key,omitempty"`
}

func parseConn(conn payment.Connection) (settings, secrets, error) {
	var st settings
	var sc secrets
	if len(conn.Settings) > 0 {
		if err := json.Unmarshal(conn.Settings, &st); err != nil {
			return st, sc, fmt.Errorf("adyen: decode settings: %w", core.ErrInvalidValue)
		}
	}
	if len(conn.Secrets) > 0 {
		if err := json.Unmarshal(conn.Secrets, &sc); err != nil {
			return st, sc, fmt.Errorf("adyen: decode secrets: %w", core.ErrInvalidValue)
		}
	}
	return st, sc, nil
}

// redirectURL is lago's AdyenUrlValidator: any "<scheme>://<rest>", so app
// deep links are accepted (adyen_url_validator.rb).
var redirectURL = regexp.MustCompile(`.+://.+`)

// ValidateConnection implements [payment.ConnectionGateway] (lago
// adyen_provider.rb:14-15): api_key and merchant_account are required
// (≤ 255 characters); success_redirect_url, when present, is a
// "<scheme>://…" URL of ≤ 1024 characters, and an empty one is invalid.
func (g *Gateway) ValidateConnection(conn payment.Connection) error {
	st, sc, err := parseConn(conn)
	if err != nil {
		return err
	}
	var v core.Validator
	v.String("api_key", sc.APIKey, 255)
	v.String("merchant_account", st.MerchantAccount, 255)
	v.MaxLen("live_prefix", st.LivePrefix, 255)
	if u := st.SuccessRedirectURL; u != nil {
		if v.MaxLen("success_redirect_url", *u, MaxSuccessRedirectURLLength) {
			v.Check(redirectURL.MatchString(*u), "success_redirect_url", webhook.CodeInvalidURL)
		}
	}
	return v.Err()
}

// RegisterWebhook implements [payment.ConnectionGateway]: Adyen
// notification endpoints are configured in the Customer Area, with the HMAC
// key stored on the connection, as in lago, which registers none.
func (g *Gateway) RegisterWebhook(context.Context, payment.Connection, string) (payment.ConnectionUpdate, error) {
	return payment.ConnectionUpdate{}, fmt.Errorf("adyen register webhook: %w", payment.ErrUnsupported)
}

// RefreshWebhook implements [payment.ConnectionGateway]; see
// [Gateway.RegisterWebhook].
func (g *Gateway) RefreshWebhook(context.Context, payment.Connection, string) error {
	return fmt.Errorf("adyen refresh webhook: %w", payment.ErrUnsupported)
}

// call is the context of one adapter operation: the connection's Checkout
// client and settings.
type call struct {
	checkout *checkout.APIClient
	settings settings
}

// client returns a Checkout client of the connection's key and
// environment over the shared HTTP client.
func (g *Gateway) client(st settings, sc secrets) *checkout.APIClient {
	cfg := &common.Config{
		ApiKey:      sc.APIKey,
		Environment: common.TestEnv,
		HTTPClient:  g.http,
	}
	if st.live() {
		cfg.Environment = common.LiveEnv
		cfg.LiveEndpointURLPrefix = st.LivePrefix
	}
	c := adyensdk.NewClient(cfg)
	if g.baseURL != "" {
		// The client keeps cfg and reads the endpoint on every call.
		cfg.CheckoutEndpoint = g.baseURL
	}
	return c.Checkout()
}

// do runs op against conn inside a span, throttled by the connection's
// rate limit, and classifies its error ([payment.ProviderError]).
func (g *Gateway) do(ctx context.Context, conn payment.Connection, op string, f func(ctx context.Context, c call) error) error {
	st, sc, err := parseConn(conn)
	if err != nil {
		return err
	}
	if sc.APIKey == "" {
		return fmt.Errorf("adyen %s: %w", op, &payment.ProviderError{
			Provider: string(Provider), Kind: payment.KindAuthentication, Message: "missing api key",
		})
	}
	ctx, span := g.tracer.Start(ctx, "adyen."+op,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("payment.provider", string(Provider)),
			attribute.String("tanvo.organization.id", conn.OrganizationID.String()),
			attribute.String("tanvo.integration.id", conn.ID.String()),
		))
	defer span.End()
	ctx = g.limits.with(ctx, conn.ID, st.live())
	if err := f(ctx, call{checkout: g.client(st, sc), settings: st}); err != nil {
		err = classify(err)
		span.RecordError(err)
		span.SetStatus(codes.Error, op)
		if pe, ok := payment.AsProviderError(err); ok {
			span.SetAttributes(attribute.String("payment.error.kind", string(pe.Kind)), attribute.String("payment.error.code", pe.Code))
		}
		return fmt.Errorf("adyen %s: %w", op, err)
	}
	return nil
}

// applicationInfo identifies the integration to Adyen on every request,
// as lago's Lago::Adyen::Params does (lib/lago/adyen/params.rb).
func applicationInfo() *checkout.ApplicationInfo {
	name := "Tanvo"
	return &checkout.ApplicationInfo{
		ExternalPlatform:    &checkout.ExternalPlatform{Name: &name, Integrator: &name},
		MerchantApplication: &checkout.CommonField{Name: &name},
	}
}

// ptr returns a pointer to v.
func ptr[T any](v T) *T { return &v }

// optional returns nil for "".
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// deref returns *s or "".
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
