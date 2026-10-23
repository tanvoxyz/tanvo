package ingest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// RepoCatalog is the [Catalog] over the repositories of libs/core: each
// lookup runs in its own transaction, or joins the one ctx carries.
type RepoCatalog struct {
	// Transactor gives the repositories.
	Transactor port.Transactor
}

var _ Catalog = RepoCatalog{}

// Metric implements [Catalog].
func (c RepoCatalog) Metric(ctx context.Context, org core.OrganizationID, code string) (*catalog.BillableMetric, error) {
	var m *catalog.BillableMetric
	err := c.Transactor.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		m, err = r.Metrics.GetByCode(ctx, org, code)
		if errors.Is(err, core.ErrNotFound) {
			m, err = nil, nil
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("ingest: metric %q: %w", code, err)
	}
	return m, nil
}

// subscriptionPage is the page size of [RepoCatalog.Subscriptions].
const subscriptionPage = 100

// Subscriptions implements [Catalog].
func (c RepoCatalog) Subscriptions(ctx context.Context, org core.OrganizationID, externalID string) ([]*subscription.Subscription, error) {
	var out []*subscription.Subscription
	err := c.Transactor.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		page := core.PageRequest{Size: subscriptionPage}
		for {
			p, err := r.Subscriptions.List(ctx, org, subscription.Filter{ExternalID: externalID}, page)
			if err != nil {
				return err
			}
			out = append(out, p.Items...)
			if p.NextToken == "" {
				return nil
			}
			page.Token = p.NextToken
		}
	})
	if err != nil {
		return nil, fmt.Errorf("ingest: subscriptions %q: %w", externalID, err)
	}
	return out, nil
}

// Charges implements [Catalog].
func (c RepoCatalog) Charges(ctx context.Context, org core.OrganizationID, plan core.PlanID, metric core.BillableMetricID) ([]*catalog.Charge, error) {
	var out []*catalog.Charge
	err := c.Transactor.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		charges, err := r.Charges.ListByPlan(ctx, org, plan)
		if err != nil {
			return err
		}
		for _, ch := range charges {
			if ch.BillableMetricID == metric {
				out = append(out, ch)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("ingest: charges of plan %s: %w", plan, err)
	}
	return out, nil
}

// ChangeKind is the kind of catalog object a [Change] is about.
type ChangeKind string

// Change kinds.
const (
	// ChangeMetric: a billable metric was created, changed or deleted; the
	// key is its code. The charges of the organization are refreshed too.
	ChangeMetric ChangeKind = "metric"
	// ChangeSubscription: a subscription was created or changed; the key
	// is its external ID.
	ChangeSubscription ChangeKind = "subscription"
	// ChangePlan: the charges of a plan changed; the key is the plan ID.
	ChangePlan ChangeKind = "plan"
	// ChangeOrganization: anything of the organization may have changed.
	ChangeOrganization ChangeKind = "organization"
)

// Change is a catalog change notification.
type Change struct {
	// OrganizationID is the organization of the change.
	OrganizationID core.OrganizationID `json:"organization_id"`
	// Kind is the kind of the changed object.
	Kind ChangeKind `json:"kind"`
	// Key identifies the object (see the kinds); empty means every object
	// of that kind in the organization.
	Key string `json:"key,omitempty"`
}

// DefaultCacheTTL is the lifetime of a [Cache] entry. Change notifications
// refresh entries sooner; the lifetime bounds the staleness after a lost
// notification.
const DefaultCacheTTL = 5 * time.Minute

// defaultCacheEntries bounds the entries of each table of a [Cache].
const defaultCacheEntries = 100_000

type metricKey struct {
	org  core.OrganizationID
	code string
}

type subscriptionKey struct {
	org        core.OrganizationID
	externalID string
}

type chargesKey struct {
	org    core.OrganizationID
	plan   core.PlanID
	metric core.BillableMetricID
}

type entry[V any] struct {
	value   V
	expires time.Time
}

// table is a bounded map of entries that expire.
type table[K comparable, V any] struct {
	m map[K]entry[V]
}

func (t *table[K, V]) get(k K, now time.Time) (V, bool) {
	e, ok := t.m[k]
	if !ok || !now.Before(e.expires) {
		var zero V
		return zero, false
	}
	return e.value, true
}

func (t *table[K, V]) put(k K, v V, expires time.Time, limit int) {
	if t.m == nil {
		t.m = map[K]entry[V]{}
	}
	if len(t.m) >= limit {
		for k, e := range t.m {
			if !expires.Before(e.expires) {
				delete(t.m, k)
			}
		}
		if len(t.m) >= limit {
			clear(t.m)
		}
	}
	t.m[k] = entry[V]{v, expires}
}

func (t *table[K, V]) deleteFunc(match func(K) bool) {
	for k := range t.m {
		if match(k) {
			delete(t.m, k)
		}
	}
}

// Cache is a [Catalog] keeping the lookups of another in memory, as the
// ingest consumers do (architecture §7.2). Missing metrics are cached too.
// Entries expire after a lifetime and are dropped by [Cache.Invalidate],
// which the change notifications of the catalog call. Returned values are
// shared: callers must not modify them. A Cache is safe for concurrent
// use.
type Cache struct {
	next    Catalog
	ttl     time.Duration
	limit   int
	now     func() time.Time
	mu      sync.Mutex
	gen     map[core.OrganizationID]uint64
	epoch   uint64
	metrics table[metricKey, *catalog.BillableMetric]
	subs    table[subscriptionKey, []*subscription.Subscription]
	charges table[chargesKey, []*catalog.Charge]
}

var _ Catalog = (*Cache)(nil)

// CacheOption configures a [Cache].
type CacheOption func(*Cache)

// WithCacheTTL sets the lifetime of entries ([DefaultCacheTTL] by default).
func WithCacheTTL(d time.Duration) CacheOption { return func(c *Cache) { c.ttl = d } }

// WithCacheClock sets the clock entries expire by.
func WithCacheClock(now func() time.Time) CacheOption { return func(c *Cache) { c.now = now } }

// WithCacheLimit bounds the entries of each table of the cache.
func WithCacheLimit(n int) CacheOption { return func(c *Cache) { c.limit = max(n, 1) } }

// NewCache returns a cache over next.
func NewCache(next Catalog, opts ...CacheOption) *Cache {
	c := &Cache{next: next, ttl: DefaultCacheTTL, limit: defaultCacheEntries, now: time.Now, gen: map[core.OrganizationID]uint64{}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// lookup reads a table under the lock, or loads the value and stores it
// unless the organization was invalidated meanwhile.
func lookup[K comparable, V any](ctx context.Context, c *Cache, org core.OrganizationID, t *table[K, V], k K, load func(context.Context) (V, error)) (V, error) {
	c.mu.Lock()
	now := c.now()
	if v, ok := t.get(k, now); ok {
		c.mu.Unlock()
		return v, nil
	}
	gen, epoch := c.gen[org], c.epoch
	c.mu.Unlock()
	v, err := load(ctx)
	if err != nil {
		return v, err
	}
	c.mu.Lock()
	if c.gen[org] == gen && c.epoch == epoch {
		t.put(k, v, now.Add(c.ttl), c.limit)
	}
	c.mu.Unlock()
	return v, nil
}

// Metric implements [Catalog].
func (c *Cache) Metric(ctx context.Context, org core.OrganizationID, code string) (*catalog.BillableMetric, error) {
	return lookup(ctx, c, org, &c.metrics, metricKey{org, code}, func(ctx context.Context) (*catalog.BillableMetric, error) {
		return c.next.Metric(ctx, org, code)
	})
}

// Subscriptions implements [Catalog].
func (c *Cache) Subscriptions(ctx context.Context, org core.OrganizationID, externalID string) ([]*subscription.Subscription, error) {
	return lookup(ctx, c, org, &c.subs, subscriptionKey{org, externalID}, func(ctx context.Context) ([]*subscription.Subscription, error) {
		return c.next.Subscriptions(ctx, org, externalID)
	})
}

// Charges implements [Catalog].
func (c *Cache) Charges(ctx context.Context, org core.OrganizationID, plan core.PlanID, metric core.BillableMetricID) ([]*catalog.Charge, error) {
	return lookup(ctx, c, org, &c.charges, chargesKey{org, plan, metric}, func(ctx context.Context) ([]*catalog.Charge, error) {
		return c.next.Charges(ctx, org, plan, metric)
	})
}

// Purge drops every entry, for when change notifications may have been
// missed (a lost notification connection). Loads running concurrently do
// not store their results.
func (c *Cache) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	clear(c.metrics.m)
	clear(c.subs.m)
	clear(c.charges.m)
}

// Invalidate drops the entries a change makes stale. Loads running
// concurrently for the organization do not store their results.
func (c *Cache) Invalidate(ch Change) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen[ch.OrganizationID]++
	org := ch.OrganizationID
	allCharges := func(k chargesKey) bool { return k.org == org }
	switch ch.Kind {
	case ChangeMetric:
		c.metrics.deleteFunc(func(k metricKey) bool { return k.org == org && (ch.Key == "" || k.code == ch.Key) })
		c.charges.deleteFunc(allCharges)
	case ChangeSubscription:
		c.subs.deleteFunc(func(k subscriptionKey) bool { return k.org == org && (ch.Key == "" || k.externalID == ch.Key) })
	case ChangePlan:
		c.charges.deleteFunc(func(k chargesKey) bool { return k.org == org && (ch.Key == "" || k.plan.String() == ch.Key) })
	default:
		c.metrics.deleteFunc(func(k metricKey) bool { return k.org == org })
		c.subs.deleteFunc(func(k subscriptionKey) bool { return k.org == org })
		c.charges.deleteFunc(allCharges)
	}
}
