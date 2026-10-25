package analytics

import (
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/analytics"
)

// DefaultCacheTTL is how long a computed report is reused (lago
// Analytics::Base.cache_expiration).
const DefaultCacheTTL = 4 * time.Hour

// defaultCacheEntries bounds the reports a [Cache] keeps.
const defaultCacheEntries = 10_000

// Cache keeps computed reports for a while, as lago's analytics models
// do in the Rails cache (app/models/analytics/base.rb): a report is
// reused for the same organization, report kind, UTC day and filter
// until it is TTL old. A customer's reports of one kind carry a version
// that an expiring read bumps, recomputing all of them; organization-wide
// reports are not versioned.
//
// Unlike lago's Rails cache, it lives in the process: each server keeps
// its own reports. It is safe for concurrent use.
type Cache struct {
	ttl time.Duration
	max int

	mu       sync.Mutex
	entries  map[string]cacheEntry
	versions map[string]uint64
	next     uint64
}

type cacheEntry struct {
	rows    any
	expires time.Time
}

// NewCache returns a cache keeping reports for ttl; a ttl of zero or less
// turns it off.
func NewCache(ttl time.Duration) *Cache {
	return &Cache{ttl: ttl, max: defaultCacheEntries, entries: map[string]cacheEntry{}, versions: map[string]uint64{}}
}

// WithCache makes s reuse the reports kept by c; nil computes every
// report.
func (s *Service) WithCache(c *Cache) *Service {
	s.cache = c
	return s
}

func (c *Cache) on() bool { return c != nil && c.ttl > 0 }

// key is the cache key of the report kind for org and scope (lago
// cache_key of each model, followed by the customer's version).
func (c *Cache) key(kind string, org core.OrganizationID, sc analytics.Scope) string {
	parts := []string{kind, sc.Now.UTC().Format(time.DateOnly), org.String(), idOrEmpty(sc.BillingEntityID), idOrEmpty(sc.CustomerID), sc.Currency.Code(), strconv.Itoa(sc.Months)}
	if sc.CustomerTINEmpty != nil {
		parts = append(parts, strconv.FormatBool(*sc.CustomerTINEmpty))
	} else {
		parts = append(parts, "")
	}
	k := strings.Join(parts, "/")
	if sc.CustomerID.IsZero() {
		return k
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return k + "/" + strconv.FormatUint(c.versions[c.versionKey(kind, org, sc.CustomerID)], 10)
}

func (c *Cache) versionKey(kind string, org core.OrganizationID, id core.CustomerID) string {
	return kind + "/cache-version/" + org.String() + "/" + id.String()
}

// expire bumps the version of the customer's reports of kind (lago
// expire_cache_for_customer).
func (c *Cache) expire(kind string, org core.OrganizationID, id core.CustomerID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	c.versions[c.versionKey(kind, org, id)] = c.next
}

// get returns the rows kept under key.
func (c *Cache) get(key string, now time.Time) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if ok && !now.Before(e.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return e.rows, ok
}

// put keeps rows under key, dropping expired entries, then arbitrary ones,
// when the cache is full.
func (c *Cache) put(key string, rows any, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.max {
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
		}
		for k := range c.entries {
			if len(c.entries) < c.max {
				break
			}
			delete(c.entries, k)
		}
	}
	c.entries[key] = cacheEntry{rows: rows, expires: now.Add(c.ttl)}
}

// cached returns the rows of the report kind for scope from c, else
// computes them with compute and keeps them. With expire and a customer,
// the customer's reports of kind are recomputed first.
func cached[T any](c *Cache, kind string, org core.OrganizationID, sc analytics.Scope, expire bool, compute func() ([]T, error)) ([]T, error) {
	if !c.on() {
		return compute()
	}
	if expire && !sc.CustomerID.IsZero() {
		c.expire(kind, org, sc.CustomerID)
	}
	key := c.key(kind, org, sc)
	if rows, ok := c.get(key, sc.Now); ok {
		return slices.Clone(rows.([]T)), nil
	}
	rows, err := compute()
	if err != nil {
		return nil, err
	}
	c.put(key, slices.Clone(rows), sc.Now)
	return rows, nil
}

func idOrEmpty[T interface {
	IsZero() bool
	String() string
}](id T) string {
	if id.IsZero() {
		return ""
	}
	return id.String()
}
