package x402

import (
	"crypto/rand"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

const instrumentationName = "github.com/tanvoxyz/tanvo/providers/x402"

type config struct {
	httpClient     *http.Client
	baseURL        string
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
	now            func() time.Time
	logger         *slog.Logger
	random         io.Reader
	rpcURLs        map[x402.Network]string
	noRetrySleep   bool
}

// Option configures [Facilitators] and [ChainReaders].
type Option func(*config)

// WithHTTPClient sets the HTTP client whose transport carries the calls;
// its transport is wrapped with instrumentation.
func WithHTTPClient(c *http.Client) Option { return func(o *config) { o.httpClient = c } }

// WithBaseURL sets the facilitator API base URL (an httptest server in
// tests); the signed tokens stay bound to the CDP host and path.
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

// WithClock sets the time source of the token claims; time.Now by
// default.
func WithClock(now func() time.Time) Option { return func(o *config) { o.now = now } }

// WithLogger sets the logger; slog.Default() by default.
func WithLogger(l *slog.Logger) Option { return func(o *config) { o.logger = l } }

// WithRandom sets the source of the token nonces and signatures;
// crypto/rand by default.
func WithRandom(r io.Reader) Option { return func(o *config) { o.random = r } }

// WithRPCURLs sets the JSON-RPC endpoint of networks, over the public
// defaults of [DefaultRPCURLs].
func WithRPCURLs(urls map[x402.Network]string) Option {
	return func(o *config) { maps.Copy(o.rpcURLs, urls) }
}

// withoutRetrySleep makes JSON-RPC retries immediate; for tests.
func withoutRetrySleep() Option { return func(o *config) { o.noRetrySleep = true } }

func newConfig(opts []Option) config {
	c := config{rpcURLs: DefaultRPCURLs()}
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
	if c.random == nil {
		c.random = rand.Reader
	}
	return c
}

// client returns the instrumented HTTP client. Redirects are not
// followed: an answer is classified by its own status.
func (c config) client() *http.Client {
	base := http.DefaultTransport
	if c.httpClient != nil && c.httpClient.Transport != nil {
		base = c.httpClient.Transport
	}
	return &http.Client{
		Transport: otelhttp.NewTransport(base,
			otelhttp.WithTracerProvider(c.tracerProvider),
			otelhttp.WithMeterProvider(c.meterProvider),
		),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
