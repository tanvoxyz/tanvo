package metering

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// persistedQuery is the query of the units persisted from before the period
// by a prorated sum: every event up to just before From.
func (a *aggregator) persistedQuery() Query {
	return a.historyQuery(time.Microsecond)
}

// currentQuery is the query of the events of the period of a prorated sum.
func (a *aggregator) currentQuery() Query {
	q := a.query()
	q.IncludeHistory = false
	return q
}

// billsFullAmount reports a pay-in-advance charge billed at the end of the
// period, which bills its full units without proration.
func (a *aggregator) billsFullAmount() bool {
	return a.req.Event == nil && a.req.PayInAdvance && !a.req.CurrentUsage
}

func (a *aggregator) proratedSum() (Result, error) {
	if a.billsFullAmount() {
		return a.sum(nil, nil)
	}
	if a.grouped() {
		return a.groupedProratedSum()
	}
	days := a.billedDays()
	current, err := a.store.ProratedSum(a.ctx, a.currentQuery(), nil)
	if err != nil {
		return Result{}, a.wrap("prorated sum", err)
	}
	persisted, err := a.store.ProratedSum(a.ctx, a.persistedQuery(), &days)
	if err != nil {
		return Result{}, a.wrap("persisted prorated sum", err)
	}
	full := Total{Value: current.Value, Count: current.Count}
	if a.req.Metric.Recurring {
		full = Total{Value: full.Value.Add(persisted.Value), Count: full.Count + persisted.Count}
	}
	base, err := a.sum(&full, nil)
	if err != nil {
		return Result{}, err
	}
	a.round(&base)

	units := ceil5(persisted.ProratedValue.Add(current.ProratedValue))
	r := base
	if a.req.Event == nil {
		r.FullUnitsNumber = ptr(base.Aggregation)
	}
	if a.req.CurrentUsage {
		if err := a.proratedCurrentUsage(units, base, &r); err != nil {
			return Result{}, err
		}
	} else {
		r.Aggregation = units
	}
	piaa, err := a.proratedPayInAdvance(base, &r)
	if err != nil {
		return Result{}, err
	}
	r.PayInAdvanceAggregation = ptr(piaa)
	r.Count = base.Count
	if len(a.req.PresentationBy) > 0 {
		rows, err := a.store.GroupedSum(a.ctx, a.currentQuery(), a.breakdownKeys())
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
	return r, nil
}

// groupedProrated is a prorated value of a group with the full value
// behind it.
type groupedProrated struct {
	groups          Groups
	value, prorated money.Decimal
	count           int64
}

// mergeGroups sums the rows sharing groups, in order of first appearance.
func mergeGroups(rows ...[]GroupedProratedAggregate) []groupedProrated {
	var out []groupedProrated
	for _, list := range rows {
		for _, row := range list {
			i := -1
			for j := range out {
				if out[j].groups.Equal(row.Groups) {
					i = j
					break
				}
			}
			if i < 0 {
				out = append(out, groupedProrated{groups: row.Groups, value: money.Zero, prorated: money.Zero})
				i = len(out) - 1
			}
			out[i].value = out[i].value.Add(row.Value)
			out[i].prorated = out[i].prorated.Add(row.ProratedValue)
			out[i].count += row.Count
		}
	}
	return out
}

func findGroup(results []Result, groups Groups) (Result, bool) {
	for _, r := range results {
		if r.Groups.Equal(groups) {
			return r, true
		}
	}
	return Result{}, false
}

func (a *aggregator) groupedProratedSum() (Result, error) {
	days := a.billedDays()
	current, err := a.store.GroupedProratedSum(a.ctx, a.currentQuery(), a.req.GroupBy, nil)
	if err != nil {
		return Result{}, a.wrap("prorated sum", err)
	}
	persisted, err := a.store.GroupedProratedSum(a.ctx, a.persistedQuery(), a.req.GroupBy, &days)
	if err != nil {
		return Result{}, a.wrap("persisted prorated sum", err)
	}
	fullRows := mergeGroups(current)
	if a.req.Metric.Recurring {
		fullRows = mergeGroups(current, persisted)
	}
	full := make([]GroupedAggregate, 0, len(fullRows))
	for _, row := range fullRows {
		full = append(full, GroupedAggregate{Groups: row.groups, Value: row.value, Count: row.count})
	}
	base, err := a.sum(nil, full)
	if err != nil {
		return Result{}, err
	}
	a.roundAll(&base)

	rows := mergeGroups(persisted, current)
	if len(rows) == 0 {
		return nullGrouped(a.req.GroupBy), nil
	}
	r := base
	r.Grouped = nil
	for _, row := range rows {
		g, err := a.proratedGroup(base, row.groups, ceil5(row.prorated))
		if err != nil {
			return Result{}, err
		}
		r.Grouped = append(r.Grouped, g)
	}
	if len(a.req.PresentationBy) > 0 {
		b, err := a.store.GroupedSum(a.ctx, a.currentQuery(), a.breakdownKeys())
		if err != nil {
			return Result{}, a.wrap("sum breakdowns", err)
		}
		r.Breakdowns = groupedValues(b)
	}
	return r, nil
}

// proratedGroup builds the result of one group of a prorated aggregation
// from its prorated units and the full result of the group.
func (a *aggregator) proratedGroup(base Result, groups Groups, units money.Decimal) (Result, error) {
	full, ok := findGroup(base.Grouped, groups)
	if !ok {
		full = nullResult(nil)
		full.Groups = groups
	}
	g := Result{Groups: groups, FullUnitsNumber: ptr(full.Aggregation), Aggregation: units}
	if a.req.CurrentUsage {
		if err := a.proratedCurrentUsage(units, full, &g); err != nil {
			return Result{}, err
		}
	}
	g.Count = full.Count
	return g, nil
}

// proratedCurrentUsage sets the current usage of a prorated charge: the
// prorated units to bill and the full units used.
func (a *aggregator) proratedCurrentUsage(prorated money.Decimal, full Result, target *Result) error {
	cached, err := a.cachedInPeriod(target.Groups)
	if err != nil {
		return err
	}
	proRata, err := a.persistedProRata()
	if err != nil {
		return err
	}
	used := money.Zero
	if full.CurrentUsageUnits != nil {
		used = *full.CurrentUsageUnits
	}
	partial := proRata.LessThan(money.One)
	switch {
	case !a.req.PayInAdvance:
		target.Aggregation = prorated
		target.CurrentUsageUnits = ptr(full.Aggregation)
	case cached != nil && partial:
		target.CurrentUsageUnits = ptr(used)
		since := used.Sub(money.Max(cached.CurrentAggregation, money.Zero))
		target.Aggregation = ceil5(since.Mul(proRata)).Add(cached.MaxAggregationWithProration)
	case cached != nil:
		target.CurrentUsageUnits = ptr(used)
		target.Aggregation = used.Sub(money.Max(cached.CurrentAggregation, money.Zero)).Add(cached.MaxAggregationWithProration)
	case partial:
		target.Aggregation = prorated
		target.CurrentUsageUnits = ptr(used)
	default:
		target.Aggregation = full.Aggregation
		target.CurrentUsageUnits = ptr(used)
	}
	target.Aggregation = money.Max(target.Aggregation, money.Zero)
	target.CurrentUsageUnits = ptr(money.Max(*target.CurrentUsageUnits, money.Zero))
	return nil
}

// proratedPayInAdvance returns the prorated units a pay-in-advance event
// adds to bill: its full units for the share of the period left.
func (a *aggregator) proratedPayInAdvance(base Result, r *Result) (money.Decimal, error) {
	if !a.eventHasProperties() {
		return money.Zero, nil
	}
	full := money.Zero
	if base.PayInAdvanceAggregation != nil {
		full = *base.PayInAdvanceAggregation
	}
	r.FullUnitsNumber = ptr(full)
	r.UnitsApplied = base.UnitsApplied
	period, err := a.period()
	if err != nil {
		return money.Zero, err
	}
	days := DateDiff(a.req.Event.Timestamp, a.req.To, a.req.Location)
	v := ceil5(full.Mul(money.NewFromInt(int64(days)).Div(period)))

	r.MaxAggregation = base.MaxAggregation
	r.CurrentAggregation = base.CurrentAggregation
	cached, err := a.cachedInPeriod(a.req.GroupValues)
	if err != nil {
		return money.Zero, err
	}
	switch {
	case cached == nil:
		r.MaxAggregationWithProration = ptr(v)
	case base.MaxAggregation != nil && base.MaxAggregation.GreaterThan(cached.MaxAggregation):
		r.MaxAggregationWithProration = ptr(cached.MaxAggregationWithProration.Add(v))
	default:
		r.MaxAggregationWithProration = ptr(cached.MaxAggregationWithProration)
	}
	return v, nil
}

func (a *aggregator) proratedUniqueCount() (Result, error) {
	if a.grouped() {
		return a.groupedProratedUniqueCount()
	}
	base, err := a.uniqueCount()
	if err != nil {
		return Result{}, err
	}
	a.round(&base)
	if a.billsFullAmount() {
		return base, nil
	}
	q := a.uniqueQuery()
	n, err := a.store.ProratedUniqueCount(a.ctx, q)
	if err != nil {
		return Result{}, a.wrap("prorated unique count", err)
	}
	units := ceil5(n)
	r := base
	if a.req.Event == nil {
		r.FullUnitsNumber = ptr(base.Aggregation)
	}
	if a.req.CurrentUsage {
		if err := a.proratedCurrentUsage(units, base, &r); err != nil {
			return Result{}, err
		}
	} else {
		r.Aggregation = units
	}
	if len(a.req.PresentationBy) > 0 {
		rows, err := a.store.GroupedUniqueCount(a.ctx, q, a.breakdownKeys())
		if err != nil {
			return Result{}, a.wrap("unique count breakdowns", err)
		}
		r.Breakdowns = groupedValues(rows)
		full := money.Zero
		if base.PayInAdvanceAggregation != nil {
			full = *base.PayInAdvanceAggregation
		}
		r.PayInAdvanceBreakdowns = a.payInAdvanceBreakdowns(full)
	}
	piaa, err := a.proratedPayInAdvance(base, &r)
	if err != nil {
		return Result{}, err
	}
	r.PayInAdvanceAggregation = ptr(piaa)
	r.Count = r.Aggregation
	return r, nil
}

func (a *aggregator) groupedProratedUniqueCount() (Result, error) {
	base, err := a.groupedUniqueCount()
	if err != nil {
		return Result{}, err
	}
	a.roundAll(&base)
	if a.billsFullAmount() {
		return base, nil
	}
	q := a.uniqueQuery()
	rows, err := a.store.GroupedProratedUniqueCount(a.ctx, q, a.req.GroupBy)
	if err != nil {
		return Result{}, a.wrap("prorated unique count", err)
	}
	if len(rows) == 0 {
		return nullGrouped(a.req.GroupBy), nil
	}
	r := base
	r.Grouped = nil
	for _, row := range rows {
		g, err := a.proratedGroup(base, row.Groups, ceil5(row.Value))
		if err != nil {
			return Result{}, err
		}
		g.Count = g.Aggregation
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
