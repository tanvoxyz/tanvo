package metering

import (
	"github.com/tanvoxyz/tanvo/libs/money"
)

func (a *aggregator) count() (Result, error) {
	if a.grouped() {
		return a.groupedCount()
	}
	if a.bypass() {
		return nullResult(nil), nil
	}
	q := a.query()
	n, err := a.store.Count(a.ctx, q)
	if err != nil {
		return Result{}, a.wrap("count", err)
	}
	units := money.NewFromInt(n)
	r := Result{
		Aggregation:             units,
		Count:                   units,
		CurrentUsageUnits:       ptr(units),
		PayInAdvanceAggregation: ptr(money.One),
	}
	if len(a.req.PresentationBy) > 0 {
		rows, err := a.store.GroupedCount(a.ctx, q, a.breakdownKeys())
		if err != nil {
			return Result{}, a.wrap("count breakdowns", err)
		}
		r.Breakdowns = groupedValues(rows)
		r.PayInAdvanceBreakdowns = a.payInAdvanceBreakdowns(money.One)
	}
	r.RunningTotal = a.countRunningTotal(units)
	return r, nil
}

func (a *aggregator) groupedCount() (Result, error) {
	if a.bypass() {
		return nullGrouped(a.req.GroupBy), nil
	}
	q := a.query()
	rows, err := a.store.GroupedCount(a.ctx, q, a.req.GroupBy)
	if err != nil {
		return Result{}, a.wrap("count", err)
	}
	if len(rows) == 0 {
		return nullGrouped(a.req.GroupBy), nil
	}
	var r Result
	for _, row := range rows {
		r.Grouped = append(r.Grouped, Result{
			Groups:            row.Groups,
			Aggregation:       row.Value,
			Count:             row.Value,
			CurrentUsageUnits: ptr(row.Value),
			RunningTotal:      a.countRunningTotal(row.Value),
		})
	}
	if len(a.req.PresentationBy) > 0 {
		rows, err := a.store.GroupedCount(a.ctx, q, a.breakdownKeys())
		if err != nil {
			return Result{}, a.wrap("count breakdowns", err)
		}
		r.Breakdowns = groupedValues(rows)
	}
	return r, nil
}

// sumQuery is the query of a sum: a recurring sum reads every event since
// the subscription started.
func (a *aggregator) sumQuery() Query {
	q := a.query()
	q.IncludeHistory = a.req.Metric.Recurring
	return q
}

// sum aggregates a sum, from the given aggregates when they are already
// known.
func (a *aggregator) sum(precomputed *Total, precomputedGrouped []GroupedAggregate) (Result, error) {
	if a.grouped() {
		return a.groupedSum(precomputedGrouped)
	}
	if a.bypass() {
		return nullResult(nil), nil
	}
	q := a.sumQuery()
	var total Total
	if precomputed != nil {
		total = *precomputed
	} else {
		var err error
		if total, err = a.store.Sum(a.ctx, q); err != nil {
			return Result{}, a.wrap("sum", err)
		}
	}
	r := Result{Aggregation: total.Value, Count: money.NewFromInt(total.Count)}
	if a.req.PayInAdvance && a.req.CurrentUsage {
		if err := a.inAdvanceCurrentUsage(total.Value, &r); err != nil {
			return Result{}, err
		}
	}
	piaa, err := a.sumPayInAdvance(&r)
	if err != nil {
		return Result{}, err
	}
	r.PayInAdvanceAggregation = ptr(piaa)
	if len(a.req.PresentationBy) > 0 {
		rows, err := a.store.GroupedSum(a.ctx, q, a.breakdownKeys())
		if err != nil {
			return Result{}, a.wrap("sum breakdowns", err)
		}
		r.Breakdowns = groupedValues(rows)
		v, err := a.eventValue()
		if err != nil {
			return Result{}, err
		}
		r.PayInAdvanceBreakdowns = a.payInAdvanceBreakdowns(v)
	}
	if r.RunningTotal, err = a.sumRunningTotal(nil); err != nil {
		return Result{}, err
	}
	if a.req.Dynamic {
		amount, err := a.store.SumPreciseTotalAmountCents(a.ctx, q)
		if err != nil {
			return Result{}, a.wrap("sum of amounts", err)
		}
		r.PreciseTotalAmountCents = ptr(amount)
		event := money.Zero
		if a.req.Event != nil && a.req.Event.PreciseTotalAmountCents != nil {
			event = *a.req.Event.PreciseTotalAmountCents
		}
		r.PayInAdvancePreciseTotalAmountCents = ptr(event)
	}
	return r, nil
}

func (a *aggregator) groupedSum(precomputed []GroupedAggregate) (Result, error) {
	if a.bypass() {
		return nullGrouped(a.req.GroupBy), nil
	}
	q := a.sumQuery()
	rows := precomputed
	if rows == nil {
		var err error
		if rows, err = a.store.GroupedSum(a.ctx, q, a.req.GroupBy); err != nil {
			return Result{}, a.wrap("sum", err)
		}
	}
	if len(rows) == 0 {
		return nullGrouped(a.req.GroupBy), nil
	}
	var r Result
	if len(a.req.PresentationBy) > 0 {
		b, err := a.store.GroupedSum(a.ctx, q, a.breakdownKeys())
		if err != nil {
			return Result{}, a.wrap("sum breakdowns", err)
		}
		r.Breakdowns = groupedValues(b)
	}
	for _, row := range rows {
		g := Result{Groups: row.Groups, Aggregation: row.Value, Count: money.NewFromInt(row.Count)}
		if a.req.PayInAdvance && a.req.CurrentUsage {
			if err := a.inAdvanceCurrentUsage(row.Value, &g); err != nil {
				return Result{}, err
			}
		}
		var err error
		if g.RunningTotal, err = a.sumRunningTotal(row.Groups); err != nil {
			return Result{}, err
		}
		r.Grouped = append(r.Grouped, g)
	}
	if a.req.Dynamic {
		amounts, err := a.store.GroupedSumPreciseTotalAmountCents(a.ctx, q, a.req.GroupBy)
		if err != nil {
			return Result{}, a.wrap("sum of amounts", err)
		}
		for _, amount := range amounts {
			for i := range r.Grouped {
				if r.Grouped[i].Groups.Equal(amount.Groups) {
					r.Grouped[i].PreciseTotalAmountCents = ptr(amount.Value)
					break
				}
			}
		}
	}
	return r, nil
}

// sumRunningTotal returns the running total of the event values as far as
// the free units need: the first FreeUnitsPerEvents values, or the values
// until the total exceeds FreeUnitsPerTotalAggregation.
func (a *aggregator) sumRunningTotal(groups Groups) ([]money.Decimal, error) {
	perEvents, perTotal := a.req.FreeUnitsPerEvents, a.req.FreeUnitsPerTotalAggregation
	if perEvents == 0 && perTotal.IsZero() {
		return nil, nil
	}
	q := withGroups(a.sumQuery(), groups)
	limit := 0
	if perEvents != 0 {
		limit = perEvents
	}
	values, err := a.store.EventValues(a.ctx, q, limit, "")
	if err != nil {
		return nil, a.wrap("event values", err)
	}
	if a.req.Event != nil && a.req.EventEstimate && (perEvents == 0 || len(values) < perEvents) {
		v, err := a.eventValue()
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	var out []money.Decimal
	total := money.Zero
	for _, v := range values {
		if perEvents == 0 && perTotal.LessThan(total) {
			break
		}
		total = total.Add(v)
		out = append(out, total)
	}
	return out, nil
}

// sumPayInAdvance returns the units a pay-in-advance event adds to bill:
// the increase of the running total over the highest total already billed.
func (a *aggregator) sumPayInAdvance(r *Result) (money.Decimal, error) {
	if !a.eventHasProperties() {
		return money.Zero, nil
	}
	v, err := a.eventValue()
	if err != nil {
		return money.Zero, err
	}
	cached, err := a.cachedInPeriod(a.req.GroupValues)
	if err != nil {
		return money.Zero, err
	}
	if cached == nil {
		r.CurrentAggregation, r.MaxAggregation, r.UnitsApplied = ptr(v), ptr(v), ptr(v)
		return money.Max(v, money.Zero), nil
	}
	cur, err := cachedValue(cached.CurrentAggregation, "current_aggregation")
	if err != nil {
		return money.Zero, err
	}
	oldMax, err := cachedValue(cached.MaxAggregation, "max_aggregation")
	if err != nil {
		return money.Zero, err
	}
	current := cur.Add(v)
	r.CurrentAggregation = ptr(current)
	if current.GreaterThan(oldMax) {
		r.MaxAggregation = ptr(money.Max(current, current.Sub(oldMax)))
		return current.Sub(oldMax), nil
	}
	r.MaxAggregation, r.UnitsApplied = ptr(oldMax), ptr(v)
	return money.Zero, nil
}

func (a *aggregator) max() (Result, error) {
	q := a.query()
	if a.grouped() {
		if a.bypass() {
			return nullGrouped(a.req.GroupBy), nil
		}
		rows, err := a.store.GroupedMax(a.ctx, q, a.req.GroupBy)
		if err != nil {
			return Result{}, a.wrap("max", err)
		}
		if len(rows) == 0 {
			return nullGrouped(a.req.GroupBy), nil
		}
		var r Result
		for _, row := range rows {
			r.Grouped = append(r.Grouped, Result{Groups: row.Groups, Aggregation: row.Value, Count: money.NewFromInt(row.Count)})
		}
		if r.Breakdowns, err = a.maxBreakdowns(q); err != nil {
			return Result{}, err
		}
		return r, nil
	}
	if a.bypass() {
		return nullResult(nil), nil
	}
	m, err := a.store.Max(a.ctx, q)
	if err != nil {
		return Result{}, a.wrap("max", err)
	}
	r := Result{Aggregation: m.Value, Count: money.NewFromInt(m.Count)}
	if r.Breakdowns, err = a.maxBreakdowns(q); err != nil {
		return Result{}, err
	}
	return r, nil
}

func (a *aggregator) maxBreakdowns(q Query) ([]GroupedValue, error) {
	if len(a.req.PresentationBy) == 0 {
		return nil, nil
	}
	rows, err := a.store.GroupedMax(a.ctx, q, a.breakdownKeys())
	if err != nil {
		return nil, a.wrap("max breakdowns", err)
	}
	return groupedValues(rows), nil
}

func (a *aggregator) latest() (Result, error) {
	q := a.query()
	if a.grouped() {
		if a.bypass() {
			return nullGrouped(a.req.GroupBy), nil
		}
		rows, err := a.store.GroupedLast(a.ctx, q, a.req.GroupBy, a.req.GroupBy)
		if err != nil {
			return Result{}, a.wrap("latest", err)
		}
		if len(rows) == 0 {
			return nullGrouped(a.req.GroupBy), nil
		}
		var r Result
		for _, row := range rows {
			r.Grouped = append(r.Grouped, Result{
				Groups:      row.Groups,
				Aggregation: money.Max(row.Value, money.Zero),
				Count:       money.NewFromInt(row.Count),
			})
		}
		if r.Breakdowns, err = a.latestBreakdowns(q); err != nil {
			return Result{}, err
		}
		return r, nil
	}
	if a.bypass() {
		return nullResult(nil), nil
	}
	last, err := a.store.Last(a.ctx, q)
	if err != nil {
		return Result{}, a.wrap("latest", err)
	}
	v := money.Zero
	if last.Value != nil {
		v = money.Max(*last.Value, money.Zero)
	}
	r := Result{Aggregation: v, Count: money.NewFromInt(last.Count)}
	if r.Breakdowns, err = a.latestBreakdowns(q); err != nil {
		return Result{}, err
	}
	return r, nil
}

func (a *aggregator) latestBreakdowns(q Query) ([]GroupedValue, error) {
	if len(a.req.PresentationBy) == 0 {
		return nil, nil
	}
	rows, err := a.store.GroupedLast(a.ctx, q, a.req.GroupBy, a.breakdownKeys())
	if err != nil {
		return nil, a.wrap("latest breakdowns", err)
	}
	return groupedValues(rows), nil
}

// uniqueQuery is the query of a unique count: a recurring count reads every
// event since the subscription started.
func (a *aggregator) uniqueQuery() Query {
	q := a.query()
	q.IncludeHistory = a.req.Metric.Recurring
	return q
}

func (a *aggregator) uniqueCount() (Result, error) {
	if a.grouped() {
		return a.groupedUniqueCount()
	}
	if a.bypass() {
		return nullResult(nil), nil
	}
	q := a.uniqueQuery()
	n, err := a.store.UniqueCount(a.ctx, q)
	if err != nil {
		return Result{}, a.wrap("unique count", err)
	}
	units := ceil5(n)
	r := Result{Aggregation: units}
	if a.req.PayInAdvance && a.req.CurrentUsage {
		if err := a.inAdvanceCurrentUsage(units, &r); err != nil {
			return Result{}, err
		}
	}
	piaa, err := a.uniquePayInAdvance(q, &r)
	if err != nil {
		return Result{}, err
	}
	r.PayInAdvanceAggregation = ptr(piaa)
	if len(a.req.PresentationBy) > 0 {
		rows, err := a.store.GroupedUniqueCount(a.ctx, q, a.breakdownKeys())
		if err != nil {
			return Result{}, a.wrap("unique count breakdowns", err)
		}
		r.Breakdowns = groupedValues(rows)
		r.PayInAdvanceBreakdowns = a.payInAdvanceBreakdowns(piaa)
	}
	r.RunningTotal = a.countRunningTotal(units)
	r.Count = r.Aggregation
	return r, nil
}

func (a *aggregator) groupedUniqueCount() (Result, error) {
	if a.bypass() {
		return nullGrouped(a.req.GroupBy), nil
	}
	q := a.uniqueQuery()
	rows, err := a.store.GroupedUniqueCount(a.ctx, q, a.req.GroupBy)
	if err != nil {
		return Result{}, a.wrap("unique count", err)
	}
	if len(rows) == 0 {
		return nullGrouped(a.req.GroupBy), nil
	}
	var r Result
	for _, row := range rows {
		g := Result{Groups: row.Groups, Aggregation: row.Value}
		if a.req.PayInAdvance && a.req.CurrentUsage {
			if err := a.inAdvanceCurrentUsage(row.Value, &g); err != nil {
				return Result{}, err
			}
		}
		g.Count = row.Value
		g.RunningTotal = a.countRunningTotal(g.Aggregation)
		r.Grouped = append(r.Grouped, g)
	}
	if len(a.req.PresentationBy) > 0 {
		b, err := a.store.GroupedUniqueCount(a.ctx, q, a.breakdownKeys())
		if err != nil {
			return Result{}, a.wrap("unique count breakdowns", err)
		}
		r.Breakdowns = groupedValues(b)
	}
	return r, nil
}

// uniquePayInAdvance returns the units a pay-in-advance event adds to bill:
// 1 when it adds a value absent so far and the count exceeds the highest
// count already billed.
func (a *aggregator) uniquePayInAdvance(q Query, r *Result) (money.Decimal, error) {
	if !a.eventHasProperties() {
		return money.Zero, nil
	}
	e := *a.req.Event
	active, err := a.store.ActiveUniqueProperty(a.ctx, q, e)
	if err != nil {
		return money.Zero, a.wrap("active unique property", err)
	}
	add := e.Operation() == OperationAdd
	newly := money.Zero
	if add && !active {
		newly = money.One
	}
	cached, err := a.cachedInPeriod(a.req.GroupValues)
	if err != nil {
		return money.Zero, err
	}
	if cached == nil {
		r.CurrentAggregation, r.MaxAggregation, r.UnitsApplied = ptr(newly), ptr(newly), ptr(newly)
		return newly, nil
	}
	cur, err := cachedValue(cached.CurrentAggregation, "current_aggregation")
	if err != nil {
		return money.Zero, err
	}
	oldMax, err := cachedValue(cached.MaxAggregation, "max_aggregation")
	if err != nil {
		return money.Zero, err
	}
	current := cur.Add(newly)
	if !add {
		current = cur
		if active {
			current = current.Sub(money.One)
		}
	}
	r.CurrentAggregation = ptr(current)
	if current.GreaterThan(oldMax) {
		r.MaxAggregation = ptr(current)
		return money.One, nil
	}
	r.MaxAggregation, r.UnitsApplied = ptr(oldMax), ptr(newly)
	return money.Zero, nil
}
