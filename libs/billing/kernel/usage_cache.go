package kernel

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/internal/cachecodec"
	"github.com/tanvoxyz/tanvo/libs/billing/internal/flight"
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
//
// Concurrent misses of one process on the same bucket compute it once
// ([UsageCache.Compute]), so that a burst of current-usage requests does
// not aggregate the same events many times over.
type UsageCache struct {
	c       port.Cache
	version string
	flight  flight.Group[usageFlight]
}

// usageFlight is the result of a computation shared by [UsageCache.Compute]:
// the entry, which only the caller that computed it receives, and its
// encoding, which every other caller decodes into fees of its own.
type usageFlight struct {
	entry UsageCacheEntry
	enc   []byte
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
		_ = u.c.Delete(ctx, key) // its failure is the cache's to report
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

// Compute returns the entry of key computed by compute, and stores it
// until its ExpiresAt when compute reports it may be kept. Concurrent
// callers of the process with the same key and watermark (the ingestion
// watermark each read, which makes them read the same events) share one
// computation: the caller that ran it receives the entry compute
// returned, the others a decoded copy, shared being true, whose fees they
// must give identifiers and timestamps of their own as on a hit. An error
// of compute is returned to every caller sharing it and is not stored.
// now is the instant the caller started at, which bounds the entry's TTL.
func (u *UsageCache) Compute(
	ctx context.Context, key string, watermark, now time.Time,
	compute func(context.Context) (e UsageCacheEntry, keep bool, err error),
) (e UsageCacheEntry, shared bool, err error) {
	if u == nil {
		e, _, err = compute(ctx)
		return e, false, err
	}
	fk := key + "@" + watermark.UTC().Format(time.RFC3339Nano)
	res, leader, err := u.flight.Do(ctx, fk, func(ctx context.Context) (usageFlight, error) {
		e, keep, err := compute(ctx)
		if err != nil {
			return usageFlight{}, err
		}
		// The encoding is made here, before any caller can change the
		// fees.
		b, encErr := cachecodec.Marshal(e)
		if encErr != nil {
			// Fees always encode; were one not to, the bucket is simply
			// recomputed next time, and each caller sharing it computes
			// its own.
			return usageFlight{entry: e}, nil
		}
		if ttl := e.ExpiresAt.Sub(now); keep && ttl > 0 {
			u.c.Set(ctx, key, b, ttl)
		}
		return usageFlight{entry: e, enc: b}, nil
	})
	switch {
	case err != nil:
		return UsageCacheEntry{}, false, err
	case leader:
		return res.entry, false, nil
	}
	var d UsageCacheEntry
	if res.enc == nil || cachecodec.Unmarshal(res.enc, &d) != nil {
		e, _, err = compute(ctx)
		return e, false, err
	}
	return d, true, nil
}
