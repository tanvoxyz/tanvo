package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/libs/cache"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/providers/apiclient"
)

// Names of the caches, the namespaces of their keys on the shared server
// and the tanvo.cache.name of their metrics.
const (
	cacheUsage     = "usage"
	cacheAnalytics = "analytics"
	cacheDraftTax  = "anrok-draft-taxes"
	cacheAPIKeys   = "api-keys"
	// counterRateLimits is the namespace of the provider rate limits.
	counterRateLimits = "rate-limits"
)

// memoryCacheEntries bounds the in-process caches whose size is not
// configured.
const memoryCacheEntries = 10_000

// caches is the backend of every cache of the installation (architecture
// §13.5): with TANVO_CACHE_URL one Redis or Valkey server, shared by every
// server process, else one in-process cache per use. Either way each use
// keeps its own switch (a TTL or a size of 0).
type caches struct {
	shared *cache.Valkey
	mp     metric.MeterProvider
}

// newCaches connects to the shared server when cfg names one. An
// unreachable server does not fail the start: the caches miss until it
// answers.
func newCaches(ctx context.Context, cfg config.Config, log *slog.Logger, tp trace.TracerProvider, mp metric.MeterProvider) (*caches, error) {
	c := &caches{mp: mp}
	if cfg.CacheURL == "" {
		return c, nil
	}
	v, err := cache.NewValkey(ctx, cfg.CacheURL,
		cache.WithPrefix(cfg.CachePrefix), cache.WithClientSideTTL(cfg.CacheClientSideTTL),
		cache.WithSentinelAuth(cfg.CacheSentinelUsername, cfg.CacheSentinelPassword),
		cache.WithLogger(log), cache.WithTracerProvider(tp), cache.WithMeterProvider(mp))
	if err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}
	c.shared = v
	return c, nil
}

// named returns the cache of the use name: its namespace on the shared
// server, else an in-process cache of at most entries; nil, which turns
// the use off, when entries is not positive.
func (c *caches) named(name string, entries int) port.Cache {
	if c == nil || entries <= 0 {
		return nil
	}
	var b port.Cache
	if c.shared != nil {
		b = c.shared
	} else {
		b = cache.NewMemory(entries)
	}
	return cache.Namespace(b, name, cache.WithMeterProvider(c.mp))
}

// sharedOnly returns the cache of the use name on the shared server; nil
// without one, for the uses whose invalidation must reach every process.
func (c *caches) sharedOnly(name string) port.Cache {
	if c == nil || c.shared == nil {
		return nil
	}
	return cache.Namespace(c.shared, name, cache.WithMeterProvider(c.mp))
}

// counter returns the shared counter of the use name; nil without a
// shared server, which leaves the count to each process.
func (c *caches) counter(name string) apiclient.Counter {
	if c == nil || c.shared == nil {
		return nil
	}
	return namedCounter{v: c.shared, prefix: name + ":"}
}

// namedCounter is the namespace of a counter on the shared server.
type namedCounter struct {
	v      *cache.Valkey
	prefix string
}

func (n namedCounter) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	return n.v.Incr(ctx, n.prefix+key, ttl)
}

// close disconnects from the shared server.
func (c *caches) close() {
	if c != nil && c.shared != nil {
		c.shared.Close()
	}
}
