package metering

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// PerEventOptions tune [PerEvent].
type PerEventOptions struct {
	// ExcludeEvent leaves the stored pay-in-advance event out (sum).
	ExcludeEvent bool `json:"exclude_event,omitempty"`
	// IncludeEventValue adds the value of the request event, which is not
	// stored (count, sum and unique count).
	IncludeEventValue bool `json:"include_event_value,omitempty"`
	// GroupValues, when not nil, replace the group values of the request.
	GroupValues Groups `json:"group_values,omitempty"`
}

// PerEventResult is the contribution of each event to the units, in
// timestamp order.
type PerEventResult struct {
	Values []money.Decimal `json:"values"`
	// Prorated are the prorated contributions of a prorated charge.
	Prorated []money.Decimal `json:"prorated,omitempty"`
}

// PerEvent returns the units each event of the period contributes, which
// graduated percentage charges price event by event. It is not implemented
// for latest and time-weighted sums.
func PerEvent(ctx context.Context, store Store, req Request, opts PerEventOptions) (PerEventResult, error) {
	a := newAggregator(ctx, store, req)
	if a.bypass() {
		r := PerEventResult{Values: []money.Decimal{}}
		if req.Prorated && (req.Metric.Aggregation == AggregationSum || req.Metric.Aggregation == AggregationUniqueCount) {
			r.Prorated = []money.Decimal{}
		}
		return r, nil
	}
	switch req.Metric.Aggregation {
	case AggregationCount:
		return a.perEventCount(withGroups(a.query(), opts.GroupValues), opts.IncludeEventValue)
	case AggregationUniqueCount:
		if req.Prorated {
			return a.perEventProratedUniqueCount(opts.GroupValues)
		}
		q := withGroups(a.query(), opts.GroupValues)
		q.IncludeHistory = false
		return a.perEventCount(q, opts.IncludeEventValue)
	case AggregationSum:
		if req.Prorated {
			return a.perEventProratedSum(opts)
		}
		values, err := a.perEventSum(opts)
		return PerEventResult{Values: values}, err
	case AggregationMax:
		return a.perEventMax(withGroups(a.query(), opts.GroupValues))
	default:
		return PerEventResult{}, fmt.Errorf("%w: per-event %s", ErrNotImplemented, req.Metric.Aggregation)
	}
}

func ones(n int) []money.Decimal {
	out := make([]money.Decimal, n)
	for i := range out {
		out[i] = money.One
	}
	return out
}

func (a *aggregator) perEventCount(q Query, includeEvent bool) (PerEventResult, error) {
	n, err := a.store.Count(a.ctx, q)
	if err != nil {
		return PerEventResult{}, a.wrap("count", err)
	}
	if includeEvent {
		n++
	}
	return PerEventResult{Values: ones(int(n))}, nil
}

func (a *aggregator) perEventSum(opts PerEventOptions) ([]money.Decimal, error) {
	q := withGroups(a.currentQuery(), opts.GroupValues)
	exclude := ""
	if opts.ExcludeEvent {
		exclude = a.excludedTransaction()
	}
	values, err := a.store.EventValues(a.ctx, q, 0, exclude)
	if err != nil {
		return nil, a.wrap("event values", err)
	}
	if opts.IncludeEventValue {
		v, err := a.eventValue()
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	if values == nil {
		values = []money.Decimal{}
	}
	return values, nil
}

func (a *aggregator) perEventMax(q Query) (PerEventResult, error) {
	m, err := a.store.Max(a.ctx, q)
	if err != nil {
		return PerEventResult{}, a.wrap("max", err)
	}
	values, err := a.store.EventValues(a.ctx, q, 0, "")
	if err != nil {
		return PerEventResult{}, a.wrap("event values", err)
	}
	out := make([]money.Decimal, len(values))
	seen := false
	for i, v := range values {
		out[i] = money.Zero
		if !seen && v.Equal(m.Value) {
			seen = true
			out[i] = v
		}
	}
	return PerEventResult{Values: out}, nil
}

func (a *aggregator) perEventProratedSum(opts PerEventOptions) (PerEventResult, error) {
	persisted, err := a.store.Sum(a.ctx, a.persistedQuery())
	if err != nil {
		return PerEventResult{}, a.wrap("persisted sum", err)
	}
	proRata, err := a.persistedProRata()
	if err != nil {
		return PerEventResult{}, err
	}
	r := PerEventResult{Values: []money.Decimal{}, Prorated: []money.Decimal{}}
	if !persisted.Value.IsZero() {
		r.Values = append(r.Values, persisted.Value)
		r.Prorated = append(r.Prorated, persisted.Value.Mul(proRata))
	}
	values, err := a.perEventSum(PerEventOptions{ExcludeEvent: opts.ExcludeEvent, GroupValues: opts.GroupValues})
	if err != nil {
		return PerEventResult{}, err
	}
	r.Values = append(r.Values, values...)
	prorated, err := a.store.ProratedEventValues(a.ctx, withGroups(a.currentQuery(), opts.GroupValues))
	if err != nil {
		return PerEventResult{}, a.wrap("prorated event values", err)
	}
	r.Prorated = append(r.Prorated, prorated...)
	return r, nil
}

func (a *aggregator) perEventProratedUniqueCount(groups Groups) (PerEventResult, error) {
	rows, err := a.store.UniqueCountIntervals(a.ctx, withGroups(a.uniqueQuery(), groups), true)
	if err != nil {
		return PerEventResult{}, a.wrap("unique count intervals", err)
	}
	r := PerEventResult{Values: []money.Decimal{}, Prorated: []money.Decimal{}}
	for _, row := range rows {
		v := money.One
		if row.Operation == OperationRemove {
			v = money.One.Neg()
		}
		r.Values = append(r.Values, v)
		r.Prorated = append(r.Prorated, ceil5(row.ProratedValue))
	}
	return r, nil
}

// BreakdownItem is one line of the day-by-day breakdown of a prorated
// charge.
type BreakdownItem struct {
	// Date is the day, in the customer's time zone.
	Date Date `json:"date"`
	// Action is "add", "remove" or, for a unique value added and removed
	// within the period, "add_and_removed".
	Action        string        `json:"action"`
	Amount        money.Decimal `json:"amount"`
	Duration      int           `json:"duration"`
	TotalDuration int           `json:"total_duration"`
}

// Breakdown returns the day-by-day breakdown of a prorated sum or unique
// count: the units added or removed each day and the number of days of the
// period they are billed for.
func Breakdown(ctx context.Context, store Store, req Request) ([]BreakdownItem, error) {
	a := newAggregator(ctx, store, req)
	switch req.Metric.Aggregation {
	case AggregationSum:
		return a.sumBreakdown()
	case AggregationUniqueCount:
		return a.uniqueCountBreakdown()
	default:
		return nil, fmt.Errorf("%w: breakdown of %s", ErrNotImplemented, req.Metric.Aggregation)
	}
}

func action(v money.Decimal) string {
	if v.IsNegative() {
		return string(OperationRemove)
	}
	return string(OperationAdd)
}

func (a *aggregator) sumBreakdown() ([]BreakdownItem, error) {
	loc := a.query().location()
	from, to := DateOf(a.req.From, loc), DateOf(a.req.To, loc)
	var items []BreakdownItem

	q := a.historyQuery(0)
	persisted, err := a.store.Sum(a.ctx, q)
	if err != nil {
		return nil, a.wrap("persisted sum", err)
	}
	if !persisted.Value.IsZero() {
		items = append(items, BreakdownItem{
			Date:          from,
			Action:        action(persisted.Value),
			Amount:        persisted.Value,
			Duration:      to.AddDays(1).Sub(from),
			TotalDuration: a.req.PeriodDays,
		})
	}
	days, err := a.store.SumByDay(a.ctx, a.currentQuery())
	if err != nil {
		return nil, a.wrap("sum by day", err)
	}
	for _, d := range days {
		items = append(items, BreakdownItem{
			Date:          d.Date,
			Action:        action(d.Value),
			Amount:        d.Value,
			Duration:      to.AddDays(1).Sub(d.Date),
			TotalDuration: a.req.PeriodDays,
		})
	}
	slices.SortStableFunc(items, func(x, y BreakdownItem) int { return x.Date.Compare(y.Date) })
	return items, nil
}

func (a *aggregator) uniqueCountBreakdown() ([]BreakdownItem, error) {
	q := a.uniqueQuery()
	loc := q.location()
	rows, err := a.store.UniqueCountIntervals(a.ctx, q, true)
	if err != nil {
		return nil, a.wrap("unique count intervals", err)
	}
	from := a.req.From
	periodDays := money.NewFromInt(int64(roundDays(a.req.To.Sub(from))))
	var items []BreakdownItem
	for _, group := range groupBy(rows, func(r UniqueCountInterval) string { return r.Property }) {
		first := group[0]
		op := string(first.Operation)
		at := first.Timestamp
		if at.Before(from) {
			at = from
		}
		switch {
		case len(group)%2 == 0:
			if first.Timestamp.Before(from) {
				op = string(OperationRemove)
				at = group[len(group)-1].Timestamp
			} else {
				op = "add_and_removed"
			}
		case len(group) > 2:
			op = string(OperationAdd)
		}
		prorated := first.ProratedValue
		if len(group) > 2 {
			prorated = money.Zero
			for _, r := range group {
				prorated = prorated.Add(r.ProratedValue)
			}
		}
		duration, _ := periodDays.Mul(prorated).Round(0, money.HalfUp).Int64()
		items = append(items, BreakdownItem{
			Date:          DateOf(at, loc),
			Action:        op,
			Amount:        first.ProratedValue.Round(0, money.Ceiling),
			Duration:      int(duration),
			TotalDuration: a.req.PeriodDays,
		})
	}
	slices.SortStableFunc(items, func(x, y BreakdownItem) int { return x.Date.Compare(y.Date) })
	return items, nil
}

// roundDays returns d in days, rounded half away from zero.
func roundDays(d time.Duration) int {
	day := 24 * time.Hour
	n := d / day
	if r := d % day; r >= day/2 {
		n++
	} else if r <= -day/2 {
		n--
	}
	return int(n)
}
