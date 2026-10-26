package apiclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"golang.org/x/time/rate"

	"github.com/tanvoxyz/tanvo/libs/core/integration"
)

// MaxBodySize bounds the response body read from a provider.
const MaxBodySize = 16 << 20

// DefaultTimeout bounds one HTTP request.
const DefaultTimeout = 60 * time.Second

// Config configures a [Client].
type Config struct {
	// Provider is the vendor, recorded on spans and errors.
	Provider integration.Provider
	// InstrumentationName names the tracer ("github.com/tanvoxyz/tanvo/providers/anrok").
	InstrumentationName string
	// HTTPClient supplies the base transport and timeout; nil uses
	// http.DefaultTransport and [DefaultTimeout].
	HTTPClient *http.Client
	// TracerProvider defaults to a no-op provider.
	TracerProvider trace.TracerProvider
	// MeterProvider defaults to a no-op provider.
	MeterProvider metric.MeterProvider
	// Rate is the request rate per limiter key in requests per second; 0
	// disables throttling.
	Rate float64
	// Burst is the bucket size; 0 means max(1, Rate).
	Burst int
	// Windows are request quotas per limiter key over fixed windows; a
	// call over one fails with a request limit instead of waiting.
	Windows []Window
	// Now is the clock of Windows; nil uses time.Now.
	Now func() time.Time
	// Counter, when set, counts the requests of every server process
	// together (lago counts in Rails.cache); nil counts in this process.
	Counter Counter
}

// Counter is a counter shared by the server processes, the Redis or
// Valkey server of the installation (libs/cache Valkey.Incr).
type Counter interface {
	// Incr adds one to the counter key and returns its new value; the
	// counter starts at zero and expires ttl after its first increment.
	Incr(ctx context.Context, key string, ttl time.Duration) (int64, error)
}

// Window is a quota of Limit requests per limiter key in each Period,
// the periods aligned on the Unix epoch, as lago's throttling gem
// (throttling 0.4.1 lib/throttling/base.rb:44-77).
type Window struct {
	// Limit is the number of requests a window allows.
	Limit int
	// Period is the window length, a whole number of seconds.
	Period time.Duration
}

// windowCount counts the requests of one key in one window.
type windowCount struct {
	start int64
	n     int
}

// Client sends the requests of one adapter. It is safe for concurrent use.
type Client struct {
	provider integration.Provider
	hc       *http.Client
	tracer   trace.Tracer
	rate     rate.Limit
	burst    int
	windows  []Window
	now      func() time.Time
	counter  Counter
	// period is the window of the shared rate limit: Burst requests in
	// Burst/Rate.
	period time.Duration

	mu     sync.Mutex
	limits map[string]*rate.Limiter
	counts map[string][]windowCount
}

// New returns a client.
func New(c Config) *Client {
	if c.TracerProvider == nil {
		c.TracerProvider = tracenoop.NewTracerProvider()
	}
	if c.MeterProvider == nil {
		c.MeterProvider = metricnoop.NewMeterProvider()
	}
	base := http.DefaultTransport
	timeout := DefaultTimeout
	if c.HTTPClient != nil {
		if c.HTTPClient.Transport != nil {
			base = c.HTTPClient.Transport
		}
		if c.HTTPClient.Timeout != 0 {
			timeout = c.HTTPClient.Timeout
		}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	burst := c.Burst
	if burst <= 0 {
		burst = max(int(c.Rate), 1)
	}
	var period time.Duration
	if c.Rate > 0 {
		period = time.Duration(float64(burst) / c.Rate * float64(time.Second)).Round(time.Millisecond)
	}
	return &Client{
		provider: c.Provider,
		hc: &http.Client{
			Timeout: timeout,
			Transport: otelhttp.NewTransport(base,
				otelhttp.WithTracerProvider(c.TracerProvider),
				otelhttp.WithMeterProvider(c.MeterProvider),
			),
		},
		tracer:  c.TracerProvider.Tracer(c.InstrumentationName),
		rate:    rate.Limit(c.Rate),
		burst:   burst,
		windows: slices.DeleteFunc(slices.Clone(c.Windows), func(w Window) bool { return w.Limit <= 0 || w.Period < time.Second }),
		now:     c.Now,
		counter: c.Counter,
		period:  period,
		limits:  map[string]*rate.Limiter{},
		counts:  map[string][]windowCount{},
	}
}

// Request is one HTTP call.
type Request struct {
	// Method defaults to POST.
	Method string
	// URL is the absolute URL.
	URL string
	// Header is added to the request.
	Header http.Header
	// JSON is encoded as the body with Content-Type application/json,
	// unless Body is set.
	JSON any
	// Body is the raw body, sent with ContentType.
	Body []byte
	// ContentType is the type of Body.
	ContentType string
	// Authorize, when set, signs the prepared request (OAuth 1.0a).
	Authorize func(*http.Request) error
}

// Response is a received answer, whatever its status.
type Response struct {
	// Status is the HTTP status code.
	Status int
	// Header is the response header.
	Header http.Header
	// Body is the response body, at most [MaxBodySize] bytes.
	Body []byte
}

// OK reports a 2xx status.
func (r *Response) OK() bool { return r.Status >= 200 && r.Status < 300 }

// Decode unmarshals the JSON body into v.
func (r *Response) Decode(v any) error {
	if err := json.Unmarshal(r.Body, v); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// RetryAfter is the delay of a Retry-After header in seconds, or 0.
func (r *Response) RetryAfter() time.Duration {
	s := strings.TrimSpace(r.Header.Get("Retry-After"))
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 0
}

// Call is the identity of one operation: the connection it runs for and
// the key it is throttled on (the connection ID when empty).
type Call struct {
	// Conn is the connection.
	Conn integration.Connection
	// Op names the operation ("createOrUpdate"); the span is
	// "<provider>.<op>".
	Op string
	// LimitKey overrides the limiter key: lago throttles NetSuite by
	// client ID and Anrok by API key, across connections sharing them.
	LimitKey string
	// Unmetered leaves the call out of the [Window] quotas (an OAuth
	// exchange, outside the vendor's API quota).
	Unmetered bool
}

// Run executes f inside the operation's client span. An error is
// recorded on the span with its [integration.ProviderError] kind and
// code.
func (c *Client) Run(ctx context.Context, call Call, f func(ctx context.Context) error) error {
	ctx, span := c.tracer.Start(ctx, string(c.provider)+"."+call.Op,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("integration.provider", string(c.provider)),
			attribute.String("tanvo.organization.id", call.Conn.OrganizationID.String()),
			attribute.String("tanvo.integration.id", call.Conn.ID.String()),
		))
	defer span.End()
	ctx = context.WithValue(ctx, callKey{}, call)
	if err := f(ctx); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, call.Op)
		if pe, ok := integration.AsProviderError(err); ok {
			span.SetAttributes(attribute.String("integration.error.kind", string(pe.Kind)), attribute.String("integration.error.code", pe.Code))
		}
		return err
	}
	return nil
}

type callKey struct{}

// Do sends req, throttled on the limiter of the [Call] that ctx carries
// (from [Client.Run]), and returns the response whatever its status. A
// transport failure is a [integration.ProviderError] of
// [integration.KindUnavailable]; a cancelled context is returned as is.
func (c *Client) Do(ctx context.Context, req Request) (*Response, error) {
	if call, ok := ctx.Value(callKey{}).(Call); ok {
		if err := c.wait(ctx, call); err != nil {
			return nil, err
		}
	}
	method := req.Method
	if method == "" {
		method = http.MethodPost
	}
	body, ctype := req.Body, req.ContentType
	if body == nil && req.JSON != nil {
		b, err := json.Marshal(req.JSON)
		if err != nil {
			return nil, fmt.Errorf("%s: encode request: %w", c.provider, err)
		}
		body, ctype = b, "application/json"
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	hr, err := http.NewRequestWithContext(ctx, method, req.URL, rd)
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", c.provider, err)
	}
	for k, vs := range req.Header {
		for _, v := range vs {
			hr.Header.Add(k, v)
		}
	}
	if ctype != "" {
		hr.Header.Set("Content-Type", ctype)
	}
	if hr.Header.Get("Accept") == "" {
		hr.Header.Set("Accept", "application/json")
	}
	if req.Authorize != nil {
		if err := req.Authorize(hr); err != nil {
			return nil, fmt.Errorf("%s: sign request: %w", c.provider, err)
		}
	}
	resp, err := c.hc.Do(hr)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &integration.ProviderError{Provider: c.provider, Kind: integration.KindUnavailable, Message: err.Error()}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodySize))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &integration.ProviderError{Provider: c.provider, Kind: integration.KindUnavailable, HTTPStatus: resp.StatusCode, Message: err.Error()}
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: b}, nil
}

func (c *Client) wait(ctx context.Context, call Call) error {
	key := call.LimitKey
	if key == "" {
		key = call.Conn.ID.String()
	}
	if !call.Unmetered {
		if err := c.count(ctx, key); err != nil {
			return err
		}
	}
	if c.rate <= 0 {
		return nil
	}
	if c.counter != nil {
		err := c.waitShared(ctx, key)
		if err == nil || ctx.Err() != nil {
			return err
		}
		// The shared counter failed (logged by it): this process's own
		// bucket throttles until it answers again.
	}
	c.mu.Lock()
	lim, ok := c.limits[key]
	if !ok {
		lim = rate.NewLimiter(c.rate, c.burst)
		c.limits[key] = lim
	}
	c.mu.Unlock()
	if err := lim.Wait(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("%s rate limit: %w", c.provider, err)
	}
	return nil
}

// waitShared takes one of the Burst requests of key's current window of
// Burst/Rate on the shared counter, the windows aligned on the Unix epoch
// as lago's (110 in 10 seconds for HubSpot, 60 a minute for Xero, 10 a
// second for NetSuite), waiting for the next window while this one is
// used up. Lago refuses the request instead and retries its job.
func (c *Client) waitShared(ctx context.Context, key string) error {
	p := c.period.Milliseconds()
	for {
		now := c.now().UnixMilli()
		start := now - now%p
		n, err := c.counter.Incr(ctx, c.counterKey(key, "rate", start), time.Duration(start+2*p-now)*time.Millisecond)
		if err != nil {
			return err
		}
		if n <= int64(c.burst) {
			return nil
		}
		t := time.NewTimer(time.Duration(start+p-now) * time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// counterKey is the shared counter of key in the window of kind starting
// at start: the key is hashed, since a limiter key may be a credential
// (lago throttles Anrok by API key).
func (c *Client) counterKey(key, kind string, start int64) string {
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%s/%s/%s/%d", c.provider, hex.EncodeToString(sum[:12]), kind, start)
}

// count records a request of key in each window, unless one is used up:
// that is a request limit retried when the window ends (lago
// BaseService::ThrottlingError, aggregator/base_service.rb:69-76). With
// a [Counter] the windows are counted across processes; when it fails,
// in this process.
func (c *Client) count(ctx context.Context, key string) error {
	if len(c.windows) == 0 {
		return nil
	}
	now := c.now().Unix()
	if c.counter != nil {
		err := c.countShared(ctx, key, now)
		if err == nil || ctx.Err() != nil {
			return err
		}
		if _, ok := integration.AsProviderError(err); ok {
			return err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	counts := c.counts[key]
	if counts == nil {
		counts = make([]windowCount, len(c.windows))
		c.counts[key] = counts
	}
	for i, w := range c.windows {
		p := int64(w.Period / time.Second)
		if start := now - now%p; counts[i].start != start {
			counts[i] = windowCount{start: start}
		}
		if counts[i].n >= w.Limit {
			return c.throttled(w, counts[i].start+p-now)
		}
	}
	for i := range counts {
		counts[i].n++
	}
	return nil
}

// countShared counts a request of key in each window on the shared
// counter, in one atomic increment per window where lago's throttling gem
// reads and then increments (throttling 0.4.1 lib/throttling/base.rb). As
// there, a request refused by a window still counts in the windows before
// it.
func (c *Client) countShared(ctx context.Context, key string, now int64) error {
	for _, w := range c.windows {
		p := int64(w.Period / time.Second)
		start := now - now%p
		n, err := c.counter.Incr(ctx, c.counterKey(key, strconv.FormatInt(p, 10)+"s", start), time.Duration(start+2*p-now)*time.Second)
		if err != nil {
			return err
		}
		if n > int64(w.Limit) {
			return c.throttled(w, start+p-now)
		}
	}
	return nil
}

// throttled is the request limit of window w, which ends in left seconds.
func (c *Client) throttled(w Window, left int64) error {
	return &integration.ProviderError{
		Provider: c.provider, Kind: integration.KindRequestLimit, Code: CodeThrottled,
		Message:    fmt.Sprintf("over %d requests per %s", w.Limit, w.Period),
		RetryAfter: time.Duration(left) * time.Second,
	}
}

// CodeThrottled is the code of a request refused by a [Window].
const CodeThrottled = "throttled"

// Kind is the default classification of an HTTP status: 429 is a
// request limit; 401 and 403 an authentication failure; 404 not found;
// 408, 5xx and other unexpected statuses unavailable; any other 4xx an
// invalid request.
func Kind(status int) integration.ErrorKind {
	switch {
	case status == http.StatusTooManyRequests:
		return integration.KindRequestLimit
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return integration.KindAuthentication
	case status == http.StatusNotFound:
		return integration.KindNotFound
	case status == http.StatusRequestTimeout || status >= 500:
		return integration.KindUnavailable
	case status >= 400:
		return integration.KindInvalidRequest
	default:
		return integration.KindUnavailable
	}
}

// Error returns the default [integration.ProviderError] of a failed
// response: its kind from [Kind], the given code and message, the status
// and the Retry-After delay.
func (r *Response) Error(provider integration.Provider, code, message string) *integration.ProviderError {
	return &integration.ProviderError{
		Provider: provider, Kind: Kind(r.Status), Code: code, Message: message,
		HTTPStatus: r.Status, RetryAfter: r.RetryAfter(),
	}
}

// Snippet is the start of the body for error messages.
func (r *Response) Snippet() string {
	s := strings.TrimSpace(string(r.Body))
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}
