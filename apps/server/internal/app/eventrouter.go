package app

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/subscriptions"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/eventstore"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// eventRouter is the event store of the billing engine: it sends each
// query to the store holding its organization's events ([EventStores.For]),
// PostgreSQL or ClickHouse by the organization's ingestion mode.
type eventRouter struct {
	stores *EventStores
}

var (
	_ metering.Store              = eventRouter{}
	_ metering.BucketReader       = eventRouter{}
	_ subscriptions.BucketDefiner = eventRouter{}
)

// of returns the store of the organization of q.
func (r eventRouter) of(q metering.Query) (metering.Store, error) {
	org, err := core.ParseID[core.OrganizationID](q.OrganizationID)
	if err != nil {
		return nil, fmt.Errorf("eventstore: query organization %q: %w", q.OrganizationID, err)
	}
	s := r.stores.For(org)
	if s == nil {
		return nil, fmt.Errorf("eventstore: organization %s ingests through Kafka without ClickHouse: %w", org, core.ErrInvalidValue)
	}
	return s, nil
}

// Count implements [metering.Store].
func (r eventRouter) Count(ctx context.Context, q metering.Query) (int64, error) {
	s, err := r.of(q)
	if err != nil {
		return 0, err
	}
	return s.Count(ctx, q)
}

// GroupedCount implements [metering.Store].
func (r eventRouter) GroupedCount(ctx context.Context, q metering.Query, by []string) ([]metering.GroupedAggregate, error) {
	s, err := r.of(q)
	if err != nil {
		return nil, err
	}
	return s.GroupedCount(ctx, q, by)
}

// Sum implements [metering.Store].
func (r eventRouter) Sum(ctx context.Context, q metering.Query) (metering.Total, error) {
	s, err := r.of(q)
	if err != nil {
		return metering.Total{}, err
	}
	return s.Sum(ctx, q)
}

// GroupedSum implements [metering.Store].
func (r eventRouter) GroupedSum(ctx context.Context, q metering.Query, by []string) ([]metering.GroupedAggregate, error) {
	s, err := r.of(q)
	if err != nil {
		return nil, err
	}
	return s.GroupedSum(ctx, q, by)
}

// Max implements [metering.Store].
func (r eventRouter) Max(ctx context.Context, q metering.Query) (metering.Total, error) {
	s, err := r.of(q)
	if err != nil {
		return metering.Total{}, err
	}
	return s.Max(ctx, q)
}

// GroupedMax implements [metering.Store].
func (r eventRouter) GroupedMax(ctx context.Context, q metering.Query, by []string) ([]metering.GroupedAggregate, error) {
	s, err := r.of(q)
	if err != nil {
		return nil, err
	}
	return s.GroupedMax(ctx, q, by)
}

// Last implements [metering.Store].
func (r eventRouter) Last(ctx context.Context, q metering.Query) (metering.LastValue, error) {
	s, err := r.of(q)
	if err != nil {
		return metering.LastValue{}, err
	}
	return s.Last(ctx, q)
}

// GroupedLast implements [metering.Store].
func (r eventRouter) GroupedLast(ctx context.Context, q metering.Query, partition []string, columns []string) ([]metering.GroupedAggregate, error) {
	s, err := r.of(q)
	if err != nil {
		return nil, err
	}
	return s.GroupedLast(ctx, q, partition, columns)
}

// LastEvent implements [metering.Store].
func (r eventRouter) LastEvent(ctx context.Context, q metering.Query) (*metering.Event, error) {
	s, err := r.of(q)
	if err != nil {
		return nil, err
	}
	return s.LastEvent(ctx, q)
}

// GroupedLastEvent implements [metering.Store].
func (r eventRouter) GroupedLastEvent(ctx context.Context, q metering.Query, by []string) ([]metering.GroupedEvent, error) {
	s, err := r.of(q)
	if err != nil {
		return nil, err
	}
	return s.GroupedLastEvent(ctx, q, by)
}

// SumPreciseTotalAmountCents implements [metering.Store].
func (r eventRouter) SumPreciseTotalAmountCents(ctx context.Context, q metering.Query) (money.Decimal, error) {
	s, err := r.of(q)
	if err != nil {
		return money.Decimal{}, err
	}
	return s.SumPreciseTotalAmountCents(ctx, q)
}

// GroupedSumPreciseTotalAmountCents implements [metering.Store].
func (r eventRouter) GroupedSumPreciseTotalAmountCents(ctx context.Context, q metering.Query, by []string) ([]metering.GroupedAggregate, error) {
	s, err := r.of(q)
	if err != nil {
		return nil, err
	}
	return s.GroupedSumPreciseTotalAmountCents(ctx, q, by)
}

// EventValues implements [metering.Store].
func (r eventRouter) EventValues(ctx context.Context, q metering.Query, limit int, exclude string) ([]money.Decimal, error) {
	s, err := r.of(q)
	if err != nil {
		return nil, err
	}
	return s.EventValues(ctx, q, limit, exclude)
}

// ProratedEventValues implements [metering.Store].
func (r eventRouter) ProratedEventValues(ctx context.Context, q metering.Query) ([]money.Decimal, error) {
	s, err := r.of(q)
	if err != nil {
		return nil, err
	}
	return s.ProratedEventValues(ctx, q)
}

// ProratedSum implements [metering.Store].
func (r eventRouter) ProratedSum(ctx context.Context, q metering.Query, persistedDays *int) (metering.ProratedAggregate, error) {
	s, err := r.of(q)
	if err != nil {
		return metering.ProratedAggregate{}, err
	}
	return s.ProratedSum(ctx, q, persistedDays)
}

// GroupedProratedSum implements [metering.Store].
func (r eventRouter) GroupedProratedSum(ctx context.Context, q metering.Query, by []string, persistedDays *int) ([]metering.GroupedProratedAggregate, error) {
	s, err := r.of(q)
	if err != nil {
		return nil, err
	}
	return s.GroupedProratedSum(ctx, q, by, persistedDays)
}

// SumByDay implements [metering.Store].
func (r eventRouter) SumByDay(ctx context.Context, q metering.Query) ([]metering.DailyValue, error) {
	s, err := r.of(q)
	if err != nil {
		return nil, err
	}
	return s.SumByDay(ctx, q)
}

// UniqueCount implements [metering.Store].
func (r eventRouter) UniqueCount(ctx context.Context, q metering.Query) (money.Decimal, error) {
	s, err := r.of(q)
	if err != nil {
		return money.Decimal{}, err
	}
	return s.UniqueCount(ctx, q)
}

// GroupedUniqueCount implements [metering.Store].
func (r eventRouter) GroupedUniqueCount(ctx context.Context, q metering.Query, by []string) ([]metering.GroupedAggregate, error) {
	s, err := r.of(q)
	if err != nil {
		return nil, err
	}
	return s.GroupedUniqueCount(ctx, q, by)
}

// ProratedUniqueCount implements [metering.Store].
func (r eventRouter) ProratedUniqueCount(ctx context.Context, q metering.Query) (money.Decimal, error) {
	s, err := r.of(q)
	if err != nil {
		return money.Decimal{}, err
	}
	return s.ProratedUniqueCount(ctx, q)
}

// GroupedProratedUniqueCount implements [metering.Store].
func (r eventRouter) GroupedProratedUniqueCount(ctx context.Context, q metering.Query, by []string) ([]metering.GroupedAggregate, error) {
	s, err := r.of(q)
	if err != nil {
		return nil, err
	}
	return s.GroupedProratedUniqueCount(ctx, q, by)
}

// UniqueCountIntervals implements [metering.Store].
func (r eventRouter) UniqueCountIntervals(ctx context.Context, q metering.Query, withRemove bool) ([]metering.UniqueCountInterval, error) {
	s, err := r.of(q)
	if err != nil {
		return nil, err
	}
	return s.UniqueCountIntervals(ctx, q, withRemove)
}

// ActiveUniqueProperty implements [metering.Store].
func (r eventRouter) ActiveUniqueProperty(ctx context.Context, q metering.Query, e metering.Event) (bool, error) {
	s, err := r.of(q)
	if err != nil {
		return false, err
	}
	return s.ActiveUniqueProperty(ctx, q, e)
}

// WeightedSum implements [metering.Store].
func (r eventRouter) WeightedSum(ctx context.Context, q metering.Query, initial money.Decimal) (metering.WeightedAggregate, error) {
	s, err := r.of(q)
	if err != nil {
		return metering.WeightedAggregate{}, err
	}
	return s.WeightedSum(ctx, q, initial)
}

// GroupedWeightedSum implements [metering.Store].
func (r eventRouter) GroupedWeightedSum(ctx context.Context, q metering.Query, by []string, initial []metering.GroupedValue) ([]metering.GroupedWeightedAggregate, error) {
	s, err := r.of(q)
	if err != nil {
		return nil, err
	}
	return s.GroupedWeightedSum(ctx, q, by, initial)
}

// LastIngestedAt implements [metering.IngestionWatermarks]; a store that
// reports none fails with [metering.ErrNotImplemented].
func (r eventRouter) LastIngestedAt(ctx context.Context, q metering.Query) (time.Time, error) {
	s, err := r.of(q)
	if err != nil {
		return time.Time{}, err
	}
	w, ok := s.(metering.IngestionWatermarks)
	if !ok {
		return time.Time{}, metering.ErrNotImplemented
	}
	return w.LastIngestedAt(ctx, q)
}

// ReadBuckets implements [metering.BucketReader] by the store of the
// query's organization; a store without usage buckets reads as no
// definition covering the read, which sends it back to the events.
func (r eventRouter) ReadBuckets(ctx context.Context, br metering.BucketRead) ([]metering.BucketTotal, bool, error) {
	s, err := r.of(br.Query)
	if err != nil {
		return nil, false, err
	}
	b, ok := s.(metering.BucketReader)
	if !ok {
		return nil, false, nil
	}
	return b.ReadBuckets(ctx, br)
}

// DefineBuckets implements the bucket definitions of
// [eventstore.BucketDefiner] (subscriptions.BucketDefiner) by the store of
// the definition's organization; a store without usage buckets fails with
// [metering.ErrBucketsUnavailable].
func (r eventRouter) DefineBuckets(ctx context.Context, d metering.BucketDefinition) error {
	s, err := r.of(metering.Query{OrganizationID: d.OrganizationID})
	if err != nil {
		return err
	}
	b, ok := s.(eventstore.BucketDefiner)
	if !ok {
		return fmt.Errorf("eventstore: define buckets: %w", metering.ErrBucketsUnavailable)
	}
	return b.DefineBuckets(ctx, d)
}
