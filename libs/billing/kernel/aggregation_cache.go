package kernel

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// AggregationCache returns, for each scope (event.CachedAggregationScope),
// its stored cached aggregations (domain §10.3), read through r.
func AggregationCache(r port.Repos, org core.OrganizationID) func(event.CachedAggregationScope) metering.CachedAggregations {
	return func(sc event.CachedAggregationScope) metering.CachedAggregations {
		return storedCache{repo: r.CachedAggregations, org: org, scope: sc}
	}
}

// storedCache is a [metering.CachedAggregations] over the repository.
type storedCache struct {
	repo  event.CachedAggregationRepository
	org   core.OrganizationID
	scope event.CachedAggregationScope
}

// LatestInPeriod implements [metering.CachedAggregations].
func (c storedCache) LatestInPeriod(ctx context.Context, from, to time.Time, groups metering.Groups, exclude string) (*metering.CachedAggregation, error) {
	a, err := c.repo.LatestInPeriod(ctx, c.org, c.scope, from, to, groups, exclude)
	if errors.Is(err, core.ErrNotFound) {
		return nil, nil //nolint:nilnil // no cached aggregation is not an error
	}
	if err != nil {
		return nil, fmt.Errorf("load cached aggregation of charge %s: %w", c.scope.ChargeID, err)
	}
	m := MeteringCachedAggregation(a)
	return &m, nil
}

// Before implements [metering.CachedAggregations].
func (c storedCache) Before(ctx context.Context, t time.Time) ([]metering.CachedAggregation, error) {
	as, err := c.repo.Before(ctx, c.org, c.scope, t)
	if err != nil {
		return nil, fmt.Errorf("load cached aggregations of charge %s: %w", c.scope.ChargeID, err)
	}
	out := make([]metering.CachedAggregation, len(as))
	for i, a := range as {
		out[i] = MeteringCachedAggregation(a)
	}
	return out, nil
}

// MeteringCachedAggregation returns the metering view of a stored cached
// aggregation.
func MeteringCachedAggregation(a *event.CachedAggregation) metering.CachedAggregation {
	m := metering.CachedAggregation{
		Timestamp: a.Timestamp, TransactionID: a.EventTransactionID, Groups: metering.Groups(event.CloneGroups(a.GroupedBy)),
		CurrentAggregation: cloneDecimal(a.CurrentAggregation), MaxAggregation: cloneDecimal(a.MaxAggregation),
		MaxAggregationWithProration: cloneDecimal(a.MaxAggregationWithProration),
	}
	for _, v := range a.PresentationBreakdowns {
		m.PresentationBreakdowns = append(m.PresentationBreakdowns, metering.GroupedValue{Groups: metering.Groups(event.CloneGroups(v.Groups)), Value: v.Value})
	}
	return m
}

// StoredCachedAggregation returns the stored form of a metering cached
// aggregation of a charge (zero filter for its default bucket) of the
// external subscription.
func StoredCachedAggregation(d *Deps, org core.OrganizationID, externalSubscriptionID string, c core.ChargeID, f core.ChargeFilterID, m metering.CachedAggregation) *event.CachedAggregation {
	a := &event.CachedAggregation{
		ID: core.NewID[core.CachedAggregationID](d.IDs), OrganizationID: org,
		ExternalSubscriptionID: externalSubscriptionID, ChargeID: c, ChargeFilterID: f,
		Timestamp: m.Timestamp.UTC(), EventTransactionID: m.TransactionID,
		GroupedBy:          event.CloneGroups(m.Groups),
		CurrentAggregation: cloneDecimal(m.CurrentAggregation), MaxAggregation: cloneDecimal(m.MaxAggregation),
		MaxAggregationWithProration: cloneDecimal(m.MaxAggregationWithProration),
		CreatedAt:                   d.Now(),
	}
	if a.GroupedBy == nil {
		a.GroupedBy = map[string]*string{}
	}
	for _, v := range m.PresentationBreakdowns {
		a.PresentationBreakdowns = append(a.PresentationBreakdowns, event.GroupedValue{Groups: event.CloneGroups(v.Groups), Value: v.Value})
	}
	return a
}

// cloneDecimal returns a copy of *p, or nil when p is nil.
func cloneDecimal(p *money.Decimal) *money.Decimal {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
