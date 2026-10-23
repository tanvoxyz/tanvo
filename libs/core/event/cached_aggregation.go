package event

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// CachedAggregation is the running aggregation state of a charge for one
// external subscription (domain §10.3): saved after each pay-in-advance
// event, so that the next event prices incrementally, and at the end of a
// period for recurring time-weighted sums, so that the next period starts
// from it.
type CachedAggregation struct {
	// ID identifies the state.
	ID core.CachedAggregationID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// ExternalSubscriptionID is the subscription's external ID.
	ExternalSubscriptionID string
	// ChargeID is the charge.
	ChargeID core.ChargeID
	// ChargeFilterID is the charge filter; zero for the charge's default
	// bucket (or a charge without filters).
	ChargeFilterID core.ChargeFilterID
	// Timestamp is the instant of the state.
	Timestamp time.Time
	// EventTransactionID is the transaction of the pay-in-advance event
	// that produced the state; empty for recurring states.
	EventTransactionID string
	// GroupedBy are the pricing group values the state is for; a nil value
	// is a group key without a value. Empty for an ungrouped charge.
	GroupedBy map[string]*string
	// CurrentAggregation is the aggregated value; nil when the state has
	// none.
	CurrentAggregation *money.Decimal
	// MaxAggregation is the maximum the value reached in the period; nil
	// for recurring states, which carry only CurrentAggregation.
	MaxAggregation *money.Decimal
	// MaxAggregationWithProration is MaxAggregation with proration applied;
	// nil when the aggregation did not produce it.
	MaxAggregationWithProration *money.Decimal
	// PresentationBreakdowns split CurrentAggregation by presentation
	// group.
	PresentationBreakdowns []GroupedValue
	// CreatedAt is the creation instant; it orders states of one
	// timestamp.
	CreatedAt time.Time
}

// GroupedValue is a value of one group of keys.
type GroupedValue struct {
	// Groups are the values by key; a nil value is a key without a value.
	Groups map[string]*string
	// Value is the value.
	Value money.Decimal
}

// Validate checks the state.
func (a *CachedAggregation) Validate() error {
	var v core.Validator
	v.Required("external_subscription_id", a.ExternalSubscriptionID)
	v.Check(!a.ChargeID.IsZero(), "charge_id", core.CodeRequired)
	v.Check(!a.Timestamp.IsZero(), "timestamp", core.CodeRequired)
	return v.Err()
}

// EqualGroups reports whether two pricing groups hold the same keys and
// values (nil and empty are equal).
func EqualGroups(a, b map[string]*string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || (av == nil) != (bv == nil) || (av != nil && *av != *bv) {
			return false
		}
	}
	return true
}

// CloneGroups returns a deep copy of g.
func CloneGroups(g map[string]*string) map[string]*string {
	if g == nil {
		return nil
	}
	out := make(map[string]*string, len(g))
	for _, k := range slices.Sorted(maps.Keys(g)) {
		v := g[k]
		if v != nil {
			c := *v
			v = &c
		}
		out[k] = v
	}
	return out
}

// CachedAggregationScope selects the states of one charge (bucket) of one
// external subscription.
type CachedAggregationScope struct {
	// ExternalSubscriptionID is the subscription's external ID.
	ExternalSubscriptionID string
	// ChargeID is the charge.
	ChargeID core.ChargeID
	// ChargeFilterID is the charge filter; zero for the default bucket of
	// a charge with filters and for a charge without filters.
	ChargeFilterID core.ChargeFilterID
	// DefaultBucket marks the default bucket of a charge with filters
	// priced in arrears or for current usage. In the lookups
	// (LatestInPeriod, Before) a set ChargeFilterID selects its states, a
	// zero one with DefaultBucket the states without a filter, and a zero
	// one without DefaultBucket the states of every filter of the charge.
	// This is lago's lookup: it filters by charge_filter_id when given a
	// charge filter (app/services/billable_metrics/aggregations/
	// base_service.rb:224, weighted_sum_service.rb:143), which fee
	// computation passes for the default bucket as an unsaved filter
	// (app/services/fees/charge_service.rb:584,
	// app/services/fees/charge_service/sources/charge.rb:63-67) and the
	// pay-in-advance aggregation only when saved
	// (app/services/charges/pay_in_advance_aggregation_service.rb:66).
	// CreateIfAbsent ignores it and matches ChargeFilterID exactly, a zero
	// one matching the states without a filter (find_or_initialize_by,
	// app/services/fees/charge_service.rb:532-539).
	DefaultBucket bool
}

// CachedAggregationRepository stores cached aggregation states.
type CachedAggregationRepository interface {
	// Create inserts a.
	Create(ctx context.Context, a *CachedAggregation) error
	// CreateIfAbsent inserts a unless a state of the same scope, groups
	// and timestamp exists, and reports whether it inserted it.
	CreateIfAbsent(ctx context.Context, a *CachedAggregation) (bool, error)
	// LatestInPeriod returns the most recent state of the scope (by
	// timestamp, then creation) whose timestamp is within [from, to], both
	// truncated to the second, whose groups equal groups and, when exclude
	// is set, whose event transaction is set and is not exclude (SQL
	// inequality, as lago's where.not, base_service.rb:223);
	// core.ErrNotFound when there is none.
	LatestInPeriod(ctx context.Context, org core.OrganizationID, sc CachedAggregationScope, from, to time.Time, groups map[string]*string, exclude string) (*CachedAggregation, error)
	// Before returns the states of the scope stamped before t, most recent
	// first (by timestamp, then creation).
	Before(ctx context.Context, org core.OrganizationID, sc CachedAggregationScope, t time.Time) ([]*CachedAggregation, error)
}
