// Package calc holds the arithmetic both event stores finish in Go: the
// databases return exact sums, and the divisions (ratios of a period) are
// made with [money.Decimal] the way [metering.MemoryStore] makes them.
package calc

import (
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Location returns the time zone of the query.
func Location(q metering.Query) *time.Location {
	if q.Location == nil {
		return time.UTC
	}
	return q.Location
}

// From returns the start of the period, truncated to the millisecond.
func From(q metering.Query) time.Time { return q.From.Truncate(time.Millisecond).UTC() }

// Until returns the inclusive upper bound of the selected events.
func Until(q metering.Query) time.Time {
	if !q.Until.IsZero() {
		return q.Until.UTC()
	}
	return q.To.UTC()
}

// WeightedTo returns the end of a time-weighted period: [metering.Query.To]
// rounded up to the second.
func WeightedTo(q metering.Query) time.Time {
	t := q.To.UTC()
	if s := t.Truncate(time.Second); !s.Equal(t) {
		return s.Add(time.Second)
	}
	return t
}

var epoch = metering.Date{Year: 1970, Month: time.January, Day: 1}

// DayNumber returns the day of t in loc as a number of days since
// 1970-01-01.
func DayNumber(t time.Time, loc *time.Location) int {
	return metering.DateOf(t, loc).Sub(epoch)
}

// DateOfDay returns the date of a day number.
func DateOfDay(n int) metering.Date { return epoch.AddDays(n) }

// PeriodDays returns the number of days of the period of a query that
// needs one.
func PeriodDays(q metering.Query) (money.Decimal, error) {
	if q.PeriodDays <= 0 {
		return money.Zero, fmt.Errorf("eventstore: query period of %d days", q.PeriodDays)
	}
	return money.NewFromInt(int64(q.PeriodDays)), nil
}

// UniquePeriod returns the period of prorated unique counts, one day when
// the query has none.
func UniquePeriod(q metering.Query) money.Decimal {
	if q.PeriodDays <= 0 {
		return money.One
	}
	return money.NewFromInt(int64(q.PeriodDays))
}

// Ratio returns days over the period.
func Ratio(days int64, period money.Decimal) money.Decimal {
	return money.NewFromInt(days).Div(period)
}

// RemainingDays returns the number of days from day to the day of
// [metering.Query.To], both included.
func RemainingDays(q metering.Query, day int) int64 {
	return int64(DayNumber(q.To, Location(q)) - day + 1)
}

// DaySum is the sum of the values of the events of one day.
type DaySum struct {
	Day   int
	Sum   money.Decimal
	Count int64
}

// Prorate returns the sum of the days and its prorated counterpart: each
// day's sum weighted by the share of the period from the day to the day of
// [metering.Query.To], or by persistedDays over the period.
func Prorate(q metering.Query, days []DaySum, period money.Decimal, persistedDays *int) metering.ProratedAggregate {
	r := metering.ProratedAggregate{Value: money.Zero, ProratedValue: money.Zero}
	for _, d := range days {
		ratio := Ratio(RemainingDays(q, d.Day), period)
		if persistedDays != nil {
			ratio = Ratio(int64(*persistedDays), period)
		}
		r.Value = r.Value.Add(d.Sum)
		r.ProratedValue = r.ProratedValue.Add(d.Sum.Mul(ratio))
		r.Count += d.Count
	}
	return r
}

// Weighted is the part of a time-weighted sum a store computes: over the
// events in order, the running total of their values times the time (in
// microseconds) until the next event or the end of the period.
type Weighted struct {
	Micros    money.Decimal
	Variation money.Decimal
	Count     int64
}

// Weight returns the time-weighted sum of a query starting from initial:
// the initial value holds over the whole period from [From] to
// [WeightedTo], the events add their running total.
func Weight(q metering.Query, w Weighted, initial, period money.Decimal) metering.WeightedAggregate {
	span := WeightedTo(q).Sub(From(q)).Microseconds()
	total := w.Micros.Add(initial.Mul(money.NewFromInt(span)))
	periodMicros := period.Mul(money.NewFromInt(86_400_000_000))
	return metering.WeightedAggregate{
		Value:     total.Div(periodMicros),
		Variation: w.Variation,
		Count:     w.Count,
	}
}

// Groups returns the groups of the keys from the values a store read, a
// nil value standing for a missing or empty property.
func Groups(keys []string, values []*string) metering.Groups {
	g := make(metering.Groups, len(keys))
	for i, k := range keys {
		if v := values[i]; v != nil && *v != "" {
			s := *v
			g[k] = &s
		} else {
			g[k] = nil
		}
	}
	return g
}

// Interval is an effective unique-count operation as a store reads it,
// with the number of days the value stays present from it.
type Interval struct {
	Timestamp time.Time
	Property  string
	Operation metering.Operation
	Days      int64
}

// UniqueInterval returns the interval of the period.
func UniqueInterval(iv Interval, period money.Decimal) metering.UniqueCountInterval {
	ratio := money.Zero
	if iv.Operation == metering.OperationAdd {
		ratio = Ratio(iv.Days, period)
	}
	return metering.UniqueCountInterval{
		Timestamp:     iv.Timestamp.UTC(),
		Property:      iv.Property,
		Operation:     iv.Operation,
		ProratedValue: ratio,
	}
}

// WeightGroups returns the time-weighted sums of the groups of events, each
// starting from its initial value, followed by the groups that only have an
// initial value.
func WeightGroups(q metering.Query, groups []metering.Groups, sums []Weighted, initial []metering.GroupedValue, period money.Decimal) []metering.GroupedWeightedAggregate {
	var out []metering.GroupedWeightedAggregate
	add := func(g metering.Groups, w Weighted, v money.Decimal) {
		r := Weight(q, w, v, period)
		out = append(out, metering.GroupedWeightedAggregate{Groups: g, Value: r.Value, Variation: r.Variation, Count: r.Count})
	}
	for i, g := range groups {
		v := money.Zero
		if j := slices.IndexFunc(initial, func(iv metering.GroupedValue) bool { return iv.Groups.Equal(g) }); j >= 0 {
			v = initial[j].Value
		}
		add(g, sums[i], v)
	}
	for _, iv := range initial {
		if !slices.ContainsFunc(groups, iv.Groups.Equal) && !slices.ContainsFunc(out[len(groups):], func(r metering.GroupedWeightedAggregate) bool { return r.Groups.Equal(iv.Groups) }) {
			add(iv.Groups, Weighted{Micros: money.Zero, Variation: money.Zero}, iv.Value)
		}
	}
	return out
}
