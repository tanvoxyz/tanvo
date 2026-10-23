package pricing

import "github.com/tanvoxyz/tanvo/libs/money"

// AggregationResult is the outcome of aggregating a metric's events over a
// period, as the charge models consume it. The metering engine produces it;
// a caller pricing a known quantity fills Units (and FullUnits) only.
type AggregationResult struct {
	// Units is the aggregated value: the billed number of units, prorated
	// when the charge is.
	Units money.Decimal
	// CurrentUsageUnits is the number of units for current usage, passed
	// through to the result; nil when not computed.
	CurrentUsageUnits *money.Decimal
	// FullUnits is the number of units ignoring proration; nil when the
	// aggregation does not distinguish it.
	FullUnits *money.Decimal
	// Count is the number of events aggregated.
	Count int64
	// TotalAggregatedUnits is the total of a weighted sum aggregation,
	// passed through to the result; nil otherwise.
	TotalAggregatedUnits *money.Decimal
	// PreciseTotalAmountCents is the sum of the amounts the events carry,
	// in minor units, priced by the dynamic model; nil when not computed.
	PreciseTotalAmountCents *money.Decimal
	// RunningTotal is the cumulative aggregation after each event, used by
	// the percentage model's free units.
	RunningTotal []money.Decimal
	// PerEvent holds the value of each event, needed by the percentage
	// model with per-transaction limits and by prorated graduated pricing.
	// Nil means the per-event breakdown is unavailable (when forecasting):
	// prorated graduated charges then fall back to plain graduated pricing.
	PerEvent *PerEventAggregation
	// Groups holds one aggregation per group of a grouped charge (pricing
	// group keys, or a charge that accepts a target wallet). Nil when the
	// usage is not grouped.
	Groups []AggregationResult
	// GroupedBy holds the group's key values in an element of Groups.
	GroupedBy map[string]string
	// CurrentAggregation, MaxAggregation and UnitsApplied describe a
	// pay-in-advance event on a max or latest-style aggregation; nil
	// otherwise. See [ApplyInAdvance].
	CurrentAggregation *money.Decimal
	// MaxAggregation is the maximum aggregation reached before the event.
	MaxAggregation *money.Decimal
	// UnitsApplied is the number of units the event added to the bill.
	UnitsApplied *money.Decimal
}

// PerEventAggregation is the per-event breakdown of an aggregation.
type PerEventAggregation struct {
	// Values holds the full (unprorated) value of each event, in event
	// order; a removal is negative.
	Values []money.Decimal
	// Prorated holds the prorated value of each event, parallel to Values,
	// for prorated aggregations; nil otherwise.
	Prorated []money.Decimal
}

// fullOrUnits returns FullUnits when set, Units otherwise.
func (a AggregationResult) fullOrUnits() money.Decimal {
	if a.FullUnits != nil {
		return *a.FullUnits
	}
	return a.Units
}

// eventValues returns the per-event values, or none.
func (a AggregationResult) eventValues() []money.Decimal {
	if a.PerEvent == nil {
		return nil
	}
	return a.PerEvent.Values
}
