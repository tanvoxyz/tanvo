package cache

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// scopeName is the instrumentation scope of the package's metrics.
const scopeName = "github.com/tanvoxyz/tanvo/libs/cache"

// Attribute keys of the package's metrics.
const (
	// AttrName is the namespace of a read ([Namespace]).
	AttrName = attribute.Key("tanvo.cache.name")
	// AttrResult is the result of a read: "hit" or "miss".
	AttrResult = attribute.Key("tanvo.cache.result")
	// AttrOperation is the operation that failed: "get", "decode" (a stored
	// value that does not decode), "set", "delete", "incr", "connect".
	AttrOperation = attribute.Key("tanvo.cache.operation")
)

// namespace is the cache [Namespace] returns.
type namespace struct {
	c      port.Cache
	prefix string
	reads  metric.Int64Counter
	hit    metric.AddOption
	miss   metric.AddOption
}

// NamespaceOption configures [Namespace].
type NamespaceOption interface {
	applyNamespace(*namespaceConfig)
}

// namespaceConfig collects the options of [Namespace].
type namespaceConfig struct {
	mp metric.MeterProvider
}

// MeterProviderOption is the option [WithMeterProvider] returns; it is
// both a [NamespaceOption] and a [ValkeyOption].
type MeterProviderOption struct {
	mp metric.MeterProvider
}

// WithMeterProvider sets the provider of the package's metrics, a no-op by
// default: the tanvo.cache.reads counter of [Namespace]; the
// tanvo.cache.errors counter of [Valkey] and the client metrics of
// valkeyotel (command duration, client-side cache hits, dials).
func WithMeterProvider(mp metric.MeterProvider) MeterProviderOption {
	return MeterProviderOption{mp: mp}
}

func (o MeterProviderOption) applyNamespace(c *namespaceConfig) { c.mp = o.mp }

func (o MeterProviderOption) applyValkey(c *valkeyConfig) { c.mp = o.mp }

// Namespace returns a view of c whose keys are prefixed with name and ":"
// (no prefix when name is empty), so that the users of one cache do not
// collide. It counts its reads in the tanvo.cache.reads counter with the
// attributes tanvo.cache.name (name) and tanvo.cache.result ("hit" or
// "miss"); name must therefore be a fixed, low-cardinality string, never
// an organization ID.
//
// A nil c, a cache that is off, returns nil.
func Namespace(c port.Cache, name string, opts ...NamespaceOption) port.Cache {
	if c == nil {
		return nil
	}
	cfg := namespaceConfig{}
	for _, opt := range opts {
		opt.applyNamespace(&cfg)
	}
	if cfg.mp == nil {
		cfg.mp = noop.NewMeterProvider()
	}
	reads, err := cfg.mp.Meter(scopeName).Int64Counter("tanvo.cache.reads",
		metric.WithDescription("Reads of a cache namespace, by result."),
		metric.WithUnit("{read}"))
	if err != nil {
		// A meter returns a usable instrument along with its error.
		reads, _ = noop.NewMeterProvider().Meter(scopeName).Int64Counter("tanvo.cache.reads")
	}
	prefix := ""
	if name != "" {
		prefix = name + ":"
	}
	return &namespace{
		c:      c,
		prefix: prefix,
		reads:  reads,
		hit:    metric.WithAttributeSet(attribute.NewSet(AttrName.String(name), AttrResult.String("hit"))),
		miss:   metric.WithAttributeSet(attribute.NewSet(AttrName.String(name), AttrResult.String("miss"))),
	}
}

// Get returns the value of the prefixed key and counts the read.
func (n *namespace) Get(ctx context.Context, key string) ([]byte, bool) {
	v, ok := n.c.Get(ctx, n.prefix+key)
	if ok {
		n.reads.Add(ctx, 1, n.hit)
	} else {
		n.reads.Add(ctx, 1, n.miss)
	}
	return v, ok
}

// Set stores value under the prefixed key.
func (n *namespace) Set(ctx context.Context, key string, value []byte, ttl time.Duration) {
	n.c.Set(ctx, n.prefix+key, value, ttl)
}

// Delete removes the prefixed keys.
func (n *namespace) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	if n.prefix == "" {
		return n.c.Delete(ctx, keys...)
	}
	prefixed := make([]string, len(keys))
	for i, k := range keys {
		prefixed[i] = n.prefix + k
	}
	return n.c.Delete(ctx, prefixed...)
}
