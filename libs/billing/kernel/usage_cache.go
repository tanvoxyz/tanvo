package kernel

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/internal/cachecodec"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// UsageCache keeps the current-usage fees of charge buckets between
// computations (lago Subscriptions::ChargeCacheService). An entry is
// validated lazily by its reader: it holds the ingestion watermark of the
// events it aggregated and is used only while no event was ingested after
// it (metering.Watermark).
//
// Entries are encoded into a [port.Cache], in the process or on Redis or
// Valkey: with a shared backend every server replica reuses the fees any
// of them computed, the watermark keeping them exact. A nil *UsageCache
// caches nothing. It is safe for concurrent use; the fees are copied in
// and out.
type UsageCache struct {
	c       port.Cache
	version string
}

// UsageCacheEntry is one cached bucket: its fees and the watermark they
// were computed at.
type UsageCacheEntry struct {
	// Fees are the bucket's fees.
	Fees []*invoice.Fee
	// CachedAt is the ingestion watermark of the events behind Fees
	// (lago cached_at).
	CachedAt time.Time
	// ExpiresAt is when the entry stops being used: the end of the
	// charges period.
	ExpiresAt time.Time
}

// NewUsageCache returns the usage cache over c; nil when c is nil, which
// turns the cache off.
func NewUsageCache(c port.Cache) *UsageCache {
	if c == nil {
		return nil
	}
	return &UsageCache{c: c, version: cachecodec.Fingerprint[UsageCacheEntry]()}
}

// Version is the shape of the entries (cachecodec.Fingerprint), which
// their keys cover, so that server processes of another version sharing
// the backend never read them; "" for a nil cache.
func (u *UsageCache) Version() string {
	if u == nil {
		return ""
	}
	return u.version
}

// Get returns the entry of key, false when there is none, it cannot be
// decoded or it expired at now.
func (u *UsageCache) Get(ctx context.Context, key string, now time.Time) (UsageCacheEntry, bool) {
	if u == nil {
		return UsageCacheEntry{}, false
	}
	b, ok := u.c.Get(ctx, key)
	if !ok {
		return UsageCacheEntry{}, false
	}
	var e UsageCacheEntry
	if err := cachecodec.Unmarshal(b, &e); err != nil || !now.Before(e.ExpiresAt) {
		// An entry written by another version or past its end is a miss.
		u.c.Delete(ctx, key)
		return UsageCacheEntry{}, false
	}
	return e, true
}

// Put stores the entry of key, replacing any, until e.ExpiresAt; nothing
// is stored when that is not after now.
func (u *UsageCache) Put(ctx context.Context, key string, e UsageCacheEntry, now time.Time) {
	if u == nil {
		return
	}
	ttl := e.ExpiresAt.Sub(now)
	if ttl <= 0 {
		return
	}
	b, err := cachecodec.Marshal(e)
	if err != nil {
		// Fees always encode; were one not to, the bucket is simply
		// recomputed next time.
		return
	}
	u.c.Set(ctx, key, b, ttl)
}
