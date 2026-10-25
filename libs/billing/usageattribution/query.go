package usageattribution

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/attribution"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// The limits of an attributed usage query.
//
// source: app/services/usage_attributions/query_service.rb:14-22
const (
	// DefaultLimit is the default number of rows of a page.
	DefaultLimit = 50
	// MaxLimit is the most rows of a page.
	MaxLimit = 100
	// MaxFilterValues is the most values of one label filter.
	MaxFilterValues = 20
	// MaxFlatFilters is the most filters on flat types.
	MaxFlatFilters = 3
	// MaxSplitFilters is the most filters of a split charge.
	MaxSplitFilters = 10
	// MaxSearchLength is the longest search, in characters.
	MaxSearchLength = 100
	// MaxCustomWindow is the longest window.
	MaxCustomWindow = 31 * 24 * time.Hour
	// MaxGroups is the most values a query groups.
	MaxGroups = 250_000
	// MaxExecutionTime is how long a query may run.
	MaxExecutionTime = 8 * time.Second
)

// Resources of the not-found errors of [QueryService.Query].
const (
	// ResourceCustomer is the customer.
	ResourceCustomer = "customer"
	// ResourceSubscription is the subscription.
	ResourceSubscription = "subscription"
	// ResourceCharge is a charge selected by code.
	ResourceCharge = "charge"
	// ResourceType is an attribution type grouped or filtered by.
	ResourceType = "usage_attribution_type"
)

// Rule codes of [QueryService.Query]: the query's limits.
const (
	// CodeTooManyGroups is a query grouping more than [MaxGroups] values.
	CodeTooManyGroups = "too_many_groups"
	// CodeQueryTimeout is a query running past [MaxExecutionTime].
	CodeQueryTimeout = "query_timeout"
	// CodeMemoryLimitExceeded is a query past the store's memory limit.
	CodeMemoryLimitExceeded = "memory_limit_exceeded"
	// CodeNotStarted is a subscription without a current billing period.
	CodeNotStarted = "not_started"
)

// Validation codes of [QueryService.Query].
const (
	CodeParentFilterRequired       = "parent_filter_required"
	CodeValuesAreRequired          = "values_are_required"
	CodeTooManyValues              = "too_many_values"
	CodeTooManyFlatFilters         = "too_many_flat_filters"
	CodeBothBoundariesAreRequired  = "both_boundaries_are_required"
	CodeInvalidDateRange           = "invalid_date_range"
	CodeWindowTooLong              = "window_too_long"
	CodeValueIsOutOfRange          = "value_is_out_of_range"
	CodeValueIsTooLong             = "value_is_too_long"
	CodeValueIsInvalid             = "value_is_invalid"
	CodeMustNotBeEmpty             = "must_not_be_empty"
	CodeUnsupportedAggregationType = "unsupported_aggregation_type"
	CodeMustBeSelected             = "must_be_selected"
	CodeMustHaveFilters            = "must_have_filters"
	CodeTooManyFilters             = "too_many_filters"
)

// ErrUnavailable is an attributed usage query for an organization whose
// event store does not answer it: events carry attribution labels in the
// ClickHouse store only (lago forbidden_failure).
var ErrUnavailable = errors.New("usageattribution: attributed usage needs the ClickHouse event store")

// Basis is what attributed usage ranks by and whether it prices.
type Basis string

// The bases.
const (
	// BasisUnits ranks by events and gives no amounts.
	BasisUnits Basis = "units"
	// BasisAmount ranks by amount and prices the standard charges.
	BasisAmount Basis = "amount"
)

// QueryService answers attributed usage queries (parity §1.42).
type QueryService struct {
	d      *kernel.Deps
	events metering.AttributedUsageReader

	// maxGroups and maxLookupEntries are the limits of the query and of
	// the price lookups (tests lower them).
	maxGroups        int
	maxLookupEntries int
}

// NewQuery returns the query service over d and the event store events.
// It uses d.Tx and d.Clock.
func NewQuery(d *kernel.Deps, events metering.AttributedUsageReader) (*QueryService, error) {
	switch {
	case d == nil, d.Tx == nil:
		return nil, fmt.Errorf("%w: Tx", kernel.ErrMissingDependency)
	case d.Clock == nil:
		return nil, fmt.Errorf("%w: Clock", kernel.ErrMissingDependency)
	case events == nil:
		return nil, fmt.Errorf("%w: attributed usage reader", kernel.ErrMissingDependency)
	}
	return &QueryService{d: d, events: events, maxGroups: MaxGroups, maxLookupEntries: MaxLookupEntries}, nil
}

// QueryInput is an attributed usage query.
type QueryInput struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// CustomerID or CustomerExternalID is the customer.
	CustomerID         core.CustomerID
	CustomerExternalID string
	// SubscriptionID or SubscriptionExternalID is one of the customer's
	// active subscriptions.
	SubscriptionID         core.SubscriptionID
	SubscriptionExternalID string
	// GroupBy is the code of the attribution type grouped by.
	GroupBy string
	// Filters keep the events whose label of each type (by code) is one of
	// the values; "" matches the events without the label.
	Filters map[string][]string
	// From and To are the window, To exclusive; both or neither are set.
	// Neither reads the current billing period.
	From, To *time.Time
	// ChargeCodes are the charges read, by code; nil reads every charge
	// and an empty selection is invalid.
	ChargeCodes []string
	// SplitChargeCode is the charge split by its filters; "" splits none.
	SplitChargeCode string
	// Search keeps the values containing it, case-insensitively.
	Search string
	// Basis is the basis; "" is [BasisUnits].
	Basis Basis
	// Limit and Offset are the page over the ranked values; a zero Limit
	// is [DefaultLimit].
	Limit, Offset int
}

// Cell is the usage of one charge, or of one filter of a split charge.
type Cell struct {
	// Charge is the charge and Metric its billable metric.
	Charge *catalog.Charge
	Metric *catalog.BillableMetric
	// Filter is the filter of a split charge; nil otherwise and for the
	// events of a split charge no filter matches.
	Filter *catalog.ChargeFilter
	// Units are the units.
	Units money.Decimal
	// Amount is the amount in minor units; nil when the cell is not priced.
	Amount *money.Decimal
	// Events is the number of events.
	Events int64
}

// Aggregate is usage over every cell.
type Aggregate struct {
	// Amount is the amount in minor units; nil on the units basis.
	Amount *money.Decimal
	// Events is the number of events.
	Events int64
	// Cells are the usage per charge (and split filter), in the order of
	// the charges, a split charge's filters in their order and its
	// default last.
	Cells []Cell
}

// Row is the usage of one attribution value.
type Row struct {
	// Value is the value.
	Value string
	// Rank is its rank among every value, from 1.
	Rank int
	Aggregate
}

// Result is the answer of an attributed usage query.
type Result struct {
	// Subscription is the subscription.
	Subscription *subscription.Subscription
	// GroupBy is the code of the type grouped by.
	GroupBy string
	// Basis is the basis.
	Basis Basis
	// From and To are the window read, To exclusive.
	From, To time.Time
	// Currency is the plan's currency.
	Currency money.Currency
	// Rows are the values of the page, by rank.
	Rows []Row
	// Unattributed is the usage of the events without a value.
	Unattributed Aggregate
	// Totals is the usage of every value and of the unattributed events.
	Totals Aggregate
	// Groups is the number of values.
	Groups int64
	// Limit and Offset are the page.
	Limit, Offset int
}

// plan is what the query reads, loaded in the transaction.
type plan struct {
	sub      *subscription.Subscription
	currency money.Currency
	from, to time.Time
	charges  []*catalog.Charge
	metrics  map[core.BillableMetricID]*catalog.BillableMetric
	split    *catalog.Charge
	columns  []metering.AttributedCharge
}

// Query returns the usage of a customer's active subscription over a
// window, grouped by the values of the attribution type in.GroupBy:
// customer, subscription and selected charges are core.NotFound; an
// organization whose event store holds no labels is [ErrUnavailable];
// unknown types are core.NotFound(ResourceType); invalid parameters are a
// *core.ValidationError on the API's field names; the query's limits are
// rule errors ([CodeTooManyGroups], [CodeQueryTimeout],
// [CodeMemoryLimitExceeded]).
//
// The count and sum charges of the plan whose metric is not recurring
// are read, the standard ones priced on the amount basis; a priced or
// split charge with filters resolves the filter of each event with its
// [PriceLookup]. A level of the hierarchy whose parent type is kept is
// read under a filter on the parent.
//
// source: app/controllers/api/v1/customers/attributed_usage_controller.rb:13-43, app/services/usage_attributions/query_service.rb:53-81
func (s *QueryService) Query(ctx context.Context, in QueryInput) (*Result, error) {
	if in.Basis == "" {
		in.Basis = BasisUnits
	}
	if in.Limit == 0 {
		in.Limit = DefaultLimit
	}
	org := in.OrganizationID
	now := s.d.Now()
	var p plan
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		p, err = s.load(ctx, r, in, now)
		return err
	})
	if err != nil {
		return nil, err
	}

	res := &Result{
		Subscription: p.sub, GroupBy: in.GroupBy, Basis: in.Basis, From: p.from, To: p.to, Currency: p.currency,
		Limit: in.Limit, Offset: in.Offset,
	}
	order := metering.AttributedOrderEvents
	if in.Basis == BasisAmount {
		order = metering.AttributedOrderAmount
	}
	q := metering.AttributedQuery{
		OrganizationID: org.String(), SubscriptionID: p.sub.ExternalID,
		From: p.from, To: kernel.MeteringTo(p.to),
		GroupKey: in.GroupBy, LabelFilters: in.Filters, Charges: p.columns, Order: order,
		Search: in.Search, Limit: in.Limit, Offset: in.Offset,
		MaxGroups: s.maxGroups, MaxExecutionTime: MaxExecutionTime,
	}
	var u metering.AttributedUsage
	if len(p.columns) > 0 {
		// A plan may hold no count or sum charge: there is nothing to read.
		u, err = s.events.AttributedUsage(ctx, q)
		if err != nil {
			return nil, queryError(err)
		}
	}
	s.assemble(res, p, q, u)
	return res, nil
}

// queryError turns the failures of the query on its limits into rule
// errors.
//
// source: app/services/usage_attributions/query_service.rb:73-81
func queryError(err error) error {
	for target, code := range map[error]string{
		metering.ErrAttributedTooManyGroups: CodeTooManyGroups,
		metering.ErrAttributedTimeout:       CodeQueryTimeout,
		metering.ErrAttributedMemoryLimit:   CodeMemoryLimitExceeded,
	} {
		if errors.Is(err, target) {
			return core.NewRuleError(code, err.Error())
		}
	}
	return fmt.Errorf("attributed usage: %w", err)
}

// load reads the subscription, its charges and the types, checks the
// query and returns what it reads.
func (s *QueryService) load(ctx context.Context, r port.Repos, in QueryInput, now time.Time) (plan, error) {
	org := in.OrganizationID
	var p plan
	sub, err := s.subscription(ctx, r, in)
	if err != nil {
		return p, err
	}
	p.sub = sub
	// The controller's parameters (attributed_usage_controller.rb:55-62;
	// the date formats are the API's).
	if in.Limit < 1 || in.Limit > MaxLimit {
		var v core.Validator
		v.Add("page_size", CodeValueIsOutOfRange)
		return p, v.Err()
	}

	env, err := kernel.LoadSubscriptionEnvOf(ctx, r, sub)
	if err != nil {
		return p, err
	}
	p.currency = env.Plan.Currency
	all, err := env.Charges(ctx, r)
	if err != nil {
		return p, err
	}
	// The selected charges, in the plan (attributed_usage_controller.rb:87-106).
	var requested []*catalog.Charge
	if in.ChargeCodes != nil {
		requested = []*catalog.Charge{}
		var codes []string
		for _, code := range in.ChargeCodes {
			if !slices.Contains(codes, code) {
				codes = append(codes, code)
			}
		}
		for _, code := range codes {
			i := slices.IndexFunc(all, func(c *catalog.Charge) bool { return c.Code == code })
			if i < 0 {
				return p, core.NotFound(ResourceCharge)
			}
			requested = append(requested, all[i])
		}
	}
	if in.SplitChargeCode != "" {
		i := slices.IndexFunc(all, func(c *catalog.Charge) bool { return c.Code == in.SplitChargeCode })
		if i < 0 {
			return p, core.NotFound(ResourceCharge)
		}
		p.split = all[i]
	}

	if !s.events.AttributedUsageAvailable(org.String()) {
		return p, ErrUnavailable
	}
	// The subscription is active, hence started (lago's not_started and
	// not_active checks cannot fail on the API's active subscriptions).
	types, err := s.types(ctx, r, org, in)
	if err != nil {
		return p, err
	}

	ids := make([]core.BillableMetricID, 0, len(all))
	for _, c := range all {
		ids = append(ids, c.BillableMetricID)
	}
	metrics, err := r.Metrics.ListByIDs(ctx, org, ids)
	if err != nil {
		return p, fmt.Errorf("load billable metrics: %w", err)
	}
	p.metrics = map[core.BillableMetricID]*catalog.BillableMetric{}
	for _, m := range metrics {
		p.metrics[m.ID] = m
	}
	supported := func(c *catalog.Charge) bool {
		m := p.metrics[c.BillableMetricID]
		return m != nil && !m.Recurring && (m.AggregationType == catalog.AggregationTypeCount || m.AggregationType == catalog.AggregationTypeSum)
	}
	lookups := map[core.ChargeID]*metering.PriceLookup{}
	lookup := func(c *catalog.Charge) *metering.PriceLookup {
		l, ok := lookups[c.ID]
		if !ok {
			l = PriceLookup(c, p.metrics[c.BillableMetricID], p.currency, s.maxLookupEntries)
			lookups[c.ID] = l
		}
		return l
	}

	var v core.Validator
	s.validate(&v, in, sub, types, requested, p.split, supported, lookup)
	if err := v.Err(); err != nil {
		return p, err
	}

	p.from, p.to, err = window(env, in, now)
	if err != nil {
		return p, err
	}
	for _, c := range all {
		if supported(c) && (requested == nil || slices.Contains(requested, c)) {
			p.charges = append(p.charges, c)
		}
	}
	for _, c := range p.charges {
		p.columns = append(p.columns, s.column(c, p, in.Basis, lookup))
	}
	return p, nil
}

// subscription returns the customer's active subscription the input designates.
func (s *QueryService) subscription(ctx context.Context, r port.Repos, in QueryInput) (*subscription.Subscription, error) {
	org := in.OrganizationID
	var (
		c   *customer.Customer
		err error
	)
	if in.CustomerExternalID != "" {
		c, err = r.Customers.GetByExternalID(ctx, org, in.CustomerExternalID)
	} else {
		c, err = r.Customers.Get(ctx, org, in.CustomerID)
	}
	if err == nil && c.Deleted() {
		err = core.ErrNotFound
	}
	if errors.Is(err, core.ErrNotFound) {
		return nil, core.NotFound(ResourceCustomer)
	}
	if err != nil {
		return nil, fmt.Errorf("load customer: %w", err)
	}
	subs, err := r.Subscriptions.ListByCustomer(ctx, org, c.ID, subscription.StatusActive)
	if err != nil {
		return nil, fmt.Errorf("list active subscriptions of %s: %w", c.ID, err)
	}
	for _, sub := range subs {
		if (in.SubscriptionExternalID != "" && sub.ExternalID == in.SubscriptionExternalID) ||
			(in.SubscriptionExternalID == "" && sub.ID == in.SubscriptionID) {
			return sub, nil
		}
	}
	return nil, core.NotFound(ResourceSubscription)
}

// types returns the kept types of the group and of the filters by code,
// with the parent of the group's type (deleted or not) under "", or
// core.NotFound(ResourceType).
//
// source: app/services/usage_attributions/query_service.rb:96-102
func (s *QueryService) types(ctx context.Context, r port.Repos, org core.OrganizationID, in QueryInput) (map[string]*attribution.Type, error) {
	out := map[string]*attribution.Type{}
	for _, code := range append([]string{in.GroupBy}, slices.Sorted(keys(in.Filters))...) {
		if _, ok := out[code]; ok {
			continue
		}
		t, err := r.UsageAttributionTypes.GetByCode(ctx, org, code)
		if errors.Is(err, core.ErrNotFound) {
			return nil, core.NotFound(ResourceType)
		}
		if err != nil {
			return nil, fmt.Errorf("load usage attribution type %q: %w", code, err)
		}
		out[code] = t
	}
	if id := out[in.GroupBy].ParentID; !id.IsZero() {
		parents, err := r.UsageAttributionTypes.GetMany(ctx, org, []core.UsageAttributionTypeID{id})
		if err != nil {
			return nil, fmt.Errorf("load usage attribution type %s: %w", id, err)
		}
		out[""] = parents[id]
	}
	return out, nil
}

// validate checks the query.
//
// source: app/services/usage_attributions/query_service.rb:104-181
func (s *QueryService) validate(v *core.Validator, in QueryInput, sub *subscription.Subscription, types map[string]*attribution.Type,
	requested []*catalog.Charge, split *catalog.Charge, supported func(*catalog.Charge) bool, lookup func(*catalog.Charge) *metering.PriceLookup,
) {
	// A level of the hierarchy is read under one of its parents, as the
	// tree is expanded one level at a time. The top level, and a level
	// whose parent type was deleted, need no filter.
	if parent := types[""]; parent != nil && parent.DeletedAt == nil {
		if _, ok := in.Filters[parent.Code]; !ok {
			v.Add("group_by", CodeParentFilterRequired)
		}
	}

	var empty, tooMany bool
	flat := 0
	for code, values := range in.Filters {
		empty = empty || len(values) == 0
		tooMany = tooMany || len(values) > MaxFilterValues
		if types[code].Role == attribution.RoleFlat {
			flat++
		}
	}
	if empty {
		v.Add("filters", CodeValuesAreRequired)
	}
	if tooMany {
		v.Add("filters", CodeTooManyValues)
	}
	if flat > MaxFlatFilters {
		v.Add("filters", CodeTooManyFlatFilters)
	}

	switch {
	case in.From == nil && in.To == nil:
	case in.From == nil || in.To == nil:
		v.Add("to_time", CodeBothBoundariesAreRequired)
	case !in.From.Before(*in.To) || !in.To.After(*sub.StartedAt):
		v.Add("to_time", CodeInvalidDateRange)
	case in.To.Sub(*in.From) > MaxCustomWindow:
		v.Add("to_time", CodeWindowTooLong)
	}

	v.Check(in.Offset >= 0, "page_token", CodeValueIsOutOfRange)
	v.Check(utf8.RuneCountInString(in.Search) <= MaxSearchLength, "search", CodeValueIsTooLong)
	v.Check(in.Basis == BasisUnits || in.Basis == BasisAmount, "basis", CodeValueIsInvalid)

	if requested != nil && len(requested) == 0 {
		v.Add("charge_codes", CodeMustNotBeEmpty)
	}
	if slices.ContainsFunc(requested, func(c *catalog.Charge) bool { return !supported(c) }) {
		v.Add("charge_codes", CodeUnsupportedAggregationType)
	}

	if split != nil {
		if !supported(split) {
			v.Add("split_charge_code", CodeUnsupportedAggregationType)
		}
		if requested != nil && !slices.Contains(requested, split) {
			v.Add("split_charge_code", CodeMustBeSelected)
		}
		n := keptFilters(split)
		switch {
		case n == 0:
			v.Add("split_charge_code", CodeMustHaveFilters)
		case n > MaxSplitFilters || (supported(split) && lookup(split) == nil):
			v.Add("split_charge_code", CodeTooManyFilters)
		}
	}
}

// keptFilters returns the number of kept filters of c.
func keptFilters(c *catalog.Charge) int {
	n := 0
	for _, f := range c.Filters {
		if f.DeletedAt == nil {
			n++
		}
	}
	return n
}

// window returns the window read: the custom one, or the current billing
// period, a period longer than [MaxCustomWindow] narrowed to its last
// MaxCustomWindow up to now so that the default view scans as much as a
// custom one at most; it starts at the subscription's start at the
// earliest.
//
// source: app/services/usage_attributions/query_service.rb:183-206
func window(env *kernel.SubscriptionEnv, in QueryInput, now time.Time) (from, to time.Time, err error) {
	started := *env.Subscription.StartedAt
	if in.From != nil {
		return maxTime(in.From.UTC(), started), in.To.UTC(), nil
	}
	dates, err := env.Dates(now, period.CurrentUsage)
	if err != nil {
		return from, to, err
	}
	b, ok := dates.Boundaries()
	if !ok {
		return from, to, core.NewRuleError(CodeNotStarted, "the subscription has not started")
	}
	from, to = b.Charges.From, b.Charges.To
	if to.Sub(from) > MaxCustomWindow {
		if now.Before(to) {
			to = now
		}
		from = maxTime(from, to.Add(-MaxCustomWindow))
	}
	return maxTime(from, started), to, nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// column returns the query column of charge c.
//
// source: app/services/usage_attributions/query_service.rb:274-301
func (s *QueryService) column(c *catalog.Charge, p plan, basis Basis, lookup func(*catalog.Charge) *metering.PriceLookup) metering.AttributedCharge {
	m := p.metrics[c.BillableMetricID]
	split := c == p.split
	filtered := keptFilters(c) > 0
	priceable := basis == BasisAmount && c.ChargeModel == catalog.ChargeModelStandard
	col := metering.AttributedCharge{
		ChargeID: c.ID.String(), Code: m.Code, Count: m.AggregationType == catalog.AggregationTypeCount, Split: split,
	}
	if filtered && (priceable || split) {
		col.Lookup = lookup(c)
	}
	if priceable && (!filtered || col.Lookup != nil) {
		rate := money.NewFromInt(1)
		if c.AppliedPricingUnit != nil {
			rate = c.AppliedPricingUnit.ConversionRate
		}
		amount := unitAmount(c.Properties, rate, p.currency)
		col.UnitAmount = &amount
	}
	return col
}

// assemble fills res from the query's answer.
//
// source: app/services/usage_attributions/query_service.rb:303-370
func (s *QueryService) assemble(res *Result, p plan, q metering.AttributedQuery, u metering.AttributedUsage) {
	cells := q.Cells()
	// The cells in the order of the charges, a split charge's filters in
	// their own order and the default last, while the query ranks them by
	// precedence.
	var split []*catalog.ChargeFilter
	if p.split != nil {
		for i := range p.split.Filters {
			if p.split.Filters[i].DeletedAt == nil {
				split = append(split, &p.split.Filters[i])
			}
		}
	}
	position := func(id string) int {
		if i := slices.IndexFunc(split, func(f *catalog.ChargeFilter) bool { return f.ID.String() == id }); i >= 0 {
			return i
		}
		return len(split)
	}
	order := make([]int, len(cells))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		ca, cb := cells[a], cells[b]
		if ca.Charge != cb.Charge {
			return ca.Charge - cb.Charge
		}
		return position(ca.ChargeFilterID) - position(cb.ChargeFilterID)
	})
	amountBasis := res.Basis == BasisAmount
	build := func(v metering.AttributedValues) Aggregate {
		out := Aggregate{Events: v.Events}
		if amountBasis {
			out.Amount = new(v.Amount)
		}
		for _, i := range order {
			cell := cells[i]
			c := p.charges[cell.Charge]
			var cu metering.AttributedCellUsage
			if i < len(v.Cells) {
				cu = v.Cells[i]
			}
			oc := Cell{Charge: c, Metric: p.metrics[c.BillableMetricID], Units: cu.Units, Events: cu.Events}
			if cell.ChargeFilterID != "" {
				if j := position(cell.ChargeFilterID); j < len(split) {
					oc.Filter = split[j]
				}
			}
			if cell.Priced {
				oc.Amount = new(cu.Amount)
			}
			out.Cells = append(out.Cells, oc)
		}
		return out
	}
	for _, r := range u.Rows {
		if r.InPage {
			res.Rows = append(res.Rows, Row{Value: r.Value, Rank: r.Rank, Aggregate: build(r.AttributedValues)})
		}
	}
	res.Unattributed = build(u.Unattributed)
	res.Totals = build(u.Level.Add(u.Unattributed))
	res.Groups = u.Groups
}

// keys returns the keys of m.
func keys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}
