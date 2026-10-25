package fees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// usageCacheSettleWindow is how long after its ingestion an event may still
// be committing: an aggregation whose latest event is that fresh is not
// cached, since a sibling ingested at the same instant may not have been
// visible to it (lago CacheService::SETTLE_WINDOW).
//
// source: app/services/cache_service.rb:5, :73-84
const usageCacheSettleWindow = time.Second

// cachedBucketFees returns the current-usage fees of the bucket bk from
// opts.UsageCache, computing and caching them when the entry is missing or
// older than the latest ingested event the bucket reads (lago
// Subscriptions::ChargeCacheMiddleware with lazy validation). The cached
// fees do not depend on opts.SkipZeroUnitsFilters, which is applied to
// what is returned.
//
// source: app/services/subscriptions/charge_cache_middleware.rb:16-53, app/services/cache_service.rb:21-38, :86-104
func (s *Service) cachedBucketFees(ctx context.Context, sc Scope, cc *chargeContext, bk bucket, req metering.Request, opts Options) ([]*invoice.Fee, error) {
	compute := func() ([]*invoice.Fee, error) {
		o := opts
		o.SkipZeroUnitsFilters = false
		return s.computeBucketFees(ctx, sc, cc, bk, req, adjustments{}, o, &ChargeResult{})
	}
	watermark, ok, err := metering.Watermark(ctx, s.d.Events, req)
	if err != nil {
		return nil, fmt.Errorf("charge %s: %w", cc.charge.ID, err)
	}
	key, keyed := usageCacheKey(sc, cc, bk, opts)
	if !ok || !keyed {
		// The usage buckets answer, fresh already (charge_service.rb:140),
		// or nothing validates an entry.
		fs, err := compute()
		return skipZeroUnits(bk, opts, fs), err
	}
	// The instant is read before the aggregation, so that a slow one
	// cannot make an unsettled event look settled (cache_service.rb:28-31).
	now := s.d.Now()
	if e, hit := opts.UsageCache.Get(key, now); hit && usageCacheValid(e, watermark) {
		for _, f := range e.Fees {
			f.ID = core.NewID[core.FeeID](s.d.IDs)
			f.CreatedAt, f.UpdatedAt = now, now
		}
		return skipZeroUnits(bk, opts, e.Fees), nil
	}
	fs, err := compute()
	if err != nil {
		return nil, err
	}
	// The entry lives until the end of the charges period.
	stamp, settled := usageCacheStamp(watermark, now)
	if expires := sc.Boundaries.Charges.To; expires.After(now) && settled {
		opts.UsageCache.Put(key, kernel.UsageCacheEntry{Fees: fs, CachedAt: stamp, ExpiresAt: expires})
	}
	return skipZeroUnits(bk, opts, fs), nil
}

// usageCacheValid reports whether the entry e still holds every event
// below watermark: none was ingested after the one it aggregated. A zero
// watermark, no event read, keeps any entry (cache_service.rb:97-104).
func usageCacheValid(e kernel.UsageCacheEntry, watermark time.Time) bool {
	return !e.CachedAt.Before(watermark)
}

// usageCacheStamp returns the stamp of an entry computed from the events
// up to watermark, the aggregation having started at now, and whether the
// entry may be kept. The stamp is the watermark, so that any event
// ingested after it invalidates the entry; without any event, the settle
// bound before now, so that the first event to come invalidates it
// (cache_service.rb:63-71). An aggregation whose latest event is within
// the settle window of now is not kept (cache_service.rb:73-84).
func usageCacheStamp(watermark, now time.Time) (stamp time.Time, settled bool) {
	bound := now.Add(-usageCacheSettleWindow)
	if watermark.IsZero() {
		return bound, true
	}
	return watermark, watermark.Before(bound)
}

// skipZeroUnits drops the fees of a charge filter without units when
// opts.SkipZeroUnitsFilters is set (charge_service.rb:208).
func skipZeroUnits(bk bucket, opts Options, fs []*invoice.Fee) []*invoice.Fee {
	if !opts.SkipZeroUnitsFilters || bk.filter == nil {
		return fs
	}
	kept := fs[:0:0]
	for _, f := range fs {
		if !f.Units.IsZero() {
			kept = append(kept, f)
		}
	}
	return kept
}

// usageCacheKey is the cache key of a bucket: the charge as last changed,
// the subscription, the bucket's filter and a fingerprint of what selects
// and prices its events, and the full-usage segment (lago
// ChargeCacheService#cache_key). Skipped grouping and a presentation filter
// have their own segments, so that the fees they shape are never served to
// another usage (§4). Tanvo's charge filters carry no update
// instant, so the fingerprint stands for lago's filter updated_at; it also
// covers the default bucket, whose events depend on the other filters.
//
// It is false when the bucket cannot be fingerprinted.
//
// source: app/services/subscriptions/charge_cache_service.rb:55-67
func usageCacheKey(sc Scope, cc *chargeContext, bk bucket, opts Options) (string, bool) {
	var name *string
	if bk.filter != nil {
		name = bk.filter.InvoiceDisplayName
	}
	fp, err := json.Marshal(struct {
		Scope metering.Filter `json:"scope"`
		Props any             `json:"props"`
		Name  *string         `json:"name"`
	}{bk.scope, bk.props, name})
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(fp)
	parts := []string{
		"charge-usage", cc.charge.ID.String(), sc.Env.Subscription.ID.String(),
		cc.charge.UpdatedAt.UTC().Format(time.RFC3339Nano), bk.filterID().String(), hex.EncodeToString(sum[:16]),
	}
	if opts.FullUsage {
		parts = append(parts, "full-usage")
	}
	if opts.UsageFilters.SkipGrouping {
		parts = append(parts, "skip-grouping")
	}
	if p := opts.UsageFilters.Presentation; p != nil {
		keys := slices.Clone(p)
		slices.Sort(keys)
		// The keys are JSON-encoded, so that no key can mimic a segment.
		enc, err := json.Marshal(slices.Compact(keys))
		if err != nil {
			return "", false
		}
		parts = append(parts, "presentation:"+string(enc))
	}
	return strings.Join(parts, "/"), true
}
