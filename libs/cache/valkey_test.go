package cache_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/tanvoxyz/tanvo/libs/cache"
	"github.com/tanvoxyz/tanvo/libs/cache/cachetest"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/dockertest"
)

// ping sends PING to the server at addr over a raw connection.
func ping(ctx context.Context, addr string) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("PING\r\n")); err != nil {
		return err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return err
	}
	if line != "+PONG\r\n" {
		return fmt.Errorf("PING = %q, want = +PONG", line)
	}
	return nil
}

// backends are the servers of the integration tests.
func backends(t *testing.T) []struct{ name, url string } {
	t.Helper()
	if servers.valkey == "" || servers.redis == "" {
		t.Skip(servers.skip)
	}
	return []struct{ name, url string }{{"valkey", servers.valkey}, {"redis", servers.redis}}
}

// uniquePrefix returns a key prefix no other test uses.
func uniquePrefix() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "test:" + hex.EncodeToString(b) + ":"
}

// newValkey returns a connected cache, closed when the test ends.
func newValkey(t *testing.T, rawURL string, opts ...cache.ValkeyOption) *cache.Valkey {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v, err := cache.NewValkey(ctx, rawURL, append([]cache.ValkeyOption{cache.WithPrefix(uniquePrefix())}, opts...)...)
	if err != nil {
		t.Fatalf("NewValkey() = %v", err)
	}
	t.Cleanup(v.Close)
	if err := v.Ping(ctx); err != nil {
		t.Fatalf("Ping() = %v", err)
	}
	return v
}

func TestValkeyConformance(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			cachetest.Run(t, func(t *testing.T) port.Cache { return newValkey(t, b.url) })
		})
	}
}

func TestValkeyNamespaceConformance(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			cachetest.Run(t, func(t *testing.T) port.Cache { return cache.Namespace(newValkey(t, b.url), "ns") })
		})
	}
}

func TestValkeyPrefix(t *testing.T) {
	ctx := context.Background()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			prefix := uniquePrefix()
			plain := newValkey(t, b.url, cache.WithPrefix(""))
			prefixed := newValkey(t, b.url, cache.WithPrefix(prefix))
			prefixed.Set(ctx, "k", []byte("v"), time.Hour)
			if got, ok := plain.Get(ctx, prefix+"k"); !ok || string(got) != "v" {
				t.Errorf("Get(%sk) without prefix = %q, %v, want = %q, true", prefix, got, ok, "v")
			}
			plain.Delete(ctx, prefix+"k")
		})
	}
}

func TestValkeyIncr(t *testing.T) {
	ctx := context.Background()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			prefix := uniquePrefix()
			// Two processes share the counter.
			one := newValkey(t, b.url, cache.WithPrefix(prefix))
			two := newValkey(t, b.url, cache.WithPrefix(prefix))
			var wg sync.WaitGroup
			for _, v := range []*cache.Valkey{one, two} {
				for range 10 {
					wg.Go(func() {
						if _, err := v.Incr(ctx, "n", time.Minute); err != nil {
							t.Errorf("Incr(n) error = %v", err)
						}
					})
				}
			}
			wg.Wait()
			if got, err := one.Incr(ctx, "n", time.Minute); err != nil || got != 21 {
				t.Errorf("Incr(n) = %d, %v, want = 21, nil", got, err)
			}
			// The counter expires after the TTL of its first increment.
			if got, err := one.Incr(ctx, "short", 50*time.Millisecond); err != nil || got != 1 {
				t.Fatalf("Incr(short) = %d, %v, want = 1, nil", got, err)
			}
			if got, _ := one.Incr(ctx, "short", time.Hour); got != 2 {
				t.Fatalf("Incr(short) again = %d, want = 2", got)
			}
			// A counter has no value header, so Get does not read it; the
			// server is asked directly.
			raw, err := valkey.NewClient(valkey.MustParseURL(b.url + "?client_cache=0"))
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			eventually(t, 5*time.Second, "the counter to expire", func() bool {
				n, err := raw.Do(ctx, raw.B().Exists().Key(prefix+"short").Build()).AsInt64()
				return err == nil && n == 0
			})
			if got, _ := one.Incr(ctx, "short", time.Hour); got != 1 {
				t.Errorf("Incr(short) after expiry = %d, want = 1", got)
			}
			one.Delete(ctx, "n", "short")
		})
	}
}

func TestValkeyClientSideCache(t *testing.T) {
	ctx := context.Background()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			prefix := uniquePrefix()
			writer := newValkey(t, b.url, cache.WithPrefix(prefix))
			reader := newValkey(t, b.url, cache.WithPrefix(prefix), cache.WithClientSideTTL(time.Minute))
			writer.Set(ctx, "k", []byte("v1"), time.Hour)
			for range 3 { // the later reads come from the client-side cache
				got, ok := reader.Get(ctx, "k")
				if !ok || string(got) != "v1" {
					t.Fatalf("Get(k) = %q, %v, want = %q, true", got, ok, "v1")
				}
				copy(got, "XX") // must not reach the client-side cache
			}
			writer.Set(ctx, "k", []byte("v2"), time.Hour)
			eventually(t, 5*time.Second, "the reader to see v2", func() bool {
				got, ok := reader.Get(ctx, "k")
				return ok && string(got) == "v2"
			})
			writer.Delete(ctx, "k")
			eventually(t, 5*time.Second, "the reader to miss", func() bool {
				_, ok := reader.Get(ctx, "k")
				return !ok
			})
		})
	}
}

func TestValkeyClientSideCacheRESP2(t *testing.T) {
	ctx := context.Background()
	b := backends(t)[0]
	// A server that speaks only RESP2 to the client cannot track keys: the
	// cache turns client-side caching off and keeps working.
	v := newValkey(t, b.url+"?protocol=2", cache.WithClientSideTTL(time.Minute))
	v.Set(ctx, "k", []byte("v"), time.Hour)
	if got, ok := v.Get(ctx, "k"); !ok || string(got) != "v" {
		t.Errorf("Get(k) = %q, %v, want = %q, true", got, ok, "v")
	}
}

func TestValkeyErrorsAreMisses(t *testing.T) {
	ctx := context.Background()
	b := backends(t)[0]
	prefix := uniquePrefix()
	logs := &syncBuffer{}
	reader := sdkmetric.NewManualReader()
	v := newValkey(t, b.url, cache.WithPrefix(prefix),
		cache.WithLogger(slog.New(slog.NewTextHandler(logs, nil))),
		cache.WithMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))))

	raw, err := valkey.NewClient(valkey.MustParseURL(b.url + "?client_cache=0"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := raw.Do(ctx, raw.B().Rpush().Key(prefix+"list").Element("x").Build()).Error(); err != nil {
		t.Fatal(err)
	}
	defer raw.Do(ctx, raw.B().Del().Key(prefix+"list").Build())

	// GET of a list fails with WRONGTYPE: a miss, logged once and counted.
	for range 3 {
		if got, ok := v.Get(ctx, "list"); ok {
			t.Fatalf("Get(list) = %q, true, want = a miss", got)
		}
	}
	if got := strings.Count(logs.String(), "cache: get failed"); got != 1 {
		t.Errorf("warnings = %d, want = 1 (rate limited); logs:\n%s", got, logs)
	}
	if got := counter(t, reader, "tanvo.cache.errors", "get"); got != 3 {
		t.Errorf("tanvo.cache.errors{get} = %d, want = 3", got)
	}
}

func TestValkeyBadURL(t *testing.T) {
	tests := []string{
		"http://localhost:6379",
		"redis://user:s3cret@localhost:6379/notadb",
		"redis://localhost:6379/1/2",
		"redis://localhost?dial_timeout=soon",
		"redis://user:s3cret@[::1",
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			v, err := cache.NewValkey(context.Background(), raw)
			if err == nil {
				v.Close()
				t.Fatalf("NewValkey(%q) error = nil, want = an error", raw)
			}
			if strings.Contains(err.Error(), "s3cret") {
				t.Errorf("NewValkey(%q) error = %q, want = no password", raw, err)
			}
		})
	}
}

func TestValkeyUnreachable(t *testing.T) {
	ctx := context.Background()
	port, err := dockertest.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	reader := sdkmetric.NewManualReader()
	start := time.Now()
	v, err := cache.NewValkey(ctx, "redis://127.0.0.1:"+port,
		cache.WithLogger(slog.New(slog.NewTextHandler(logs, nil))),
		cache.WithMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))),
		cache.WithBackoff(10*time.Millisecond, 20*time.Millisecond))
	if err != nil {
		t.Fatalf("NewValkey() = %v, want = nil", err)
	}
	if v.Connected() {
		t.Fatal("Connected() = true, want = false")
	}
	v.Set(ctx, "k", []byte("v"), time.Hour)
	v.Delete(ctx, "k")
	if _, ok := v.Get(ctx, "k"); ok {
		t.Error("Get(k) ok = true, want = false")
	}
	if _, err := v.Incr(ctx, "n", time.Hour); !errors.Is(err, cache.ErrNotConnected) {
		t.Errorf("Incr() = %v, want = ErrNotConnected", err)
	}
	if err := v.Ping(ctx); !errors.Is(err, cache.ErrNotConnected) {
		t.Errorf("Ping() = %v, want = ErrNotConnected", err)
	}
	if _, _, err := v.AcquireSlot(ctx, "s", 1); !errors.Is(err, cache.ErrNotConnected) {
		t.Errorf("AcquireSlot() error got = %v, want = %v", err, cache.ErrNotConnected)
	}
	eventually(t, 5*time.Second, "several connection attempts", func() bool {
		return counter(t, reader, "tanvo.cache.errors", "connect") >= 3
	})
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "cache: connect failed") {
		t.Errorf("logs = %q, want = a connect warning", logs)
	}
	v.Close()
	v.Close()
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("unreachable server took %v, want = well under 5s", d)
	}
}

func TestValkeySentinelAuth(t *testing.T) {
	port, err := dockertest.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	for _, tc := range []struct {
		name           string
		opts           []cache.ValkeyOption
		user, password string
	}{
		{"none", nil, "", ""},
		{"own credentials", []cache.ValkeyOption{cache.WithSentinelAuth("sentinel", "watch")}, "sentinel", "watch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The server's credentials stay the server's: sentinels get
			// only their own.
			v, err := cache.NewValkey(ctx, "redis://app:secret@127.0.0.1:"+port+"?master_set=main", tc.opts...)
			if err != nil {
				t.Fatalf("NewValkey() = %v, want = nil", err)
			}
			defer v.Close()
			if u, p := cache.SentinelAuth(v); u != tc.user || p != tc.password {
				t.Errorf("sentinel auth got = %q %q, want = %q %q", u, p, tc.user, tc.password)
			}
		})
	}
}

func TestValkeyReconnects(t *testing.T) {
	if servers.engine == nil {
		t.Skip("needs a Docker engine: " + servers.skip)
	}
	if h := servers.engine.Host(); h != "127.0.0.1" {
		t.Skipf("needs a local engine, have %s", h)
	}
	ctx := context.Background()
	port, err := dockertest.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	v, err := cache.NewValkey(ctx, "redis://127.0.0.1:"+port, cache.WithPrefix(uniquePrefix()),
		cache.WithLogger(slog.New(slog.DiscardHandler)),
		cache.WithBackoff(50*time.Millisecond, 500*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if v.Connected() {
		t.Fatal("Connected() before the server starts = true, want = false")
	}
	c, err := startServer(ctx, valkeyImage, []string{"valkey-server", "--save", "", "--appendonly", "no"},
		map[string]string{"6379/tcp": port})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close(context.Background()) }()
	eventually(t, 30*time.Second, "the cache to connect", v.Connected)
	v.Set(ctx, "k", []byte("v"), time.Hour)
	if got, ok := v.Get(ctx, "k"); !ok || string(got) != "v" {
		t.Fatalf("Get(k) = %q, %v, want = %q, true", got, ok, "v")
	}

	// A server that goes away makes reads miss, within the timeout.
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, ok := v.Get(ctx, "k"); ok {
		t.Error("Get(k) after the server stopped ok = true, want = false")
	}
	if d := time.Since(start); d > 2*cache.DefaultTimeout {
		t.Errorf("Get after the server stopped took %v, want = at most %v", d, 2*cache.DefaultTimeout)
	}
}

// counter returns the value of an int64 counter for an operation.
func counter(t *testing.T, r sdkmetric.Reader, name, op string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				if v, _ := dp.Attributes.Value(cache.AttrOperation); v.AsString() == op {
					return dp.Value
				}
			}
		}
	}
	return 0
}

// eventually polls cond until it holds or the timeout passes.
func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent use.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
