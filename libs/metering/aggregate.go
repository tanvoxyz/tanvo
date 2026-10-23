package metering

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// AggregationType is how a billable metric aggregates its events.
type AggregationType string

// Aggregation types.
const (
	// AggregationCount counts the events.
	AggregationCount AggregationType = "count"
	// AggregationSum sums the property.
	AggregationSum AggregationType = "sum"
	// AggregationMax takes the largest value of the property.
	AggregationMax AggregationType = "max"
	// AggregationUniqueCount counts the distinct values of the property,
	// following add and remove operations.
	AggregationUniqueCount AggregationType = "unique_count"
	// AggregationWeightedSum sums the property weighted by the time each
	// running total holds.
	AggregationWeightedSum AggregationType = "weighted_sum"
	// AggregationLatest takes the value of the most recent event.
	AggregationLatest AggregationType = "latest"
)

// RoundingFunction rounds aggregated units.
type RoundingFunction string

// Rounding functions.
const (
	RoundingRound RoundingFunction = "round"
	RoundingCeil  RoundingFunction = "ceil"
	RoundingFloor RoundingFunction = "floor"
)

// Metric is the part of a billable metric the aggregations read.
type Metric struct {
	Code        string          `json:"code"`
	Aggregation AggregationType `json:"aggregation"`
	// FieldName is the aggregated property (unused by count).
	FieldName string `json:"field_name,omitempty"`
	// Recurring metrics carry their usage over from one period to the
	// next.
	Recurring bool `json:"recurring,omitempty"`
	// RoundingFunction, when set, rounds the units at RoundingPrecision
	// decimal places (negative for tens, hundreds…).
	RoundingFunction  RoundingFunction `json:"rounding_function,omitempty"`
	RoundingPrecision int32            `json:"rounding_precision,omitempty"`
}

// Round rounds units with the rounding of the metric.
func (m Metric) Round(units money.Decimal) money.Decimal {
	switch m.RoundingFunction {
	case RoundingCeil:
		return units.Round(m.RoundingPrecision, money.Ceiling)
	case RoundingFloor:
		return units.Round(m.RoundingPrecision, money.Floor)
	case RoundingRound:
		return units.Round(m.RoundingPrecision, money.HalfUp)
	default:
		return units
	}
}

// Request is one aggregation of the events of a charge (or of one charge
// filter, or one pricing group) over a billing period.
type Request struct {
	Metric         Metric `json:"metric"`
	OrganizationID string `json:"organization_id,omitempty"`
	// SubscriptionID is the external subscription ID.
	SubscriptionID string `json:"subscription_id,omitempty"`

	// From and To bound the charges period, both included.
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// PeriodDays is the full duration of the charges period in days,
	// which prorated and time-weighted values are measured against.
	PeriodDays int `json:"period_days,omitempty"`
	// DaysTo is the end the billed days are counted to, for the persisted
	// units of prorated charges and for pay-in-advance events: the
	// termination instant when the subscription was terminated at the end
	// of this period, so that a termination at local midnight bills the day
	// it ends on (lago Utils::Datetime.date_diff_with_timezone, whose
	// to_datetime is terminated_at). Zero means To.
	DaysTo time.Time `json:"days_to,omitzero"`
	// Location is the customer's time zone. Nil means UTC.
	Location *time.Location `json:"-"`
	// MaxTimestamp, when set, caps the events read (the timestamp of the
	// pay-in-advance event being billed).
	MaxTimestamp time.Time `json:"max_timestamp,omitzero"`
	// TerminatedByUpgrade reports that the subscription ends with this
	// period because it was upgraded: the last day is then not billed to
	// the persisted units of prorated charges.
	TerminatedByUpgrade bool `json:"terminated_by_upgrade,omitempty"`
	// HasPreviousSubscription reports that the subscription replaces
	// another one with the same external ID, whose events carry recurring
	// usage over.
	HasPreviousSubscription bool `json:"has_previous_subscription,omitempty"`

	// Filter narrows the events to a charge filter (see
	// [ChargeFilterScope]).
	Filter Filter `json:"filter,omitzero"`
	// GroupBy are the pricing group keys: the result is then computed per
	// group in [Result.Grouped].
	GroupBy []string `json:"group_by,omitempty"`
	// GroupValues narrow the events to one pricing group.
	GroupValues Groups `json:"group_values,omitempty"`
	// PresentationBy are keys the units are broken down by for display.
	PresentationBy []string `json:"presentation_by,omitempty"`

	// Prorated charges bill units for the share of the period they are
	// used (sum and unique count).
	Prorated bool `json:"prorated,omitempty"`
	// PayInAdvance charges bill each event when it is received.
	PayInAdvance bool `json:"pay_in_advance,omitempty"`
	// Dynamic charges bill the amounts carried by the events (sum only).
	Dynamic bool `json:"dynamic,omitempty"`
	// CurrentUsage computes the usage of the period so far instead of the
	// usage to bill.
	CurrentUsage bool `json:"current_usage,omitempty"`

	// Event is the pay-in-advance event being billed.
	Event *Event `json:"event,omitempty"`
	// EventEstimate reports that Event is not stored, as when estimating
	// the fees of an event: it is then added to the stored ones.
	EventEstimate bool `json:"event_estimate,omitempty"`

	// FreeUnitsPerEvents and FreeUnitsPerTotalAggregation configure the
	// free units of percentage charges, which need the running totals.
	FreeUnitsPerEvents           int           `json:"free_units_per_events,omitempty"`
	FreeUnitsPerTotalAggregation money.Decimal `json:"free_units_per_total_aggregation,omitzero"`

	// BypassAggregation skips reading the events of a non-recurring metric
	// known to have none in the period.
	BypassAggregation bool `json:"bypass_aggregation,omitempty"`

	// Cache holds the cached aggregations of the charge; nil means none.
	Cache CachedAggregations `json:"-"`

	// UsageBuckets lets [Aggregate] answer from the usage buckets of a
	// store that is also a [BucketReader], for a charge billing found
	// servable (see [BucketServable]): count, sum, max and latest
	// aggregations of non-recurring metrics, not prorated, not billed in
	// advance. The answer is the one of the events; buckets only make it
	// cheaper.
	UsageBuckets bool `json:"usage_buckets,omitempty"`
}

// Result is the outcome of an aggregation. With [Request.GroupBy], the
// per-group results are in Grouped and the top-level units are unset.
type Result struct {
	// Groups are the pricing group values of a per-group result.
	Groups Groups `json:"groups,omitempty"`
	// Aggregation is the number of units to bill.
	Aggregation money.Decimal `json:"aggregation"`
	// Count is the number of events behind the units (the units for unique
	// counts).
	Count money.Decimal `json:"count"`
	// CurrentUsageUnits is the number of units used so far, without
	// proration.
	CurrentUsageUnits *money.Decimal `json:"current_usage_units,omitempty"`
	// FullUnitsNumber is the number of units without proration.
	FullUnitsNumber *money.Decimal `json:"full_units_number,omitempty"`
	// RunningTotal is the cumulative units event after event, as far as
	// the free units of percentage charges need.
	RunningTotal []money.Decimal `json:"running_total,omitempty"`

	// PreciseTotalAmountCents is the sum of the amounts of the events of
	// a dynamic charge.
	PreciseTotalAmountCents *money.Decimal `json:"precise_total_amount_cents,omitempty"`
	// PayInAdvancePreciseTotalAmountCents is the amount of the
	// pay-in-advance event of a dynamic charge.
	PayInAdvancePreciseTotalAmountCents *money.Decimal `json:"pay_in_advance_precise_total_amount_cents,omitempty"`

	// Variation is the number of units added in the period by a
	// time-weighted sum.
	Variation *money.Decimal `json:"variation,omitempty"`
	// TotalAggregatedUnits is the number of units held at the end of the
	// period by a time-weighted sum.
	TotalAggregatedUnits *money.Decimal `json:"total_aggregated_units,omitempty"`
	// RecurringUpdatedAt is when the units of a recurring time-weighted sum
	// last changed.
	RecurringUpdatedAt *time.Time `json:"recurring_updated_at,omitempty"`

	// PayInAdvanceAggregation is the number of units the pay-in-advance
	// event adds to bill.
	PayInAdvanceAggregation *money.Decimal `json:"pay_in_advance_aggregation,omitempty"`
	// CurrentAggregation, MaxAggregation, MaxAggregationWithProration and
	// UnitsApplied are the state to cache after a pay-in-advance event.
	CurrentAggregation          *money.Decimal `json:"current_aggregation,omitempty"`
	MaxAggregation              *money.Decimal `json:"max_aggregation,omitempty"`
	MaxAggregationWithProration *money.Decimal `json:"max_aggregation_with_proration,omitempty"`
	UnitsApplied                *money.Decimal `json:"units_applied,omitempty"`

	// Breakdowns are the units per group of [Request.GroupBy] and
	// [Request.PresentationBy].
	Breakdowns []GroupedValue `json:"breakdowns,omitempty"`
	// PayInAdvanceBreakdowns are the units of the pay-in-advance event
	// with its groups.
	PayInAdvanceBreakdowns []GroupedValue `json:"pay_in_advance_breakdowns,omitempty"`

	// Grouped are the per-group results.
	Grouped []Result `json:"grouped,omitempty"`
}

// ErrNotImplemented reports an aggregation the metric does not support,
// such as billing a max, latest or time-weighted sum in advance.
var ErrNotImplemented = errors.New("metering: aggregation not implemented")

func ptr[T any](v T) *T { return &v }

func nullResult(groupBy []string) Result {
	r := Result{Aggregation: money.Zero, Count: money.Zero, CurrentUsageUnits: ptr(money.Zero)}
	if groupBy != nil {
		r.Groups = NilGroups(groupBy)
	}
	return r
}

func nullGrouped(groupBy []string) Result {
	return Result{Grouped: []Result{nullResult(groupBy)}}
}

// aggregator carries one aggregation request.
type aggregator struct {
	ctx   context.Context //nolint:containedctx // scoped to one call
	store Store
	req   Request
}

func newAggregator(ctx context.Context, store Store, req Request) *aggregator {
	return &aggregator{ctx: ctx, store: store, req: req}
}

// query returns the base query of the request.
func (a *aggregator) query() Query {
	r := a.req
	q := Query{
		OrganizationID: r.OrganizationID,
		SubscriptionID: r.SubscriptionID,
		Code:           r.Metric.Code,
		From:           r.From,
		To:             r.To,
		Until:          r.MaxTimestamp,
		Property:       r.Metric.FieldName,
		Filter:         r.Filter,
		GroupValues:    r.GroupValues,
		PeriodDays:     r.PeriodDays,
		Location:       r.Location,
	}
	if !r.MaxTimestamp.IsZero() && r.Event != nil && !r.EventEstimate {
		q.UntilTransactionID = r.Event.TransactionID
	}
	switch r.Metric.Aggregation {
	case AggregationSum, AggregationMax, AggregationLatest, AggregationWeightedSum:
		q.Numeric = true
	}
	if r.Prorated {
		q = a.proratedQuery(q)
	}
	return q
}

// historyQuery returns the query of the usage carried over from before the
// period: every earlier event, up to just before From.
func (a *aggregator) historyQuery(before time.Duration) Query {
	q := a.query()
	q.IncludeHistory = true
	q.To = a.req.From.Add(-before)
	q.Until, q.UntilTransactionID = time.Time{}, ""
	return q
}

func withGroups(q Query, groups Groups) Query {
	if groups != nil {
		q.GroupValues = groups
	}
	return q
}

func (a *aggregator) bypass() bool {
	return !a.req.Metric.Recurring && a.req.BypassAggregation
}

func (a *aggregator) grouped() bool { return len(a.req.GroupBy) > 0 }

// breakdownKeys returns the group keys followed by the presentation keys,
// without duplicates.
func (a *aggregator) breakdownKeys() []string {
	var keys []string
	for _, k := range slices.Concat(a.req.GroupBy, a.req.PresentationBy) {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return keys
}

func groupedValues(rows []GroupedAggregate) []GroupedValue {
	out := make([]GroupedValue, len(rows))
	for i, r := range rows {
		out[i] = GroupedValue{Groups: r.Groups, Value: r.Value}
	}
	return out
}

func (a *aggregator) eventHasProperties() bool {
	return a.req.Event != nil && len(a.req.Event.Properties) > 0
}

// eventValue returns the aggregated property of the event, zero when it is
// missing.
func (a *aggregator) eventValue() (money.Decimal, error) {
	if a.req.Event == nil {
		return money.Zero, nil
	}
	s, ok := a.req.Event.Properties[a.req.Metric.FieldName]
	if !ok {
		return money.Zero, nil
	}
	v, err := money.Parse(s)
	if err != nil {
		return money.Zero, fmt.Errorf("metering: event %q property %q: %w", a.req.Event.TransactionID, a.req.Metric.FieldName, err)
	}
	return v, nil
}

func (a *aggregator) payInAdvanceBreakdowns(v money.Decimal) []GroupedValue {
	if !a.eventHasProperties() {
		return nil
	}
	groups := Groups{}
	for _, k := range a.breakdownKeys() {
		if s, ok := a.req.Event.Properties[k]; ok {
			groups[k] = &s
		} else {
			groups[k] = nil
		}
	}
	return []GroupedValue{{Groups: groups, Value: v}}
}

func (a *aggregator) excludedTransaction() string {
	if a.req.Event == nil {
		return ""
	}
	return a.req.Event.TransactionID
}

// cachedInPeriod returns the latest cached aggregation of the period for
// the groups, the pay-in-advance event left out.
func (a *aggregator) cachedInPeriod(groups Groups) (*CachedAggregation, error) {
	if a.req.Cache == nil {
		return nil, nil //nolint:nilnil // no cache, no cached aggregation
	}
	c, err := a.req.Cache.LatestInPeriod(a.ctx, a.req.From, a.req.To, groups, a.excludedTransaction())
	if err != nil {
		return nil, fmt.Errorf("metering: cached aggregation: %w", err)
	}
	return c, nil
}

func (a *aggregator) cachedBefore() ([]CachedAggregation, error) {
	if a.req.Cache == nil {
		return nil, nil
	}
	c, err := a.req.Cache.Before(a.ctx, a.req.From)
	if err != nil {
		return nil, fmt.Errorf("metering: cached aggregations: %w", err)
	}
	return c, nil
}

// inAdvanceCurrentUsage sets the current usage of a pay-in-advance charge:
// the units already billed (the cached maximum) plus the variation since.
func (a *aggregator) inAdvanceCurrentUsage(total money.Decimal, r *Result) error {
	cached, err := a.cachedInPeriod(r.Groups)
	if err != nil {
		return err
	}
	r.Aggregation = total
	if cached != nil {
		current, err := cachedValue(cached.CurrentAggregation, "current_aggregation")
		if err != nil {
			return err
		}
		maxAgg, err := cachedValue(cached.MaxAggregation, "max_aggregation")
		if err != nil {
			return err
		}
		r.Aggregation = total.Sub(current).Add(maxAgg)
	}
	r.CurrentUsageUnits = ptr(money.Max(total, money.Zero))
	r.Aggregation = money.Max(r.Aggregation, money.Zero)
	return nil
}

func (a *aggregator) round(r *Result) {
	m := a.req.Metric
	if m.RoundingFunction == "" || a.req.Event != nil {
		return
	}
	r.Aggregation = m.Round(r.Aggregation)
	if r.FullUnitsNumber != nil {
		r.FullUnitsNumber = ptr(m.Round(*r.FullUnitsNumber))
	}
	if r.CurrentUsageUnits != nil {
		r.CurrentUsageUnits = ptr(m.Round(*r.CurrentUsageUnits))
	}
}

func (a *aggregator) roundAll(r *Result) {
	if a.grouped() {
		for i := range r.Grouped {
			a.round(&r.Grouped[i])
		}
		return
	}
	a.round(r)
}

// Aggregate aggregates the events of the request read from the store. With
// [Request.UsageBuckets] and a store that is also a [BucketReader], the
// whole buckets of the window are read from the usage buckets (see
// [UsageBucketStore]).
func Aggregate(ctx context.Context, store Store, req Request) (Result, error) {
	if br, ok := store.(BucketReader); ok && bucketRequest(req) {
		store = NewUsageBucketStore(store, br)
	}
	a := newAggregator(ctx, store, req)
	var (
		r   Result
		err error
	)
	m := req.Metric
	if req.Dynamic && (m.Aggregation != AggregationSum || req.Prorated) {
		return Result{}, fmt.Errorf("%w: dynamic %s charge", ErrNotImplemented, m.Aggregation)
	}
	switch m.Aggregation {
	case AggregationCount:
		r, err = a.count()
	case AggregationSum:
		if req.Prorated {
			r, err = a.proratedSum()
		} else {
			r, err = a.sum(nil, nil)
		}
	case AggregationMax, AggregationLatest, AggregationWeightedSum:
		if req.PayInAdvance && !req.CurrentUsage {
			return Result{}, fmt.Errorf("%w: %s charge billed in advance", ErrNotImplemented, m.Aggregation)
		}
		switch m.Aggregation {
		case AggregationMax:
			r, err = a.max()
		case AggregationLatest:
			r, err = a.latest()
		default:
			r, err = a.weightedSum()
		}
	case AggregationUniqueCount:
		if req.Prorated {
			r, err = a.proratedUniqueCount()
		} else {
			r, err = a.uniqueCount()
		}
	default:
		return Result{}, fmt.Errorf("%w: aggregation %q", ErrNotImplemented, m.Aggregation)
	}
	if err != nil {
		return Result{}, err
	}
	a.roundAll(&r)
	return r, nil
}

// countRunningTotal returns 1, 2… up to the units, when free units are
// configured.
func (a *aggregator) countRunningTotal(units money.Decimal) []money.Decimal {
	if a.req.FreeUnitsPerEvents == 0 && a.req.FreeUnitsPerTotalAggregation.IsZero() {
		return nil
	}
	n, _ := units.Round(0, money.Floor).Int64()
	out := make([]money.Decimal, 0, max(n, 0))
	for i := int64(1); i <= n; i++ {
		out = append(out, money.NewFromInt(i))
	}
	return out
}

// DateDiff returns the number of days from from to to in loc, a partial day
// counting as a full one and a to at midnight counting its day.
func DateDiff(from, to time.Time, loc *time.Location) int {
	if loc == nil {
		loc = time.UTC
	}
	if to.Equal(period.StartOfDay(to, loc)) {
		to = to.Add(time.Second)
	}
	_, fromOffset := from.In(loc).Zone()
	_, toOffset := to.In(loc).Zone()
	elapsed := to.Sub(from) - time.Duration(fromOffset-toOffset)*time.Second
	day := 24 * time.Hour
	days := elapsed / day
	if elapsed%day > 0 {
		days++
	}
	return int(days)
}

// billedDays returns the number of days of the period billed to persisted
// units: the last day of a period ended by an upgrade is billed by the new
// subscription.
func (a *aggregator) billedDays() int {
	n := DateDiff(a.req.From, a.daysTo(), a.req.Location)
	if a.req.TerminatedByUpgrade {
		n = max(n-1, 0)
	}
	return n
}

// daysTo returns the end billed days are counted to (see [Request.DaysTo]).
func (a *aggregator) daysTo() time.Time {
	if a.req.DaysTo.IsZero() {
		return a.req.To
	}
	return a.req.DaysTo
}

func (a *aggregator) period() (money.Decimal, error) {
	if a.req.PeriodDays <= 0 {
		return money.Zero, fmt.Errorf("metering: period of %d days", a.req.PeriodDays)
	}
	return money.NewFromInt(int64(a.req.PeriodDays)), nil
}

// persistedProRata is the share of the period billed to persisted units.
func (a *aggregator) persistedProRata() (money.Decimal, error) {
	period, err := a.period()
	if err != nil {
		return money.Zero, err
	}
	return money.NewFromInt(int64(a.billedDays())).Div(period), nil
}

func ceil5(d money.Decimal) money.Decimal { return d.Round(5, money.Ceiling) }

func (a *aggregator) wrap(what string, err error) error {
	return fmt.Errorf("metering: %s of %q: %w", what, a.req.Metric.Code, err)
}
