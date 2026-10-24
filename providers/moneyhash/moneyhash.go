package moneyhash

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
)

// Provider is the vendor this adapter serves.
const Provider = integration.ProviderMoneyhash

const (
	// instrumentationName names the tracer and meter.
	instrumentationName = "github.com/tanvoxyz/tanvo/providers/moneyhash"

	// LiveURL and SandboxURL are MoneyHash's API servers (lago
	// MoneyhashProvider.api_base_url).
	LiveURL    = "https://web.moneyhash.io"
	SandboxURL = "https://staging-web.moneyhash.io"

	// MaxFlowIDLength bounds flow_id (lago moneyhash_provider.rb validates
	// length ≤ 20).
	MaxFlowIDLength = 20

	// DefaultRate is the per-connection request rate, in requests per
	// second. MoneyHash publishes no limit; a 429 answer is retryable
	// whatever the rate. Lago has no client-side throttle.
	DefaultRate = 10.0

	// defaultTimeout is the HTTP timeout of a call.
	defaultTimeout = 60 * time.Second

	// API paths (lago moneyhash services).
	pathIntents      = "/api/v1.1/payments/intent/"
	pathCustomers    = "/api/v1.1/customers/"
	pathSignatureKey = "/api/v1/organizations/get-webhook-signature-key/"

	// headerAPIKey and headerIdempotency are MoneyHash's request headers.
	headerAPIKey      = "x-Api-Key"
	headerIdempotency = "X-Idempotency-Key"
)

// Gateway is the MoneyHash [payment.Gateway]. It is safe for concurrent
// use; one Gateway serves every MoneyHash connection.
type Gateway struct {
	http    *http.Client
	baseURL string
	tracer  trace.Tracer
	now     func() time.Time
	logger  *slog.Logger
	limits  *limiters
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
}

// Option configures a [Gateway].
type Option func(*config)

// WithHTTPClient sets the HTTP client whose transport carries the calls;
// the gateway wraps its transport with rate limiting and instrumentation.
func WithHTTPClient(c *http.Client) Option { return func(o *config) { o.httpClient = c } }

// WithBaseURL sets the API base URL (an httptest server in tests); by
// default it follows the connection's sandbox setting.
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

// WithClock sets the time source of payment link expiries and Retry-After
// dates; time.Now by default.
func WithClock(now func() time.Time) Option { return func(o *config) { o.now = now } }

// WithLogger sets the logger; slog.Default() by default.
func WithLogger(l *slog.Logger) Option { return func(o *config) { o.logger = l } }

// WithRateLimit sets the per-connection request rate, in requests per
// second; 0 disables throttling.
func WithRateLimit(perSecond float64) Option {
	return func(o *config) { o.rate = rate.Limit(perSecond) }
}

// New returns a MoneyHash gateway.
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
	// FlowID is the MoneyHash flow every intent runs.
	FlowID string `json:"flow_id"`
	// Sandbox selects the staging environment. Lago picks the
	// environment from the Rails environment (moneyhash_provider.rb
	// api_base_url), which cannot mix test and live connections.
	Sandbox bool `json:"sandbox,omitempty"`
}

// secrets is the connection's decrypted secrets object.
type secrets struct {
	APIKey       string `json:"api_key"`
	SignatureKey string `json:"signature_key,omitempty"`
}

func parseConn(conn payment.Connection) (settings, secrets, error) {
	var st settings
	var sc secrets
	if len(conn.Settings) > 0 {
		if err := json.Unmarshal(conn.Settings, &st); err != nil {
			return st, sc, fmt.Errorf("moneyhash: decode settings: %w", core.ErrInvalidValue)
		}
	}
	if len(conn.Secrets) > 0 {
		if err := json.Unmarshal(conn.Secrets, &sc); err != nil {
			return st, sc, fmt.Errorf("moneyhash: decode secrets: %w", core.ErrInvalidValue)
		}
	}
	return st, sc, nil
}

// ValidateConnection implements [payment.ConnectionGateway] (lago
// moneyhash_provider.rb validations): api_key and flow_id are required,
// flow_id of ≤ 20 characters.
func (g *Gateway) ValidateConnection(conn payment.Connection) error {
	st, sc, err := parseConn(conn)
	if err != nil {
		return err
	}
	var v core.Validator
	v.Required("api_key", sc.APIKey)
	if v.Required("flow_id", st.FlowID) {
		v.MaxLen("flow_id", st.FlowID, MaxFlowIDLength)
	}
	return v.Err()
}

// RegisterWebhook implements [payment.ConnectionGateway]. MoneyHash has no
// webhook endpoints to create: every intent names its webhook URL. The
// organization's signature key is fetched once, when the connection has
// none, and returned in the secrets with every other key kept (lago
// moneyhash_service.rb create_or_update, get_signature_key).
func (g *Gateway) RegisterWebhook(ctx context.Context, conn payment.Connection, _ string) (payment.ConnectionUpdate, error) {
	_, sc, err := parseConn(conn)
	if err != nil {
		return payment.ConnectionUpdate{}, err
	}
	if sc.SignatureKey != "" {
		return payment.ConnectionUpdate{}, nil
	}
	var key struct {
		Secret string `json:"webhook_signature_secret"`
	}
	err = g.do(ctx, conn, "register_webhook", func(ctx context.Context, c call) error {
		return c.send(ctx, http.MethodGet, pathSignatureKey, "", nil, &key)
	})
	if err != nil {
		return payment.ConnectionUpdate{}, err
	}
	if key.Secret == "" {
		return payment.ConnectionUpdate{}, fmt.Errorf("moneyhash register webhook: %w", &payment.ProviderError{
			Provider: string(Provider), Kind: payment.KindPermanent, Message: "no webhook signature secret",
		})
	}
	all := map[string]any{}
	if len(conn.Secrets) > 0 {
		if err := json.Unmarshal(conn.Secrets, &all); err != nil {
			return payment.ConnectionUpdate{}, fmt.Errorf("moneyhash: decode secrets: %w", core.ErrInvalidValue)
		}
	}
	all["signature_key"] = key.Secret
	b, err := json.Marshal(all)
	if err != nil {
		return payment.ConnectionUpdate{}, fmt.Errorf("moneyhash: encode secrets: %w", err)
	}
	return payment.ConnectionUpdate{Secrets: b}, nil
}

// RefreshWebhook implements [payment.ConnectionGateway]: intents carry the
// webhook URL, so there is nothing to refresh.
func (g *Gateway) RefreshWebhook(context.Context, payment.Connection, string) error { return nil }

// call is the context of one adapter operation.
type call struct {
	g        *Gateway
	conn     payment.Connection
	settings settings
	secrets  secrets
	baseURL  string
}

// endpoint returns the API base URL of the connection's environment.
func (g *Gateway) endpoint(st settings) string {
	switch {
	case g.baseURL != "":
		return g.baseURL
	case st.Sandbox:
		return SandboxURL
	default:
		return LiveURL
	}
}

// envelope is the wrapper of every MoneyHash answer.
type envelope struct {
	Status struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Errors  json.RawMessage `json:"errors"`
	} `json:"status"`
	Data json.RawMessage `json:"data"`
}

// send makes one call and decodes the answer's data into out (when not
// nil); failures are [payment.ProviderError] (see [answerError]).
func (c call) send(ctx context.Context, method, path, idempotencyKey string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set(headerAPIKey, c.secrets.APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set(headerIdempotency, idempotencyKey)
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
	var env envelope
	if err := json.Unmarshal(raw, &env); err == nil && len(env.Data) > 0 {
		err = json.Unmarshal(env.Data, out)
		if err == nil {
			return nil
		}
	}
	// A 2xx answer that cannot be read: the operation may have happened,
	// and a retry with the same key finds it.
	return &payment.ProviderError{
		Provider: string(Provider), Kind: payment.KindUnavailable, HTTPStatus: res.StatusCode,
		Message: "undecodable answer",
	}
}

// do runs op against conn inside a span, throttled by the connection's
// rate limit.
func (g *Gateway) do(ctx context.Context, conn payment.Connection, op string, f func(ctx context.Context, c call) error) error {
	st, sc, err := parseConn(conn)
	if err != nil {
		return err
	}
	if sc.APIKey == "" {
		return fmt.Errorf("moneyhash %s: %w", op, &payment.ProviderError{
			Provider: string(Provider), Kind: payment.KindAuthentication, Message: "missing api key",
		})
	}
	ctx, span := g.tracer.Start(ctx, "moneyhash."+op,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("payment.provider", string(Provider)),
			attribute.String("tanvo.organization.id", conn.OrganizationID.String()),
			attribute.String("tanvo.integration.id", conn.ID.String()),
		))
	defer span.End()
	ctx = g.limits.with(ctx, conn.ID)
	if err := f(ctx, call{g: g, conn: conn, settings: st, secrets: sc, baseURL: g.endpoint(st)}); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, op)
		if pe, ok := payment.AsProviderError(err); ok {
			span.SetAttributes(attribute.String("payment.error.kind", string(pe.Kind)), attribute.String("payment.error.code", pe.Code))
		}
		return fmt.Errorf("moneyhash %s: %w", op, err)
	}
	return nil
}
