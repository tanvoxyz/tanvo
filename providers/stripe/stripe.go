package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	stripe "github.com/stripe/stripe-go/v84"
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
const Provider = integration.ProviderStripe

const (
	// instrumentationName names the tracer and meter.
	instrumentationName = "github.com/tanvoxyz/tanvo/providers/stripe"

	// DefaultSuccessRedirectURL is where customers return after a hosted
	// page when the connection sets no success_redirect_url (lago
	// StripeProvider::SUCCESS_REDIRECT_URL).
	DefaultSuccessRedirectURL = "https://stripe.com/"

	// MaxSuccessRedirectURLLength bounds success_redirect_url (lago
	// stripe_provider.rb validates length ≤ 1024).
	MaxSuccessRedirectURLLength = 1024

	// DefaultLiveRate and DefaultTestRate are Stripe's documented
	// per-account limits of read and write requests per second in live and
	// test mode. Lago has no client-side throttle: it lets Stripe answer
	// 429 and retries the job (invoices/payments/create_job.rb retry_on
	// RateLimitError); throttling before sending keeps one busy
	// organization from spending its account's budget on rejected calls.
	DefaultLiveRate = 100
	// DefaultTestRate is the test-mode limit; see DefaultLiveRate.
	DefaultTestRate = 25

	// DefaultMaxNetworkRetries is how often stripe-go retries a call
	// in-process, with the same idempotency key: on connection failures and
	// 429 lock_timeout answers (stripe-go stripe.go shouldRetry and
	// error.go canRetry; other API errors are not retried). Lago's stripe-ruby client does not retry
	// (max_network_retries 0) and relies on job retries; retrying in
	// process first saves a job round trip for transient failures, and the
	// job still retries what remains [payment.ErrRetryable].
	DefaultMaxNetworkRetries = 2
)

// Gateway is the Stripe [payment.Gateway]. It is safe for concurrent use;
// one Gateway serves every Stripe connection.
type Gateway struct {
	backends *stripe.Backends
	tracer   trace.Tracer
	now      func() time.Time
	logger   *slog.Logger
	limits   *limiters
}

var (
	_ payment.Gateway        = (*Gateway)(nil)
	_ payment.FundingGateway = (*Gateway)(nil)
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
	maxRetries     int64
	noRetrySleep   bool
}

// Option configures a [Gateway].
type Option func(*config)

// WithHTTPClient sets the HTTP client whose transport carries the calls;
// the gateway wraps its transport with rate limiting and instrumentation.
func WithHTTPClient(c *http.Client) Option { return func(o *config) { o.httpClient = c } }

// WithBaseURL sets the API base URL (an httptest server in tests).
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

// WithClock sets the time source of webhook signature tolerance and
// expirations; time.Now by default.
func WithClock(now func() time.Time) Option { return func(o *config) { o.now = now } }

// WithLogger sets the logger; slog.Default() by default.
func WithLogger(l *slog.Logger) Option { return func(o *config) { o.logger = l } }

// WithRateLimit sets the per-connection request rates of live and test
// keys, in requests per second; 0 disables throttling of that mode.
func WithRateLimit(live, test float64) Option {
	return func(o *config) { o.liveRate, o.testRate = rate.Limit(live), rate.Limit(test) }
}

// WithMaxNetworkRetries sets the in-process retries of a call; see
// [DefaultMaxNetworkRetries].
func WithMaxNetworkRetries(n int) Option { return func(o *config) { o.maxRetries = int64(n) } }

// withoutRetrySleep makes in-process retries immediate; for tests.
func withoutRetrySleep() Option { return func(o *config) { o.noRetrySleep = true } }

// New returns a Stripe gateway.
func New(opts ...Option) *Gateway {
	c := config{
		liveRate:   DefaultLiveRate,
		testRate:   DefaultTestRate,
		maxRetries: DefaultMaxNetworkRetries,
	}
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
	timeout := 80 * time.Second // stripe-go's default client timeout
	if c.httpClient != nil {
		if c.httpClient.Transport != nil {
			base = c.httpClient.Transport
		}
		if c.httpClient.Timeout != 0 {
			timeout = c.httpClient.Timeout
		}
	}
	limits := newLimiters(c.liveRate, c.testRate)
	hc := &http.Client{
		Timeout: timeout,
		// The limiter wraps the instrumented transport, so waiting for a
		// token is not counted as HTTP latency.
		Transport: limits.transport(otelhttp.NewTransport(base,
			otelhttp.WithTracerProvider(c.tracerProvider),
			otelhttp.WithMeterProvider(c.meterProvider),
		)),
	}
	bc := &stripe.BackendConfig{
		HTTPClient:        hc,
		MaxNetworkRetries: stripe.Int64(c.maxRetries),
		LeveledLogger:     slogLeveled{c.logger},
		EnableTelemetry:   stripe.Bool(false),
	}
	if c.baseURL != "" {
		bc.URL = stripe.String(c.baseURL)
	}
	backends := stripe.NewBackendsWithConfig(bc)
	if c.noRetrySleep {
		for _, b := range []stripe.Backend{backends.API, backends.Connect} {
			if bi, ok := b.(*stripe.BackendImplementation); ok {
				bi.SetNetworkRetriesSleep(false)
			}
		}
	}
	return &Gateway{
		backends: backends,
		tracer:   c.tracerProvider.Tracer(instrumentationName),
		now:      c.now,
		logger:   c.logger,
		limits:   limits,
	}
}

// Provider implements [payment.ConnectionGateway].
func (g *Gateway) Provider() integration.Provider { return Provider }

// settings is the connection's settings object.
type settings struct {
	SuccessRedirectURL           string `json:"success_redirect_url,omitempty"`
	WebhookEndpointID            string `json:"webhook_endpoint_id,omitempty"`
	Supports3DS                  bool   `json:"supports_3ds,omitempty"`
	RequireTermsOfServiceConsent bool   `json:"require_terms_of_service_consent,omitempty"`
}

// successURL returns the configured return URL or the default.
func (s settings) successURL() string {
	if strings.TrimSpace(s.SuccessRedirectURL) != "" {
		return s.SuccessRedirectURL
	}
	return DefaultSuccessRedirectURL
}

// secrets is the connection's decrypted secrets object. The webhook
// signing secret is a secret here, where lago keeps it among the settings
// (payment_providers/base_provider.rb settings_accessors :webhook_secret):
// it authenticates deliveries, so it is encrypted at rest with the API key.
type secrets struct {
	SecretKey     string `json:"secret_key"`
	WebhookSecret string `json:"webhook_secret,omitempty"`
}

// live reports a live-mode key.
func (s secrets) live() bool {
	return strings.HasPrefix(s.SecretKey, "sk_live_") || strings.HasPrefix(s.SecretKey, "rk_live_")
}

func parseConn(conn payment.Connection) (settings, secrets, error) {
	var st settings
	var sc secrets
	if len(conn.Settings) > 0 {
		if err := json.Unmarshal(conn.Settings, &st); err != nil {
			return st, sc, fmt.Errorf("stripe: decode settings: %w", core.ErrInvalidValue)
		}
	}
	if len(conn.Secrets) > 0 {
		if err := json.Unmarshal(conn.Secrets, &sc); err != nil {
			return st, sc, fmt.Errorf("stripe: decode secrets: %w", core.ErrInvalidValue)
		}
	}
	return st, sc, nil
}

// ValidateConnection implements [payment.ConnectionGateway]: secret_key
// is required (≤ 255 characters), success_redirect_url is an absolute
// http(s) URL of ≤ 1024 characters when set (lago stripe_provider.rb).
func (g *Gateway) ValidateConnection(conn payment.Connection) error {
	st, sc, err := parseConn(conn)
	if err != nil {
		return err
	}
	var v core.Validator
	v.String("secret_key", sc.SecretKey, 255)
	if st.SuccessRedirectURL != "" {
		if v.MaxLen("success_redirect_url", st.SuccessRedirectURL, MaxSuccessRedirectURLLength) {
			v.Check(webhook.ValidURL(st.SuccessRedirectURL), "success_redirect_url", webhook.CodeInvalidURL)
		}
	}
	return v.Err()
}

// call is the context of one adapter operation: the connection's client,
// settings and secrets.
type call struct {
	client   *stripe.Client
	settings settings
	secrets  secrets
}

// do runs op against conn inside a span, throttled by the connection's
// rate limit, and classifies its error ([payment.ProviderError]).
func (g *Gateway) do(ctx context.Context, conn payment.Connection, op string, f func(ctx context.Context, c call) error) error {
	st, sc, err := parseConn(conn)
	if err != nil {
		return err
	}
	if sc.SecretKey == "" {
		return fmt.Errorf("stripe %s: %w", op, &payment.ProviderError{
			Provider: string(Provider), Kind: payment.KindAuthentication, Message: "missing secret key",
		})
	}
	ctx, span := g.tracer.Start(ctx, "stripe."+op,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("payment.provider", string(Provider)),
			attribute.String("tanvo.organization.id", conn.OrganizationID.String()),
			attribute.String("tanvo.integration.id", conn.ID.String()),
		))
	defer span.End()
	ctx = g.limits.with(ctx, conn.ID, sc.live())
	c := call{
		client:   stripe.NewClient(sc.SecretKey, stripe.WithBackends(g.backends)),
		settings: st,
		secrets:  sc,
	}
	if err := f(ctx, c); err != nil {
		err = classify(err)
		span.RecordError(err)
		span.SetStatus(codes.Error, op)
		if pe, ok := payment.AsProviderError(err); ok {
			span.SetAttributes(attribute.String("payment.error.kind", string(pe.Kind)), attribute.String("payment.error.code", pe.Code))
		}
		return fmt.Errorf("stripe %s: %w", op, err)
	}
	return nil
}

// slogLeveled adapts slog to stripe-go's leveled logger. stripe-go's
// informational messages (each request) go to debug.
type slogLeveled struct{ l *slog.Logger }

func (s slogLeveled) Debugf(format string, v ...any) { s.l.Debug(fmt.Sprintf(format, v...)) }
func (s slogLeveled) Infof(format string, v ...any)  { s.l.Debug(fmt.Sprintf(format, v...)) }
func (s slogLeveled) Warnf(format string, v ...any)  { s.l.Warn(fmt.Sprintf(format, v...)) }
func (s slogLeveled) Errorf(format string, v ...any) { s.l.Warn(fmt.Sprintf(format, v...)) }

// slogErr is the error attribute of a log record.
func slogErr(err error) slog.Attr { return slog.String("error", err.Error()) }
