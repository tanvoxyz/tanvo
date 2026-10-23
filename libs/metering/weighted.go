package metering

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// latestUnits is the number of units a recurring time-weighted sum holds at
// the start of the period, with their breakdowns.
type latestUnits struct {
	value      money.Decimal
	breakdowns []GroupedValue
}

// latest returns the units held at the start of the period: those of the
// latest cached aggregation before it, or, for a subscription replacing
// another one, the sum of every earlier event.
func (a *aggregator) latestUnits() (latestUnits, error) {
	cached, err := a.cachedBefore()
	if err != nil {
		return latestUnits{}, err
	}
	for _, c := range cached {
		if len(c.Groups) == 0 {
			return latestUnits{value: c.CurrentAggregation, breakdowns: c.PresentationBreakdowns}, nil
		}
	}
	if !a.req.HasPreviousSubscription {
		return latestUnits{value: money.Zero}, nil
	}
	q := a.historyQuery(time.Second)
	s, err := a.store.Sum(a.ctx, q)
	if err != nil {
		return latestUnits{}, a.wrap("previous units", err)
	}
	u := latestUnits{value: s.Value}
	if len(a.req.PresentationBy) > 0 {
		rows, err := a.store.GroupedSum(a.ctx, q, a.breakdownKeys())
		if err != nil {
			return latestUnits{}, a.wrap("previous units", err)
		}
		u.breakdowns = groupedValues(rows)
	}
	return u, nil
}

func (a *aggregator) weightedSum() (Result, error) {
	if a.grouped() {
		return a.groupedWeightedSum()
	}
	if a.bypass() {
		return nullResult(nil), nil
	}
	q := a.query()
	recurring := a.req.Metric.Recurring
	latest := latestUnits{value: money.Zero}
	if recurring {
		var err error
		if latest, err = a.latestUnits(); err != nil {
			return Result{}, err
		}
	}
	w, err := a.store.WeightedSum(a.ctx, q, latest.value)
	if err != nil {
		return Result{}, a.wrap("weighted sum", err)
	}
	r := Result{
		Aggregation:          w.Value.Round(20, money.Ceiling),
		Count:                money.NewFromInt(w.Count),
		Variation:            ptr(w.Variation),
		TotalAggregatedUnits: ptr(w.Variation),
	}
	if len(a.req.PresentationBy) > 0 {
		rows, err := a.store.GroupedWeightedSum(a.ctx, q, a.breakdownKeys(), latest.breakdowns)
		if err != nil {
			return Result{}, a.wrap("weighted sum breakdowns", err)
		}
		for _, row := range rows {
			r.Breakdowns = append(r.Breakdowns, GroupedValue{Groups: row.Groups, Value: row.Value})
		}
	}
	if recurring {
		r.TotalAggregatedUnits = ptr(latest.value.Add(w.Variation))
		last, err := a.store.LastEvent(a.ctx, q)
		if err != nil {
			return Result{}, a.wrap("last event", err)
		}
		at := a.req.From
		if last != nil {
			at = last.Timestamp
		}
		r.RecurringUpdatedAt = &at
		if len(r.Breakdowns) == 0 {
			r.Breakdowns = latest.breakdowns
		}
	}
	return r, nil
}

// groupedLatest returns the units per group held at the start of the
// period, with their breakdowns.
func (a *aggregator) groupedLatest() ([]GroupedValue, []GroupedValue, error) {
	cached, err := a.cachedBefore()
	if err != nil {
		return nil, nil, err
	}
	var values, breakdowns []GroupedValue
	for _, c := range cached {
		if !hasKeys(c.Groups, a.req.GroupBy) {
			continue
		}
		values = append(values, GroupedValue{Groups: c.Groups, Value: c.CurrentAggregation})
		breakdowns = append(breakdowns, c.PresentationBreakdowns...)
	}
	if len(values) > 0 || !a.req.HasPreviousSubscription {
		return values, breakdowns, nil
	}
	q := a.historyQuery(time.Second)
	rows, err := a.store.GroupedSum(a.ctx, q, a.req.GroupBy)
	if err != nil {
		return nil, nil, a.wrap("previous units", err)
	}
	values = groupedValues(rows)
	if len(a.req.PresentationBy) > 0 {
		b, err := a.store.GroupedSum(a.ctx, q, a.breakdownKeys())
		if err != nil {
			return nil, nil, a.wrap("previous units", err)
		}
		breakdowns = groupedValues(b)
	}
	return values, breakdowns, nil
}

func hasKeys(g Groups, keys []string) bool {
	for _, k := range keys {
		if _, ok := g[k]; !ok {
			return false
		}
	}
	return true
}

func (a *aggregator) groupedWeightedSum() (Result, error) {
	if a.bypass() {
		return nullGrouped(a.req.GroupBy), nil
	}
	q := a.query()
	recurring := a.req.Metric.Recurring
	var latest, latestBreakdowns []GroupedValue
	if recurring {
		var err error
		if latest, latestBreakdowns, err = a.groupedLatest(); err != nil {
			return Result{}, err
		}
	}
	rows, err := a.store.GroupedWeightedSum(a.ctx, q, a.req.GroupBy, firstPerGroup(latest))
	if err != nil {
		return Result{}, a.wrap("weighted sum", err)
	}
	if len(rows) == 0 {
		return nullGrouped(a.req.GroupBy), nil
	}
	var lastEvents []GroupedEvent
	if recurring {
		if lastEvents, err = a.store.GroupedLastEvent(a.ctx, q, a.req.GroupBy); err != nil {
			return Result{}, a.wrap("last events", err)
		}
	}
	var r Result
	if len(a.req.PresentationBy) > 0 {
		b, err := a.store.GroupedWeightedSum(a.ctx, q, a.breakdownKeys(), latestBreakdowns)
		if err != nil {
			return Result{}, a.wrap("weighted sum breakdowns", err)
		}
		for _, row := range b {
			r.Breakdowns = append(r.Breakdowns, GroupedValue{Groups: row.Groups, Value: row.Value})
		}
		if len(r.Breakdowns) == 0 {
			r.Breakdowns = latestBreakdowns
		}
	}
	for _, row := range rows {
		g := Result{
			Groups:               row.Groups,
			Aggregation:          row.Value,
			Count:                money.NewFromInt(row.Count),
			Variation:            ptr(row.Variation),
			TotalAggregatedUnits: ptr(row.Variation),
		}
		if recurring {
			initial := money.Zero
			for _, l := range latest {
				if l.Groups.Equal(row.Groups) {
					initial = l.Value
					break
				}
			}
			g.TotalAggregatedUnits = ptr(initial.Add(row.Variation))
			at := a.req.From
			for _, e := range lastEvents {
				if e.Groups.Equal(row.Groups) {
					at = e.Timestamp
					break
				}
			}
			g.RecurringUpdatedAt = &at
		}
		r.Grouped = append(r.Grouped, g)
	}
	return r, nil
}

// firstPerGroup keeps the first value of each group.
func firstPerGroup(values []GroupedValue) []GroupedValue {
	var out []GroupedValue
	for _, v := range values {
		seen := false
		for _, o := range out {
			if o.Groups.Equal(v.Groups) {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, v)
		}
	}
	return out
}
