package metering

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// CachedAggregation is the running state of a charge saved after each
// pay-in-advance event, and at the end of a period for recurring
// time-weighted sums.
type CachedAggregation struct {
	Timestamp     time.Time `json:"timestamp"`
	TransactionID string    `json:"transaction_id,omitempty"`
	// Groups are the pricing group values the state is for; empty for an
	// ungrouped charge.
	Groups Groups `json:"groups,omitempty"`
	// CurrentAggregation, MaxAggregation and MaxAggregationWithProration
	// are nil when the state does not carry them: a recurring state holds
	// only CurrentAggregation, a pay-in-advance state what its aggregation
	// produced.
	CurrentAggregation *money.Decimal `json:"current_aggregation,omitempty"`
	MaxAggregation     *money.Decimal `json:"max_aggregation,omitempty"`
	// MaxAggregationWithProration is MaxAggregation with proration applied.
	MaxAggregationWithProration *money.Decimal `json:"max_aggregation_with_proration,omitempty"`
	PresentationBreakdowns      []GroupedValue `json:"presentation_breakdowns,omitempty"`
}

// ErrMissingCachedValue reports a cached aggregation lacking a value an
// aggregation reads; lago raises on it (BigDecimal(nil) in
// app/services/billable_metrics/aggregations/base_service.rb:185-186 and
// the services reading find_cached_aggregation).
var ErrMissingCachedValue = errors.New("metering: cached aggregation has no value")

// cachedValue returns v, or [ErrMissingCachedValue] naming the value when
// it is nil.
func cachedValue(v *money.Decimal, name string) (money.Decimal, error) {
	if v == nil {
		return money.Zero, fmt.Errorf("%w: %s", ErrMissingCachedValue, name)
	}
	return *v, nil
}

// CachedAggregations reads the cached aggregations of one charge (or charge
// filter) of one external subscription.
type CachedAggregations interface {
	// LatestInPeriod returns the most recent cached aggregation (by
	// timestamp, then storage) whose timestamp, truncated to the second, is
	// within [from, to] (also truncated), whose groups equal groups and,
	// when exclude is set, whose transaction ID is set and is not exclude
	// (SQL inequality, as lago's where.not, base_service.rb:223). It
	// returns nil when there is none.
	LatestInPeriod(ctx context.Context, from, to time.Time, groups Groups, exclude string) (*CachedAggregation, error)
	// Before returns the cached aggregations stamped before t, most recent
	// first.
	Before(ctx context.Context, t time.Time) ([]CachedAggregation, error)
}

// MemoryCache is a [CachedAggregations] over a slice, in storage order.
type MemoryCache []CachedAggregation

var _ CachedAggregations = MemoryCache(nil)

// newestFirst returns the aggregations ordered by timestamp then storage,
// most recent first.
func (c MemoryCache) newestFirst() []CachedAggregation {
	out := slices.Clone(c)
	slices.Reverse(out)
	slices.SortStableFunc(out, func(a, b CachedAggregation) int { return b.Timestamp.Compare(a.Timestamp) })
	return out
}

// LatestInPeriod implements [CachedAggregations].
func (c MemoryCache) LatestInPeriod(_ context.Context, from, to time.Time, groups Groups, exclude string) (*CachedAggregation, error) {
	from, to = from.Truncate(time.Second), to.Truncate(time.Second)
	for _, a := range c.newestFirst() {
		if a.Timestamp.Before(from) || a.Timestamp.After(to) || !a.Groups.Equal(groups) {
			continue
		}
		if exclude != "" && (a.TransactionID == "" || a.TransactionID == exclude) {
			continue
		}
		return &a, nil
	}
	return nil, nil //nolint:nilnil // no cached aggregation is not an error
}

// Before implements [CachedAggregations].
func (c MemoryCache) Before(_ context.Context, t time.Time) ([]CachedAggregation, error) {
	return slices.DeleteFunc(c.newestFirst(), func(a CachedAggregation) bool { return !a.Timestamp.Before(t) }), nil
}
