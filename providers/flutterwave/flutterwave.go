package flutterwave

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

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
const Provider = integration.ProviderFlutterwave

const (
	// instrumentationName names the tracer and meter.
	instrumentationName = "github.com/tanvoxyz/tanvo/providers/flutterwave"

	// APIURL is Flutterwave's v3 API, for test and live keys alike (lago
	// FlutterwaveProvider::API_URL).
	APIURL = "https://api.flutterwave.com/v3"

	// DefaultSuccessRedirectURL is where customers return after paying when
	// the connection sets none (lago
	// FlutterwaveProvider::SUCCESS_REDIRECT_URL).
	DefaultSuccessRedirectURL = "https://www.flutterwave.com/ng"

	// MaxSuccessRedirectURLLength bounds success_redirect_url (lago
	// flutterwave_provider.rb validates length ≤ 1024).
	MaxSuccessRedirectURLLength = 1024

	// testKeyPrefix starts the secret key of a test connection.
	testKeyPrefix = "FLWSECK_TEST"

	// DefaultRate is the per-connection request rate, in requests per
	// second. Flutterwave throttles per merchant and answers 429 beyond its
	// limit, which is retryable whatever the rate. Lago has no client-side
	// throttle.
	DefaultRate = 10.0

	// defaultTimeout is the HTTP timeout of a call.
	defaultTimeout = 60 * time.Second

	// pathPayments is the hosted checkout endpoint; pathVerify the
	// transaction verification endpoint, a format of the transaction ID.
	pathPayments = "/payments"
	pathVerify   = "/transactions/%s/verify"
)

// Gateway is the Flutterwave [payment.Gateway]. It is safe for concurrent
// use; one Gateway serves every Flutterwave connection.
type Gateway struct {
	http    *http.Client
	baseURL string
	tracer  trace.Tracer
	now     func() time.Time
	logger  *slog.Logger
	limits  *limiters
	secret  func() string
}

var _ payment.Gateway = (*Gateway)(nil)

type config struct {
	httpClient     *http.Client
	baseURL        string
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
	now            func() time.Time
	logger         *slog.Logger
	rate           rate.Limit
	secret         func() string
}

// Option configures a [Gateway].
type Option func(*config)

// WithHTTPClient sets the HTTP client whose transport carries the calls;
// the gateway wraps its transport with rate limiting and instrumentation.
func WithHTTPClient(c *http.Client) Option { return func(o *config) { o.httpClient = c } }

// WithBaseURL sets the API base URL (an httptest server in tests);
// [APIURL] by default.
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

// WithClock sets the time source of Retry-After dates; time.Now by
// default.
func WithClock(now func() time.Time) Option { return func(o *config) { o.now = now } }

// WithLogger sets the logger; slog.Default() by default.
func WithLogger(l *slog.Logger) Option { return func(o *config) { o.logger = l } }

// WithRateLimit sets the per-connection request rate, in requests per
// second; 0 disables throttling.
func WithRateLimit(perSecond float64) Option {
	return func(o *config) { o.rate = rate.Limit(perSecond) }
}

// WithWebhookSecrets sets the source of generated webhook secrets; 64
// random hexadecimal characters by default.
func WithWebhookSecrets(next func() string) Option { return func(o *config) { o.secret = next } }

// New returns a Flutterwave gateway.
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
	if c.secret == nil {
		c.secret = randomSecret
	}
	if c.baseURL == "" {
		c.baseURL = APIURL
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
		baseURL: c.baseURL,
		tracer:  c.tracerProvider.Tracer(instrumentationName),
		now:     c.now,
		logger:  c.logger,
		limits:  limits,
		secret:  c.secret,
	}
}

// randomSecret returns a webhook secret of 32 random bytes in hexadecimal
// (lago flutterwave_provider.rb generate_webhook_secret: SecureRandom.hex(32)).
func randomSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return hex.EncodeToString(b)
}

// Provider implements [payment.ConnectionGateway].
func (g *Gateway) Provider() integration.Provider { return Provider }

// settings is the connection's settings object.
type settings struct {
	// SuccessRedirectURL is where customers return after paying.
	SuccessRedirectURL *string `json:"success_redirect_url,omitempty"`
}

// successRedirectURL returns the connection's return URL, else the
// default (lago success_redirect_url.presence).
func (s settings) successRedirectURL() string {
	if s.SuccessRedirectURL != nil && *s.SuccessRedirectURL != "" {
		return *s.SuccessRedirectURL
	}
	return DefaultSuccessRedirectURL
}

// secrets is the connection's decrypted secrets object.
type secrets struct {
	SecretKey     string `json:"secret_key"`
	WebhookSecret string `json:"webhook_secret"`
}

// livemode reports a live secret key.
func (s secrets) livemode() bool { return !strings.HasPrefix(s.SecretKey, testKeyPrefix) }

func parseConn(conn payment.Connection) (settings, secrets, error) {
	var st settings
	var sc secrets
	if len(conn.Settings) > 0 {
		if err := json.Unmarshal(conn.Settings, &st); err != nil {
			return st, sc, fmt.Errorf("flutterwave: decode settings: %w", core.ErrInvalidValue)
		}
	}
	if len(conn.Secrets) > 0 {
		if err := json.Unmarshal(conn.Secrets, &sc); err != nil {
			return st, sc, fmt.Errorf("flutterwave: decode secrets: %w", core.ErrInvalidValue)
		}
	}
	return st, sc, nil
}

// ValidateConnection implements [payment.ConnectionGateway] (lago
// flutterwave_provider.rb validations): secret_key is required;
// success_redirect_url, when set, is a valid URL of ≤ 1024 characters.
func (g *Gateway) ValidateConnection(conn payment.Connection) error {
	st, sc, err := parseConn(conn)
	if err != nil {
		return err
	}
	var v core.Validator
	v.Required("secret_key", sc.SecretKey)
	if u := st.SuccessRedirectURL; u != nil {
		if v.MaxLen("success_redirect_url", *u, MaxSuccessRedirectURLLength) {
			v.Check(webhook.ValidURL(*u), "success_redirect_url", webhook.CodeInvalidURL)
		}
	}
	return v.Err()
}

// RegisterWebhook implements [payment.ConnectionGateway]. Flutterwave's
// webhook endpoint and secret hash are set in its dashboard, so nothing is
// created at Flutterwave; a connection without a webhook secret gets a
// generated one, keeping its other secrets (lago flutterwave_provider.rb
// before_create generate_webhook_secret). The merchant copies it to the
// dashboard.
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
			return payment.ConnectionUpdate{}, fmt.Errorf("flutterwave: decode secrets: %w", core.ErrInvalidValue)
		}
	}
	all["webhook_secret"] = g.secret()
	b, err := json.Marshal(all)
	if err != nil {
		return payment.ConnectionUpdate{}, fmt.Errorf("flutterwave: encode secrets: %w", err)
	}
	return payment.ConnectionUpdate{Secrets: b}, nil
}

// RefreshWebhook implements [payment.ConnectionGateway]: the endpoint
// lives in Flutterwave's dashboard, so there is nothing to refresh.
func (g *Gateway) RefreshWebhook(context.Context, payment.Connection, string) error { return nil }

// call is the context of one adapter operation.
type call struct {
	g        *Gateway
	settings settings
	secrets  secrets
}

// send makes one call and decodes the answer into out (when not nil);
// failures are [payment.ProviderError] (see [answerError]).
func (c call) send(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.g.baseURL+path, rd)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.secrets.SecretKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.g.http.Do(req)
	if err != nil {
		return transportError(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return transportError(err)
	}
	if res.StatusCode >= 300 {
		return answerError(res, raw, c.g.now())
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		// A 2xx answer that cannot be read: the page may exist.
		return &payment.ProviderError{
			Provider: string(Provider), Kind: payment.KindUnavailable, HTTPStatus: res.StatusCode,
			Message: "undecodable answer",
		}
	}
	return nil
}

// do runs op against conn inside a span, throttled by the connection's
// rate limit.
func (g *Gateway) do(ctx context.Context, conn payment.Connection, op string, f func(ctx context.Context, c call) error) error {
	st, sc, err := parseConn(conn)
	if err != nil {
		return err
	}
	if sc.SecretKey == "" {
		return fmt.Errorf("flutterwave %s: %w", op, &payment.ProviderError{
			Provider: string(Provider), Kind: payment.KindAuthentication, Message: "missing secret key",
		})
	}
	ctx, span := g.tracer.Start(ctx, "flutterwave."+op,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("payment.provider", string(Provider)),
			attribute.String("tanvo.organization.id", conn.OrganizationID.String()),
			attribute.String("tanvo.integration.id", conn.ID.String()),
		))
	defer span.End()
	ctx = g.limits.with(ctx, conn.ID)
	if err := f(ctx, call{g: g, settings: st, secrets: sc}); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, op)
		if pe, ok := payment.AsProviderError(err); ok {
			span.SetAttributes(attribute.String("payment.error.kind", string(pe.Kind)), attribute.String("payment.error.code", pe.Code))
		}
		return fmt.Errorf("flutterwave %s: %w", op, err)
	}
	return nil
}
