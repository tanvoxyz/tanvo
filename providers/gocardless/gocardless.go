package gocardless

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	gc "github.com/gocardless/gocardless-pro-go/v6"
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
const Provider = integration.ProviderGocardless

const (
	// instrumentationName names the tracer and meter.
	instrumentationName = "github.com/tanvoxyz/tanvo/providers/gocardless"

	// DefaultSuccessRedirectURL is where customers return after a mandate
	// setup when the connection sets no success_redirect_url (lago
	// GocardlessProvider::SUCCESS_REDIRECT_URL).
	DefaultSuccessRedirectURL = "https://gocardless.com/"

	// MaxSuccessRedirectURLLength bounds success_redirect_url (lago
	// gocardless_provider.rb:12 validates length ≤ 1024).
	MaxSuccessRedirectURLLength = 1024

	// DefaultRate is the per-connection request rate, in requests per
	// second: GoCardless limits an access token to 1000 requests per
	// minute and answers 429 beyond. Lago has no client-side throttle.
	DefaultRate = 1000.0 / 60

	// webhookSecretLength is the length of a generated webhook secret
	// (lago gocardless_service.rb SecureRandom.alphanumeric(50)).
	webhookSecretLength = 50

	// livePrefix starts a live-environment access token.
	livePrefix = "live_"

	// defaultTimeout is the HTTP timeout of a call.
	defaultTimeout = 60 * time.Second
)

// Gateway is the GoCardless [payment.Gateway]. It is safe for concurrent
// use; one Gateway serves every GoCardless connection.
type Gateway struct {
	http     *http.Client
	baseURL  string
	oauthURL string
	tracer   trace.Tracer
	now      func() time.Time
	logger   *slog.Logger
	limits   *limiters
}

var _ payment.Gateway = (*Gateway)(nil)

type config struct {
	httpClient     *http.Client
	baseURL        string
	oauthURL       string
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
	now            func() time.Time
	logger         *slog.Logger
	rate           rate.Limit
}

// Option configures a [Gateway].
type Option func(*config)

// WithHTTPClient sets the HTTP client whose transport carries the calls;
// the gateway wraps its transport with rate limiting and instrumentation.
func WithHTTPClient(c *http.Client) Option { return func(o *config) { o.httpClient = c } }

// WithBaseURL sets the API base URL (an httptest server in tests); by
// default it follows the access token's environment.
func WithBaseURL(u string) Option { return func(o *config) { o.baseURL = u } }

// WithOAuthURL sets the OAuth server base URL (an httptest server in
// tests); by default it follows [OAuthClient.Sandbox].
func WithOAuthURL(u string) Option { return func(o *config) { o.oauthURL = u } }

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

// WithClock sets the time source of rate-limit resets; time.Now by
// default.
func WithClock(now func() time.Time) Option { return func(o *config) { o.now = now } }

// WithLogger sets the logger; slog.Default() by default.
func WithLogger(l *slog.Logger) Option { return func(o *config) { o.logger = l } }

// WithRateLimit sets the per-connection request rate, in requests per
// second; 0 disables throttling.
func WithRateLimit(perSecond float64) Option {
	return func(o *config) { o.rate = rate.Limit(perSecond) }
}

// New returns a GoCardless gateway.
//
// Calls are not retried in process (see the package documentation): a
// failure worth retrying is a [payment.ErrRetryable] provider error, and
// the caller's job retries it with the same idempotency key. gocardless-pro
// for Ruby retries timeouts, connection failures and 5xx answers three
// times, 0.5 s apart (request.rb with_retries); the job's backoff takes
// that role here.
func New(opts ...Option) *Gateway {
	c := config{rate: DefaultRate}
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
	limits := newLimiters(c.rate)
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
		baseURL:  c.baseURL,
		oauthURL: c.oauthURL,
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
	// SuccessRedirectURL is a pointer: an empty string is invalid while an
	// absent one is the default (lago gocardless_provider.rb:12 allow_nil).
	SuccessRedirectURL *string `json:"success_redirect_url,omitempty"`
}

// successURL returns the configured return URL or the default.
func (s settings) successURL() string {
	if s.SuccessRedirectURL != nil && *s.SuccessRedirectURL != "" {
		return *s.SuccessRedirectURL
	}
	return DefaultSuccessRedirectURL
}

// secrets is the connection's decrypted secrets object. The webhook
// secret is a secret here, where lago keeps it among the settings
// (payment_providers/base_provider.rb:30): it authenticates deliveries, so
// it is encrypted at rest with the access token. The merchant still reads
// it to configure the endpoint (lago types/payment_providers/gocardless.rb
// webhook_secret).
type secrets struct {
	AccessToken   string `json:"access_token"`
	WebhookSecret string `json:"webhook_secret,omitempty"`
}

// live reports a live-environment access token. Lago picks the
// environment from the Rails environment (gocardless_provider.rb:24-30),
// which cannot mix sandbox and live connections; GoCardless prefixes its
// tokens with their environment.
func (s secrets) live() bool { return strings.HasPrefix(s.AccessToken, livePrefix) }

func parseConn(conn payment.Connection) (settings, secrets, error) {
	var st settings
	var sc secrets
	if len(conn.Settings) > 0 {
		if err := json.Unmarshal(conn.Settings, &st); err != nil {
			return st, sc, fmt.Errorf("gocardless: decode settings: %w", core.ErrInvalidValue)
		}
	}
	if len(conn.Secrets) > 0 {
		if err := json.Unmarshal(conn.Secrets, &sc); err != nil {
			return st, sc, fmt.Errorf("gocardless: decode secrets: %w", core.ErrInvalidValue)
		}
	}
	return st, sc, nil
}

// ValidateConnection implements [payment.ConnectionGateway] (lago
// gocardless_provider.rb:11-12): access_token is required;
// success_redirect_url, when present, is an absolute http(s)
// URL of ≤ 1024 characters, and an empty one is invalid.
func (g *Gateway) ValidateConnection(conn payment.Connection) error {
	st, sc, err := parseConn(conn)
	if err != nil {
		return err
	}
	var v core.Validator
	v.Required("access_token", sc.AccessToken)
	if u := st.SuccessRedirectURL; u != nil {
		if v.MaxLen("success_redirect_url", *u, MaxSuccessRedirectURLLength) {
			v.Check(webhook.ValidURL(*u), "success_redirect_url", webhook.CodeInvalidURL)
		}
	}
	return v.Err()
}

// RegisterWebhook implements [payment.ConnectionGateway]. GoCardless has no
// API creating webhook endpoints: the merchant creates one in its dashboard
// with the connection's secret, which is generated here (50 alphanumeric
// characters) when the connection has none, as lago does
// (gocardless_service.rb:33). The returned secrets keep every other key.
func (g *Gateway) RegisterWebhook(_ context.Context, conn payment.Connection, _ string) (payment.ConnectionUpdate, error) {
	_, sc, err := parseConn(conn)
	if err != nil {
		return payment.ConnectionUpdate{}, err
	}
	if sc.WebhookSecret != "" {
		return payment.ConnectionUpdate{}, nil
	}
	all := map[string]any{}
	if len(conn.Secrets) > 0 {
		if err := json.Unmarshal(conn.Secrets, &all); err != nil {
			return payment.ConnectionUpdate{}, fmt.Errorf("gocardless: decode secrets: %w", core.ErrInvalidValue)
		}
	}
	all["webhook_secret"] = alphanumeric(webhookSecretLength)
	b, err := json.Marshal(all)
	if err != nil {
		return payment.ConnectionUpdate{}, fmt.Errorf("gocardless: encode secrets: %w", err)
	}
	return payment.ConnectionUpdate{Secrets: b}, nil
}

// RefreshWebhook implements [payment.ConnectionGateway]: the endpoint lives
// in the merchant's dashboard, so there is nothing to refresh.
func (g *Gateway) RefreshWebhook(context.Context, payment.Connection, string) error { return nil }

// alphanumeric returns n random characters of [A-Za-z0-9].
func alphanumeric(n int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	// rand.Read never fails (crypto/rand).
	_, _ = rand.Read(b)
	for i := range b {
		// 256 % 62 = 8: the first 8 characters are slightly more likely,
		// irrelevant for a 50-character secret.
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// call is the context of one adapter operation: the connection's client
// and settings.
type call struct {
	svc      *gc.Service
	settings settings
	secrets  secrets
	rec      *recorder
	now      func() time.Time
}

// sdk marks an error of a client call with the status of its answer
// ([sdkError]); nil stays nil.
func (c call) sdk(err error) error {
	if err == nil {
		return nil
	}
	status, retry := c.rec.last(c.now())
	return &sdkError{status: status, retry: retry, err: err}
}

// client returns a client of the connection's token and environment over
// the shared HTTP client.
func (g *Gateway) client(sc secrets) (*gc.Service, error) {
	cfg, err := gc.NewConfig(sc.AccessToken, gc.WithEndpoint(g.endpoint(sc)), gc.WithClient(g.http))
	if err != nil {
		return nil, fmt.Errorf("gocardless: client: %w", err)
	}
	return gc.New(cfg)
}

// endpoint returns the API base URL of the token's environment.
func (g *Gateway) endpoint(sc secrets) string {
	switch {
	case g.baseURL != "":
		return g.baseURL
	case sc.live():
		return gc.LiveEndpoint
	default:
		return gc.SandboxEndpoint
	}
}

// do runs op against conn inside a span, throttled by the connection's
// rate limit, and classifies its error ([payment.ProviderError]).
func (g *Gateway) do(ctx context.Context, conn payment.Connection, op string, f func(ctx context.Context, c call) error) error {
	st, sc, err := parseConn(conn)
	if err != nil {
		return err
	}
	if sc.AccessToken == "" {
		return fmt.Errorf("gocardless %s: %w", op, &payment.ProviderError{
			Provider: string(Provider), Kind: payment.KindAuthentication, Message: "missing access token",
		})
	}
	svc, err := g.client(sc)
	if err != nil {
		return err
	}
	ctx, span := g.tracer.Start(ctx, "gocardless."+op,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("payment.provider", string(Provider)),
			attribute.String("tanvo.organization.id", conn.OrganizationID.String()),
			attribute.String("tanvo.integration.id", conn.ID.String()),
		))
	defer span.End()
	ctx = g.limits.with(ctx, conn.ID)
	ctx, rec := withRecorder(ctx)
	if err := f(ctx, call{svc: svc, settings: st, secrets: sc, rec: rec, now: g.now}); err != nil {
		err = classify(err)
		span.RecordError(err)
		span.SetStatus(codes.Error, op)
		if pe, ok := payment.AsProviderError(err); ok {
			span.SetAttributes(attribute.String("payment.error.kind", string(pe.Kind)), attribute.String("payment.error.code", pe.Code))
		}
		return fmt.Errorf("gocardless %s: %w", op, err)
	}
	return nil
}

// noRetries disables the client's in-process retries (see the package
// documentation); every request passes it.
func noRetries() gc.RequestOption { return gc.WithoutRetries() }
