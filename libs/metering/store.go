package metering

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// Query selects the events an aggregation reads: the events of a billable
// metric code for an external subscription within a time window, narrowed
// by a charge filter and by pricing group values.
type Query struct {
	OrganizationID string `json:"organization_id,omitempty"`
	// SubscriptionID is the external subscription ID: events of the
	// subscriptions replacing each other on upgrade share it.
	SubscriptionID string `json:"subscription_id,omitempty"`
	Code           string `json:"code,omitempty"`

	// From is the start of the period, truncated to the millisecond, and
	// the inclusive lower bound of the selected events unless
	// IncludeHistory is set.
	From time.Time `json:"from"`
	// IncludeHistory selects the events before From too, as recurring
	// metrics carry usage over from earlier periods. From remains the start
	// of the period prorated and time-weighted values are measured from.
	IncludeHistory bool `json:"include_history,omitempty"`
	// To is the inclusive upper bound of the window and the end of the
	// period prorated and time-weighted values are measured to.
	To time.Time `json:"to"`
	// Until, when set, replaces To as the inclusive upper bound of the
	// selected events (the timestamp of a pay-in-advance event).
	Until time.Time `json:"until,omitzero"`
	// UntilTransactionID names the event at Until that closes the window:
	// events sharing its timestamp are selected only up to it in ingestion
	// order, so that each gets a distinct position.
	UntilTransactionID string `json:"until_transaction_id,omitempty"`

	// Property is the aggregated property.
	Property string `json:"property,omitempty"`
	// Numeric keeps only the events whose property is a plain decimal
	// number (see [Properties]).
	Numeric bool `json:"numeric,omitempty"`

	// Filter narrows the events to a charge filter.
	Filter Filter `json:"filter,omitzero"`
	// GroupValues narrows the events to pricing group values: a set value
	// must be equal, a nil value requires the property to be missing or
	// empty.
	GroupValues Groups `json:"group_values,omitempty"`

	// PeriodDays is the number of days of the billing period, which
	// prorated and time-weighted values are measured against.
	PeriodDays int `json:"period_days,omitempty"`
	// Location is the customer's time zone, in which days are counted. Nil
	// means UTC.
	Location *time.Location `json:"-"`
}

// location returns the time zone of the query.
func (q Query) location() *time.Location {
	if q.Location == nil {
		return time.UTC
	}
	return q.Location
}

// Total is an aggregated value with the number of events behind it.
type Total struct {
	Value money.Decimal `json:"value"`
	Count int64         `json:"count"`
}

// GroupedAggregate is an [Total] of one group of events.
type GroupedAggregate struct {
	Groups Groups        `json:"groups"`
	Value  money.Decimal `json:"value"`
	Count  int64         `json:"count"`
}

// LastValue is the value of the most recent event, nil when there is none,
// with the number of events.
type LastValue struct {
	Value *money.Decimal `json:"value"`
	Count int64          `json:"count"`
}

// GroupedEvent is the most recent event of a group.
type GroupedEvent struct {
	Groups    Groups        `json:"groups"`
	Timestamp time.Time     `json:"timestamp"`
	Value     money.Decimal `json:"value"`
}

// ProratedAggregate is a sum along with its prorated counterpart.
type ProratedAggregate struct {
	Value         money.Decimal `json:"value"`
	ProratedValue money.Decimal `json:"prorated_value"`
	Count         int64         `json:"count"`
}

// GroupedProratedAggregate is a [ProratedAggregate] of one group.
type GroupedProratedAggregate struct {
	Groups        Groups        `json:"groups"`
	Value         money.Decimal `json:"value"`
	ProratedValue money.Decimal `json:"prorated_value"`
	Count         int64         `json:"count"`
}

// WeightedAggregate is a time-weighted sum.
type WeightedAggregate struct {
	// Value is the sum, over the period, of the running total weighted by
	// the fraction of the period it held.
	Value money.Decimal `json:"value"`
	// Variation is the sum of the event values (the initial value
	// excluded).
	Variation money.Decimal `json:"variation"`
	Count     int64         `json:"count"`
}

// GroupedWeightedAggregate is a [WeightedAggregate] of one group.
type GroupedWeightedAggregate struct {
	Groups    Groups        `json:"groups"`
	Value     money.Decimal `json:"value"`
	Variation money.Decimal `json:"variation"`
	Count     int64         `json:"count"`
}

// DailyValue is a sum over one day.
type DailyValue struct {
	Date  Date          `json:"date"`
	Value money.Decimal `json:"value"`
}

// UniqueCountInterval is one interval of presence of a unique-count value:
// an effective add (or remove) and the fraction of the period the value
// stays present from it.
type UniqueCountInterval struct {
	Timestamp     time.Time     `json:"timestamp"`
	Property      string        `json:"property"`
	Operation     Operation     `json:"operation"`
	ProratedValue money.Decimal `json:"prorated_value"`
}

// Store is the set of event queries the aggregations need. Every method
// reads the events selected by the [Query]: the latest copy of each event
// (by transaction ID and timestamp) of the code and subscription, within
// [Query.From] and [Query.To] (or [Query.Until]), passing [Query.Filter]
// and [Query.GroupValues], and, with [Query.Numeric], carrying a numeric
// [Query.Property].
//
// Grouped methods return one entry per distinct combination of the values
// of the given properties, a missing or empty one reading as nil; their
// order is unspecified.
type Store interface {
	// Count returns the number of events.
	Count(ctx context.Context, q Query) (int64, error)
	// GroupedCount returns the number of events per group.
	GroupedCount(ctx context.Context, q Query, by []string) ([]GroupedAggregate, error)

	// Sum returns the sum of the property.
	Sum(ctx context.Context, q Query) (Total, error)
	// GroupedSum returns the sum of the property per group.
	GroupedSum(ctx context.Context, q Query, by []string) ([]GroupedAggregate, error)

	// Max returns the largest value of the property, zero without events.
	Max(ctx context.Context, q Query) (Total, error)
	// GroupedMax returns the largest value of the property per group.
	GroupedMax(ctx context.Context, q Query, by []string) ([]GroupedAggregate, error)

	// Last returns the value of the most recent event (by timestamp, then
	// ingestion).
	Last(ctx context.Context, q Query) (LastValue, error)
	// GroupedLast returns, for each group of the partition properties, the
	// value of its most recent event with the groups of the columns read
	// on that event, and the number of events of the group. Without
	// partition properties, it returns the most recent event of all.
	GroupedLast(ctx context.Context, q Query, partition, columns []string) ([]GroupedAggregate, error)
	// LastEvent returns the most recent event, nil when there is none.
	LastEvent(ctx context.Context, q Query) (*Event, error)
	// GroupedLastEvent returns the most recent event of each group.
	GroupedLastEvent(ctx context.Context, q Query, by []string) ([]GroupedEvent, error)

	// SumPreciseTotalAmountCents returns the sum of the amounts carried by
	// the events.
	SumPreciseTotalAmountCents(ctx context.Context, q Query) (money.Decimal, error)
	// GroupedSumPreciseTotalAmountCents returns the sum of the amounts per
	// group.
	GroupedSumPreciseTotalAmountCents(ctx context.Context, q Query, by []string) ([]GroupedAggregate, error)

	// EventValues returns the values of the property in timestamp order,
	// at most limit of them when limit is positive, leaving out the event
	// with transaction ID exclude when it is set.
	EventValues(ctx context.Context, q Query, limit int, exclude string) ([]money.Decimal, error)
	// ProratedEventValues returns, in timestamp order, the value of each
	// event multiplied by the share of the period from its day to the day
	// of [Query.To] (both included).
	ProratedEventValues(ctx context.Context, q Query) ([]money.Decimal, error)

	// ProratedSum returns the sum of the property and its prorated
	// counterpart: each value weighted by the share of the period from its
	// day to the day of [Query.To], or, when persistedDays is not nil, by
	// persistedDays over the period.
	ProratedSum(ctx context.Context, q Query, persistedDays *int) (ProratedAggregate, error)
	// GroupedProratedSum is [Store.ProratedSum] per group.
	GroupedProratedSum(ctx context.Context, q Query, by []string, persistedDays *int) ([]GroupedProratedAggregate, error)
	// SumByDay returns the sum of the property per day, in day order.
	SumByDay(ctx context.Context, q Query) ([]DailyValue, error)

	// UniqueCount returns the number of distinct property values present,
	// following the add and remove operations of the events.
	UniqueCount(ctx context.Context, q Query) (money.Decimal, error)
	// GroupedUniqueCount is [Store.UniqueCount] per group.
	GroupedUniqueCount(ctx context.Context, q Query, by []string) ([]GroupedAggregate, error)
	// ProratedUniqueCount returns the sum, over the property values, of the
	// share of the period they were present.
	ProratedUniqueCount(ctx context.Context, q Query) (money.Decimal, error)
	// GroupedProratedUniqueCount is [Store.ProratedUniqueCount] per group.
	GroupedProratedUniqueCount(ctx context.Context, q Query, by []string) ([]GroupedAggregate, error)
	// UniqueCountIntervals returns the intervals behind
	// [Store.ProratedUniqueCount], ordered by timestamp then property,
	// leaving out the ones without prorated value unless withRemove is
	// set.
	UniqueCountIntervals(ctx context.Context, q Query, withRemove bool) ([]UniqueCountInterval, error)
	// ActiveUniqueProperty reports whether the property value of e is
	// already present before it: the latest earlier event with that value
	// (e itself left out) adds it.
	ActiveUniqueProperty(ctx context.Context, q Query, e Event) (bool, error)

	// WeightedSum returns the time-weighted sum of the property over the
	// period from [Query.From] to [Query.To] starting from initial.
	WeightedSum(ctx context.Context, q Query, initial money.Decimal) (WeightedAggregate, error)
	// GroupedWeightedSum is [Store.WeightedSum] per group, each group
	// starting from its initial value (zero when it has none). The groups
	// are those of the events and of the initial values; none yields an
	// empty result.
	GroupedWeightedSum(ctx context.Context, q Query, by []string, initial []GroupedValue) ([]GroupedWeightedAggregate, error)
}

// Date is a calendar day.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// DateOf returns the day of t in loc.
func DateOf(t time.Time, loc *time.Location) Date {
	y, m, d := t.In(loc).Date()
	return Date{y, m, d}
}

// ParseDate parses a "2006-01-02" date.
func ParseDate(s string) (Date, error) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return Date{}, fmt.Errorf("metering: date: %w", err)
	}
	return DateOf(t, time.UTC), nil
}

func (d Date) time() time.Time { return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC) }

// AddDays returns the day n days after d.
func (d Date) AddDays(n int) Date { return DateOf(d.time().AddDate(0, 0, n), time.UTC) }

// Sub returns the number of days from e to d.
func (d Date) Sub(e Date) int { return int(d.time().Sub(e.time()) / (24 * time.Hour)) }

// Compare returns -1, 0 or +1 as d is before, equal to or after e.
func (d Date) Compare(e Date) int { return d.time().Compare(e.time()) }

// String returns the date as "2006-01-02".
func (d Date) String() string { return d.time().Format(time.DateOnly) }

// MarshalJSON encodes the date as "2006-01-02".
func (d Date) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// UnmarshalJSON decodes a "2006-01-02" date.
func (d *Date) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("metering: date: %w", err)
	}
	v, err := ParseDate(s)
	if err != nil {
		return err
	}
	*d = v
	return nil
}
