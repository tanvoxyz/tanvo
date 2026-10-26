package apiclient

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
)

func TestKind(t *testing.T) {
	tests := []struct {
		status int
		want   integration.ErrorKind
	}{
		{429, integration.KindRequestLimit},
		{401, integration.KindAuthentication},
		{403, integration.KindAuthentication},
		{404, integration.KindNotFound},
		{408, integration.KindUnavailable},
		{400, integration.KindInvalidRequest},
		{409, integration.KindInvalidRequest},
		{500, integration.KindUnavailable},
		{502, integration.KindUnavailable},
	}
	for _, tt := range tests {
		if got := Kind(tt.status); got != tt.want {
			t.Errorf("Kind(%d) = %s, want = %s", tt.status, got, tt.want)
		}
	}
}

func TestDo(t *testing.T) {
	var gotBody, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotType = string(b), r.Header.Get("Content-Type")
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("slow down"))
	}))
	defer srv.Close()

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	c := New(Config{Provider: integration.ProviderAnrok, TracerProvider: tp, Rate: 100})
	conn := integration.Connection{ID: core.MustParseID[core.IntegrationID]("0199a000-0000-7000-8000-000000000001")}
	err := c.Run(context.Background(), Call{Conn: conn, Op: "void"}, func(ctx context.Context) error {
		resp, err := c.Do(ctx, Request{URL: srv.URL, JSON: map[string]string{"a": "b"}})
		if err != nil {
			return err
		}
		if resp.OK() {
			t.Errorf("OK() = true, want = false")
		}
		return resp.Error(integration.ProviderAnrok, "", resp.Snippet())
	})
	pe, ok := integration.AsProviderError(err)
	if !ok || pe.Kind != integration.KindRequestLimit || pe.RetryAfter != 3*time.Second || pe.Message != "slow down" {
		t.Fatalf("error = %#v, want = request limit, 3s, slow down", err)
	}
	if gotBody != `{"a":"b"}` || gotType != "application/json" {
		t.Errorf("body = %s (%s), want = JSON", gotBody, gotType)
	}
	var names []string
	for _, s := range rec.Ended() {
		names = append(names, s.Name())
	}
	if len(names) == 0 || names[len(names)-1] != "anrok.void" {
		t.Errorf("spans = %v, want = anrok.void last", names)
	}
}

func TestDoUnavailable(t *testing.T) {
	c := New(Config{Provider: integration.ProviderXero})
	_, err := c.Do(context.Background(), Request{URL: "http://127.0.0.1:1/x"})
	if !errors.Is(err, integration.ErrRetryable) {
		t.Errorf("Do() error = %v, want = retryable", err)
	}
}

// TestThrottle covers the per-key token buckets that stand in for lago's
// throttle! (app/services/integrations/aggregator/base_service.rb:69-88):
// calls past the burst wait for a token instead of failing, connections
// are limited separately, and a LimitKey is shared across connections.
func TestThrottle(t *testing.T) {
	// Every aggregator service throttles its calls by provider:
	// source: spec/services/integrations/aggregator/companies/create_service_spec.rb:100, 143, 163, 225, 260, 295
	// source: spec/services/integrations/aggregator/companies/update_service_spec.rb:104, 147, 218, 253, 288
	// source: spec/services/integrations/aggregator/contacts/create_service_spec.rb:111, 156, 176, 242, 357, 392
	// source: spec/services/integrations/aggregator/contacts/update_service_spec.rb:83, 126, 201, 236, 271
	// source: spec/services/integrations/aggregator/credit_notes/create_service_spec.rb:322, 342, 370, 411, 454
	// source: spec/services/integrations/aggregator/invoices/hubspot/create_customer_association_service_spec.rb:82
	// source: spec/services/integrations/aggregator/invoices/hubspot/create_service_spec.rb:179, 199, 226, 244
	// source: spec/services/integrations/aggregator/invoices/hubspot/update_service_spec.rb:135, 151, 178, 196
	// source: spec/services/integrations/aggregator/invoices/reconcile_service_spec.rb:71
	// source: spec/services/integrations/aggregator/payments/create_service_spec.rb:139, 166, 186, 232
	// source: spec/services/integrations/aggregator/subscriptions/hubspot/create_customer_association_service_spec.rb:71
	// source: spec/services/integrations/aggregator/subscriptions/hubspot/create_service_spec.rb:115, 135, 163, 181
	// source: spec/services/integrations/aggregator/subscriptions/hubspot/update_service_spec.rb:105, 121, 149, 167
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	a := integration.Connection{ID: core.MustParseID[core.IntegrationID]("0199a000-0000-7000-8000-000000000001")}
	b := integration.Connection{ID: core.MustParseID[core.IntegrationID]("0199a000-0000-7000-8000-000000000002")}
	elapsed := func(c *Client, calls ...Call) time.Duration {
		start := time.Now()
		for _, call := range calls {
			err := c.Run(context.Background(), call, func(ctx context.Context) error {
				_, err := c.Do(ctx, Request{URL: srv.URL, Method: http.MethodGet})
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		return time.Since(start)
	}
	const step = 100 * time.Millisecond
	newClient := func() *Client { return New(Config{Provider: integration.ProviderNetsuite, Rate: 10, Burst: 1}) }
	if d := elapsed(newClient(), Call{Conn: a}, Call{Conn: a}, Call{Conn: a}); d < 2*step-20*time.Millisecond {
		t.Errorf("same connection: got = %v, want = at least %v", d, 2*step)
	}
	if d := elapsed(newClient(), Call{Conn: a}, Call{Conn: b}); d >= step {
		t.Errorf("two connections: got = %v, want = under %v", d, step)
	}
	if d := elapsed(newClient(), Call{Conn: a, LimitKey: "k"}, Call{Conn: b, LimitKey: "k"}); d < step-20*time.Millisecond {
		t.Errorf("shared key: got = %v, want = at least %v", d, step)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := newClient()
	_ = elapsed(c, Call{Conn: a})
	err := c.Run(ctx, Call{Conn: a}, func(ctx context.Context) error {
		_, err := c.Do(ctx, Request{URL: srv.URL, Method: http.MethodGet})
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled wait: got = %v, want = context.Canceled", err)
	}
}

func TestWindows(t *testing.T) {
	// Xero's daily quota (lago config/initializers/throttling.rb, the
	// throttling gem's epoch-aligned windows): a call over the quota is a
	// request limit until the window ends, per limiter key.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	now := time.Date(2026, 3, 1, 23, 0, 0, 0, time.UTC)
	c := New(Config{Provider: integration.ProviderXero, Windows: []Window{{Limit: 2, Period: 24 * time.Hour}}, Now: func() time.Time { return now }})
	a := integration.Connection{ID: core.MustParseID[core.IntegrationID]("0199a000-0000-7000-8000-000000000001")}
	call := func(key string) error {
		return c.Run(context.Background(), Call{Conn: a, LimitKey: key}, func(ctx context.Context) error {
			_, err := c.Do(ctx, Request{URL: srv.URL, Method: http.MethodGet})
			return err
		})
	}
	tests := []struct {
		name  string
		at    time.Time
		key   string
		retry time.Duration // 0: allowed
	}{
		{name: "first", at: now, key: "t1"},
		{name: "second", at: now.Add(time.Minute), key: "t1"},
		{name: "over the quota", at: now.Add(30 * time.Minute), key: "t1", retry: 30 * time.Minute},
		{name: "another key", at: now.Add(30 * time.Minute), key: "t2"},
		{name: "next window", at: now.Add(time.Hour), key: "t1"},
	}
	for _, tt := range tests {
		now = tt.at
		err := call(tt.key)
		if tt.retry == 0 {
			if err != nil {
				t.Errorf("%s: error got = %v, want = nil", tt.name, err)
			}
			continue
		}
		pe, ok := integration.AsProviderError(err)
		if !ok || !errors.Is(err, integration.ErrRequestLimit) || pe.Code != CodeThrottled || pe.RetryAfter != tt.retry {
			t.Errorf("%s: error got = %v, want = request limit retried after %v", tt.name, err, tt.retry)
		}
	}
}

// sharedCounter is a Counter in a map, standing for the cache server of
// several processes; fail makes every increment fail.
type sharedCounter struct {
	mu   sync.Mutex
	n    map[string]int64
	fail bool
}

func (s *sharedCounter) Incr(_ context.Context, key string, ttl time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return 0, errors.New("unreachable")
	}
	if ttl <= 0 {
		return 0, errors.New("no ttl")
	}
	s.n[key]++
	return s.n[key], nil
}

func (s *sharedCounter) keys() map[string]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.n)
}

func TestSharedWindows(t *testing.T) {
	// The quota is counted across processes, as lago's in Rails.cache.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	now := time.Date(2026, 3, 1, 23, 0, 0, 0, time.UTC)
	counter := &sharedCounter{n: map[string]int64{}}
	cfg := Config{Provider: integration.ProviderXero, Windows: []Window{{Limit: 2, Period: 24 * time.Hour}}, Now: func() time.Time { return now }, Counter: counter}
	one, two := New(cfg), New(cfg)
	a := integration.Connection{ID: core.MustParseID[core.IntegrationID]("0199a000-0000-7000-8000-000000000001")}
	call := func(c *Client) error {
		return c.Run(context.Background(), Call{Conn: a, LimitKey: "tenant-secret"}, func(ctx context.Context) error {
			_, err := c.Do(ctx, Request{URL: srv.URL, Method: http.MethodGet})
			return err
		})
	}
	if err := call(one); err != nil {
		t.Fatal(err)
	}
	if err := call(two); err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Minute)
	err := call(one)
	if pe, ok := integration.AsProviderError(err); !ok || pe.Code != CodeThrottled || pe.RetryAfter != 30*time.Minute {
		t.Errorf("third call: error got = %v, want = request limit retried after 30m", err)
	}
	for k := range counter.keys() {
		if strings.Contains(k, "tenant-secret") {
			t.Errorf("counter key got = %q, want = the limiter key hashed", k)
		}
	}
	// Without the shared counter, each process counts on its own.
	counter.fail = true
	if err := call(one); err != nil {
		t.Errorf("call with the counter down: error got = %v, want = nil", err)
	}
	now = now.Add(time.Hour)
	counter.fail = false
	if err := call(one); err != nil {
		t.Errorf("call in the next window: error got = %v, want = nil", err)
	}
}

func TestSharedThrottle(t *testing.T) {
	// A rate of Burst requests in Burst/Rate is shared by the processes:
	// two requests of each window of 200ms between both clients.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	counter := &sharedCounter{n: map[string]int64{}}
	cfg := Config{Provider: integration.ProviderHubspot, Rate: 10, Burst: 2, Counter: counter}
	one, two := New(cfg), New(cfg)
	a := integration.Connection{ID: core.MustParseID[core.IntegrationID]("0199a000-0000-7000-8000-000000000001")}
	for i, c := range []*Client{one, two, one, two, one} {
		err := c.Run(context.Background(), Call{Conn: a}, func(ctx context.Context) error {
			_, err := c.Do(ctx, Request{URL: srv.URL, Method: http.MethodGet})
			return err
		})
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	var granted int64
	keys := counter.keys()
	for _, n := range keys {
		granted += min(n, 2)
	}
	if granted != 5 || len(keys) < 3 {
		t.Errorf("windows got = %v, want = 5 requests over at least 3 windows of 2", keys)
	}
	// A cancelled wait for the next window returns at once.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := one.Run(ctx, Call{Conn: a}, func(ctx context.Context) error {
		_, err := one.Do(ctx, Request{URL: srv.URL, Method: http.MethodGet})
		return err
	}); err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled wait: got = %v, want = nil or context.Canceled", err)
	}
	// The counter down, each process throttles on its own bucket.
	counter.fail = true
	if err := two.Run(context.Background(), Call{Conn: a}, func(ctx context.Context) error {
		_, err := two.Do(ctx, Request{URL: srv.URL, Method: http.MethodGet})
		return err
	}); err != nil {
		t.Errorf("call with the counter down: error got = %v, want = nil", err)
	}
}
