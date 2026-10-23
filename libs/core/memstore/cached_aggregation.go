package memstore

import (
	"context"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
)

// cachedAggregationRepo implements event.CachedAggregationRepository.
type cachedAggregationRepo struct{ d *eventData }

var _ event.CachedAggregationRepository = cachedAggregationRepo{}

func (r cachedAggregationRepo) Create(_ context.Context, a *event.CachedAggregation) error {
	if r.d.cachedAggregations.has(a.ID) {
		return conflict("cached_aggregation", "id")
	}
	r.d.cachedAggregations.put(a.ID, *a)
	return nil
}

func (r cachedAggregationRepo) CreateIfAbsent(ctx context.Context, a *event.CachedAggregation) (bool, error) {
	sc := event.CachedAggregationScope{ExternalSubscriptionID: a.ExternalSubscriptionID, ChargeID: a.ChargeID, ChargeFilterID: a.ChargeFilterID}
	exists := len(r.d.cachedAggregations.find(func(x *event.CachedAggregation) bool {
		return inScope(x, a.OrganizationID, sc) && x.ChargeFilterID == sc.ChargeFilterID &&
			x.Timestamp.Equal(a.Timestamp) && event.EqualGroups(x.GroupedBy, a.GroupedBy)
	})) > 0
	if exists {
		return false, nil
	}
	return true, r.Create(ctx, a)
}

// inScope reports whether x belongs to the scope of a lookup
// (event.CachedAggregationScope): a zero charge filter matches every
// filter unless the scope is a default bucket.
func inScope(x *event.CachedAggregation, org core.OrganizationID, sc event.CachedAggregationScope) bool {
	return x.OrganizationID == org && x.ExternalSubscriptionID == sc.ExternalSubscriptionID &&
		x.ChargeID == sc.ChargeID &&
		(x.ChargeFilterID == sc.ChargeFilterID || (sc.ChargeFilterID.IsZero() && !sc.DefaultBucket))
}

// newestFirst returns the matching states by timestamp, then creation,
// most recent first.
func (r cachedAggregationRepo) newestFirst(pred func(*event.CachedAggregation) bool) []*event.CachedAggregation {
	out := r.d.cachedAggregations.find(pred)
	slices.Reverse(out)
	slices.SortStableFunc(out, func(a, b *event.CachedAggregation) int {
		if c := b.Timestamp.Compare(a.Timestamp); c != 0 {
			return c
		}
		return b.CreatedAt.Compare(a.CreatedAt)
	})
	return out
}

func (r cachedAggregationRepo) LatestInPeriod(_ context.Context, org core.OrganizationID, sc event.CachedAggregationScope, from, to time.Time, groups map[string]*string, exclude string) (*event.CachedAggregation, error) {
	from, to = from.Truncate(time.Second), to.Truncate(time.Second)
	out := r.newestFirst(func(x *event.CachedAggregation) bool {
		return inScope(x, org, sc) && !x.Timestamp.Before(from) && !x.Timestamp.After(to) &&
			event.EqualGroups(x.GroupedBy, groups) && (exclude == "" || (x.EventTransactionID != "" && x.EventTransactionID != exclude))
	})
	if len(out) == 0 {
		return nil, notFound("cached_aggregation", sc.ChargeID)
	}
	return out[0], nil
}

func (r cachedAggregationRepo) Before(_ context.Context, org core.OrganizationID, sc event.CachedAggregationScope, t time.Time) ([]*event.CachedAggregation, error) {
	return r.newestFirst(func(x *event.CachedAggregation) bool { return inScope(x, org, sc) && x.Timestamp.Before(t) }), nil
}
