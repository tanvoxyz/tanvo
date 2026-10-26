package analytics

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/internal/cachecodec"
	"github.com/tanvoxyz/tanvo/libs/billing/internal/flight"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/analytics"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// DefaultCacheTTL is how long a computed report is reused (lago
// Analytics::Base.cache_expiration).
const DefaultCacheTTL = 4 * time.Hour

// versionTTL is how long a customer's version token is kept (lago
// VERSION_CACHE_EXPIRATION).
const versionTTL = 24 * time.Hour

// Cache keeps computed reports for a while, as lago's analytics models
// do in the Rails cache (app/models/analytics/base.rb): a report is
// reused for the same organization, report kind, UTC day and filter
// until it is TTL old. A customer's reports of one kind carry a version
// token, kept in the cache itself, that an expiring read replaces,
// recomputing all of them; organization-wide reports are not versioned.
//
// Reports and tokens are encoded into a [port.Cache], in the process or
// on Redis or Valkey: with a shared backend every server replica reuses
// the reports any of them computed, and an expiring read on one replica
// is seen by all. It is safe for concurrent use.
//
// Concurrent misses of one process on the same report compute it once,
// every caller decoding rows of its own from the shared encoding.
type Cache struct {
	c      port.Cache
	ttl    time.Duration
	flight flight.Group[[]byte]
}

// cacheEntry is the encoding of a kept report.
type cacheEntry[T any] struct {
	Rows []T `json:"rows"`
	// Expires is the end of the report's TTL on the service clock, which
	// the backend's own expiry does not follow.
	Expires time.Time `json:"expires"`
}

// NewCache returns a cache keeping reports in c for ttl; a nil c or a ttl
// of zero or less turns it off.
func NewCache(c port.Cache, ttl time.Duration) *Cache {
	return &Cache{c: c, ttl: ttl}
}

// WithCache makes s reuse the reports kept by c; nil computes every
// report.
func (s *Service) WithCache(c *Cache) *Service {
	s.cache = c
	return s
}

func (c *Cache) on() bool { return c != nil && c.c != nil && c.ttl > 0 }

// key is the cache key of the report kind for org and scope (lago
// versioned_cache_key: the cache_key of each model, followed by the
// customer's version).
func (c *Cache) key(ctx context.Context, kind string, org core.OrganizationID, sc analytics.Scope) string {
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
	return k + "/" + strconv.FormatInt(c.version(ctx, kind, org, sc.CustomerID, sc.Now), 10)
}

func versionKey(kind string, org core.OrganizationID, id core.CustomerID) string {
	return kind + "/cache-version/" + org.String() + "/" + id.String()
}

// version returns the customer's version token of kind, seeding it with
// the current instant when absent (lago cache_version). Seeding from the
// clock rather than from zero keeps a lost token safe: the new one is
// greater than any a kept report was written with, so a lost token only
// ever makes a miss, never a stale hit.
func (c *Cache) version(ctx context.Context, kind string, org core.OrganizationID, id core.CustomerID, now time.Time) int64 {
	k := versionKey(kind, org, id)
	if v, ok := c.token(ctx, k); ok {
		return v
	}
	v := now.UnixNano()
	c.c.Set(ctx, k, []byte(strconv.FormatInt(v, 10)), versionTTL)
	return v
}

// expire replaces the customer's version token of kind, which every
// replica reads, so that their reports of kind are recomputed (lago
// expire_cache_for_customer). Lago's token is the current second, so two
// expiring reads within one second share it and the second one may be
// served what the first one cached; the token here is the current
// nanosecond and always above the previous token, so each expiring read
// recomputes even on a clock that has not moved.
func (c *Cache) expire(ctx context.Context, kind string, org core.OrganizationID, id core.CustomerID, now time.Time) {
	k := versionKey(kind, org, id)
	v := now.UnixNano()
	if prev, ok := c.token(ctx, k); ok && v <= prev {
		v = prev + 1
	}
	c.c.Set(ctx, k, []byte(strconv.FormatInt(v, 10)), versionTTL)
}

func (c *Cache) token(ctx context.Context, key string) (int64, bool) {
	b, ok := c.c.Get(ctx, key)
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseInt(string(b), 10, 64)
	return v, err == nil
}

// cached returns the rows of the report kind for scope from c, else
// computes them with compute and keeps them. With expire and a customer,
// the customer's reports of kind are recomputed first.
func cached[T any](ctx context.Context, c *Cache, kind string, org core.OrganizationID, sc analytics.Scope, expire bool, compute func() ([]T, error)) ([]T, error) {
	if !c.on() {
		return compute()
	}
	if expire && !sc.CustomerID.IsZero() {
		c.expire(ctx, kind, org, sc.CustomerID, sc.Now)
	}
	// The shape of the rows prefixes the key (cachecodec.Fingerprint).
	key := cachecodec.Fingerprint[cacheEntry[T]]() + "/" + c.key(ctx, kind, org, sc)
	if b, ok := c.c.Get(ctx, key); ok {
		var e cacheEntry[T]
		if err := cachecodec.Unmarshal(b, &e); err == nil && sc.Now.Before(e.Expires) {
			return e.Rows, nil
		}
		// A report past its TTL, or written by another version, is a miss.
		_ = c.c.Delete(ctx, key) // its failure is the cache's to report
	}
	var rows []T
	enc, leader, err := c.flight.Do(ctx, key, func(context.Context) ([]byte, error) {
		var err error
		if rows, err = compute(); err != nil {
			return nil, err
		}
		b, err := cachecodec.Marshal(cacheEntry[T]{Rows: rows, Expires: sc.Now.Add(c.ttl)})
		if err != nil {
			// Rows always encode; were they not to, the report is
			// recomputed next time, and each caller sharing it computes
			// its own.
			return nil, nil
		}
		c.c.Set(ctx, key, b, c.ttl)
		return b, nil
	})
	switch {
	case err != nil:
		return nil, err
	case leader:
		return rows, nil
	}
	// Another request computed the report: this one gets rows of its own.
	var e cacheEntry[T]
	if enc == nil || cachecodec.Unmarshal(enc, &e) != nil {
		return compute()
	}
	return e.Rows, nil
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
