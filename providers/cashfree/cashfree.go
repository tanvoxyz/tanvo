package cashfree

import (
	"bytes"
	"context"
	"crypto/rand"
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
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Provider is the vendor this adapter serves.
const Provider = integration.ProviderCashfree

const (
	// instrumentationName names the tracer and meter.
	instrumentationName = "github.com/tanvoxyz/tanvo/providers/cashfree"

	// LiveURL and SandboxURL are Cashfree's Payment Gateway API servers
	// (lago CashfreeProvider::BASE_URL without its /links path).
	LiveURL    = "https://api.cashfree.com/pg"
	SandboxURL = "https://sandbox.cashfree.com/pg"

	// APIVersion is the x-api-version of every call (lago
	// CashfreeProvider::API_VERSION).
	APIVersion = "2023-08-01"

	// DefaultSuccessRedirectURL is where customers return after paying a
	// link when the connection sets none (lago
	// CashfreeProvider::SUCCESS_REDIRECT_URL).
	DefaultSuccessRedirectURL = "https://cashfree.com/"

	// MaxSuccessRedirectURLLength bounds success_redirect_url (lago
	// cashfree_provider.rb validates length ≤ 1024).
	MaxSuccessRedirectURLLength = 1024

	// DefaultRate is the per-connection request rate, in requests per
	// second. Cashfree throttles per merchant and answers 429 beyond its
	// limit, which is retryable whatever the rate. Lago has no client-side
	// throttle.
	DefaultRate = 10.0

	// defaultTimeout is the HTTP timeout of a call.
	defaultTimeout = 60 * time.Second

	// pathLinks is the payment links endpoint.
	pathLinks = "/links"
)

// Gateway is the Cashfree [payment.Gateway]. It is safe for concurrent
// use; one Gateway serves every Cashfree connection.
type Gateway struct {
	http    *http.Client
	baseURL string
	tracer  trace.Tracer
	now     func() time.Time
	logger  *slog.Logger
	limits  *limiters
	linkID  func() string
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
	linkID         func() string
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

// WithLinkIDs sets the source of payment link IDs; random 26-character
// base32 IDs by default.
func WithLinkIDs(next func() string) Option { return func(o *config) { o.linkID = next } }

// New returns a Cashfree gateway.
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
	if c.linkID == nil {
		c.linkID = randomLinkID
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
		linkID:  c.linkID,
	}
}

// randomLinkID returns a random link ID. Like lago ("<uuid>.<attempts>",
// invoices/payments/cashfree_service.rb payment_url_params), every link
// gets a new ID; 26 random base32 characters fit Cashfree's 50-character
// alphanumeric limit.
func randomLinkID() string { return rand.Text() }

// Provider implements [payment.ConnectionGateway].
func (g *Gateway) Provider() integration.Provider { return Provider }

// settings is the connection's settings object.
type settings struct {
	// SuccessRedirectURL is where customers return after paying.
	SuccessRedirectURL *string `json:"success_redirect_url,omitempty"`
	// Sandbox selects the sandbox environment. Lago picks the environment
	// from the Rails environment (cashfree_provider.rb BASE_URL), which
	// cannot mix test and live connections.
	Sandbox bool `json:"sandbox,omitempty"`
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
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

func parseConn(conn payment.Connection) (settings, secrets, error) {
	var st settings
	var sc secrets
	if len(conn.Settings) > 0 {
		if err := json.Unmarshal(conn.Settings, &st); err != nil {
			return st, sc, fmt.Errorf("cashfree: decode settings: %w", core.ErrInvalidValue)
		}
	}
	if len(conn.Secrets) > 0 {
		if err := json.Unmarshal(conn.Secrets, &sc); err != nil {
			return st, sc, fmt.Errorf("cashfree: decode secrets: %w", core.ErrInvalidValue)
		}
	}
	return st, sc, nil
}

// ValidateConnection implements [payment.ConnectionGateway] (lago
// cashfree_provider.rb validations): client_id and client_secret are
// required; success_redirect_url, when set, is a valid URL of ≤ 1024
// characters.
func (g *Gateway) ValidateConnection(conn payment.Connection) error {
	st, sc, err := parseConn(conn)
	if err != nil {
		return err
	}
	var v core.Validator
	v.Required("client_id", sc.ClientID)
	v.Required("client_secret", sc.ClientSecret)
	if u := st.SuccessRedirectURL; u != nil {
		if v.MaxLen("success_redirect_url", *u, MaxSuccessRedirectURLLength) {
			v.Check(webhook.ValidURL(*u), "success_redirect_url", webhook.CodeInvalidURL)
		}
	}
	return v.Err()
}

// RegisterWebhook implements [payment.ConnectionGateway]. Cashfree's
// webhook endpoint is set in its dashboard and deliveries are signed with
// the client secret, so there is nothing to create or store (lago
// cashfree_service.rb create_or_update).
func (g *Gateway) RegisterWebhook(context.Context, payment.Connection, string) (payment.ConnectionUpdate, error) {
	return payment.ConnectionUpdate{}, nil
}

// RefreshWebhook implements [payment.ConnectionGateway]; see
// RegisterWebhook.
func (g *Gateway) RefreshWebhook(context.Context, payment.Connection, string) error { return nil }

// call is the context of one adapter operation.
type call struct {
	g        *Gateway
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
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("x-client-id", c.secrets.ClientID)
	req.Header.Set("x-client-secret", c.secrets.ClientSecret)
	req.Header.Set("x-api-version", APIVersion)
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
		// A 2xx answer that cannot be read: the link may exist.
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
	if sc.ClientID == "" || sc.ClientSecret == "" {
		return fmt.Errorf("cashfree %s: %w", op, &payment.ProviderError{
			Provider: string(Provider), Kind: payment.KindAuthentication, Message: "missing client credentials",
		})
	}
	ctx, span := g.tracer.Start(ctx, "cashfree."+op,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("payment.provider", string(Provider)),
			attribute.String("tanvo.organization.id", conn.OrganizationID.String()),
			attribute.String("tanvo.integration.id", conn.ID.String()),
		))
	defer span.End()
	ctx = g.limits.with(ctx, conn.ID)
	if err := f(ctx, call{g: g, settings: st, secrets: sc, baseURL: g.endpoint(st)}); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, op)
		if pe, ok := payment.AsProviderError(err); ok {
			span.SetAttributes(attribute.String("payment.error.kind", string(pe.Kind)), attribute.String("payment.error.code", pe.Code))
		}
		return fmt.Errorf("cashfree %s: %w", op, err)
	}
	return nil
}
