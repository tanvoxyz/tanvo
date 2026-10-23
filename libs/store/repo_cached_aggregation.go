package store

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// cachedAggregationRepo implements event.CachedAggregationRepository.
type cachedAggregationRepo struct{ t *txState }

var _ event.CachedAggregationRepository = cachedAggregationRepo{}

// groupedValueJSON is an element of the stored presentation_breakdowns
// array of a cached aggregation.
type groupedValueJSON struct {
	Groups map[string]*string `json:"groups"`
	Value  money.Decimal      `json:"value"`
}

// groupsJSON encodes pricing groups as a JSON object (never null).
func groupsJSON(g map[string]*string) ([]byte, error) {
	if g == nil {
		g = map[string]*string{}
	}
	return encodeJSON(g)
}

func cachedAggregationParams(a *event.CachedAggregation) (db.CreateCachedAggregationParams, error) {
	groups, err := groupsJSON(a.GroupedBy)
	if err != nil {
		return db.CreateCachedAggregationParams{}, fmt.Errorf("cached aggregation %s: grouped by: %w", a.ID, err)
	}
	bs := make([]groupedValueJSON, len(a.PresentationBreakdowns))
	for i, b := range a.PresentationBreakdowns {
		bs[i] = groupedValueJSON{Groups: b.Groups, Value: b.Value}
		if bs[i].Groups == nil {
			bs[i].Groups = map[string]*string{}
		}
	}
	breakdowns, err := encodeJSON(bs)
	if err != nil {
		return db.CreateCachedAggregationParams{}, fmt.Errorf("cached aggregation %s: presentation breakdowns: %w", a.ID, err)
	}
	p := db.CreateCachedAggregationParams{
		OrganizationID:         a.OrganizationID.UUID(),
		ID:                     a.ID.UUID(),
		ExternalSubscriptionID: a.ExternalSubscriptionID,
		ChargeID:               a.ChargeID.UUID(),
		ChargeFilterID:         optUUID(a.ChargeFilterID),
		Timestamp:              utc(a.Timestamp),
		EventTransactionID:     optString(a.EventTransactionID),
		GroupedBy:              groups,
		PresentationBreakdowns: breakdowns,
		CreatedAt:              utc(a.CreatedAt),
	}
	p.CurrentAggregation = a.CurrentAggregation
	p.MaxAggregation = a.MaxAggregation
	p.MaxAggregationWithProration = a.MaxAggregationWithProration
	return p, nil
}

func cachedAggregationOf(r db.CachedAggregation) (*event.CachedAggregation, error) {
	var groups map[string]*string
	if err := decodeJSON(r.GroupedBy, &groups); err != nil {
		return nil, fmt.Errorf("cached aggregation %s: grouped by: %w", r.ID, err)
	}
	if len(groups) == 0 {
		groups = nil
	}
	var bs []groupedValueJSON
	if err := decodeJSON(r.PresentationBreakdowns, &bs); err != nil {
		return nil, fmt.Errorf("cached aggregation %s: presentation breakdowns: %w", r.ID, err)
	}
	a := &event.CachedAggregation{
		ID:                          idOf[core.CachedAggregationID](r.ID),
		OrganizationID:              idOf[core.OrganizationID](r.OrganizationID),
		ExternalSubscriptionID:      r.ExternalSubscriptionID,
		ChargeID:                    idOf[core.ChargeID](r.ChargeID),
		ChargeFilterID:              optIDOf[core.ChargeFilterID](r.ChargeFilterID),
		Timestamp:                   utc(r.Timestamp),
		EventTransactionID:          deref(r.EventTransactionID),
		GroupedBy:                   groups,
		CurrentAggregation:          r.CurrentAggregation,
		MaxAggregation:              r.MaxAggregation,
		MaxAggregationWithProration: r.MaxAggregationWithProration,
		CreatedAt:                   utc(r.CreatedAt),
	}
	if len(bs) > 0 {
		a.PresentationBreakdowns = make([]event.GroupedValue, len(bs))
		for i, b := range bs {
			a.PresentationBreakdowns[i] = event.GroupedValue{Groups: b.Groups, Value: b.Value}
		}
	}
	return a, nil
}

func (r cachedAggregationRepo) Create(ctx context.Context, a *event.CachedAggregation) error {
	q, err := r.t.queries(ctx, a.OrganizationID)
	if err != nil {
		return err
	}
	p, err := cachedAggregationParams(a)
	if err != nil {
		return err
	}
	n, err := q.CreateCachedAggregation(ctx, p)
	return insertErr("cached_aggregation", n, err)
}

func (r cachedAggregationRepo) CreateIfAbsent(ctx context.Context, a *event.CachedAggregation) (bool, error) {
	q, err := r.t.queries(ctx, a.OrganizationID)
	if err != nil {
		return false, err
	}
	p, err := cachedAggregationParams(a)
	if err != nil {
		return false, err
	}
	exists, err := q.CachedAggregationExists(ctx, db.CachedAggregationExistsParams{
		OrganizationID: p.OrganizationID, ExternalSubscriptionID: p.ExternalSubscriptionID, ChargeID: p.ChargeID,
		ChargeFilterID: p.ChargeFilterID, Timestamp: p.Timestamp, GroupedBy: p.GroupedBy,
	})
	if err != nil {
		return false, fmt.Errorf("cached aggregation exists: %w", mapErr(err))
	}
	if exists {
		return false, nil
	}
	n, err := q.CreateCachedAggregation(ctx, p)
	if err := insertErr("cached_aggregation", n, err); err != nil {
		return false, err
	}
	return true, nil
}

func (r cachedAggregationRepo) LatestInPeriod(ctx context.Context, org core.OrganizationID, sc event.CachedAggregationScope, from, to time.Time, groups map[string]*string, exclude string) (*event.CachedAggregation, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	g, err := groupsJSON(groups)
	if err != nil {
		return nil, fmt.Errorf("cached aggregation groups: %w", err)
	}
	row, err := q.LatestCachedAggregationInPeriod(ctx, db.LatestCachedAggregationInPeriodParams{
		OrganizationID:         org.UUID(),
		ExternalSubscriptionID: sc.ExternalSubscriptionID,
		ChargeID:               sc.ChargeID.UUID(),
		ChargeFilterID:         optUUID(sc.ChargeFilterID),
		DefaultBucket:          sc.DefaultBucket,
		FromDatetime:           utc(from.Truncate(time.Second)),
		ToDatetime:             utc(to.Truncate(time.Second)),
		GroupedBy:              g,
		ExcludeTransactionID:   exclude,
	})
	if err != nil {
		return nil, getErr("cached_aggregation", sc.ChargeID, err)
	}
	return cachedAggregationOf(row)
}

func (r cachedAggregationRepo) Before(ctx context.Context, org core.OrganizationID, sc event.CachedAggregationScope, t time.Time) ([]*event.CachedAggregation, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListCachedAggregationsBefore(ctx, db.ListCachedAggregationsBeforeParams{
		OrganizationID:         org.UUID(),
		ExternalSubscriptionID: sc.ExternalSubscriptionID,
		ChargeID:               sc.ChargeID.UUID(),
		ChargeFilterID:         optUUID(sc.ChargeFilterID),
		DefaultBucket:          sc.DefaultBucket,
		Before:                 utc(t),
	})
	if err != nil {
		return nil, fmt.Errorf("list cached aggregations: %w", mapErr(err))
	}
	return convAll(rows, cachedAggregationOf)
}
