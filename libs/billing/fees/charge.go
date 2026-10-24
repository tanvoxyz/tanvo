package fees

import (
	"context"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
	"github.com/tanvoxyz/tanvo/libs/pricing"
)

// bucket is one pricing bucket of a charge: a charge filter, the default
// bucket of a charge with filters (the events no filter matches), or the
// whole charge.
type bucket struct {
	// filter is the charge filter; nil for the default bucket and for a
	// charge without filters.
	filter *catalog.ChargeFilter
	// scope selects the events of the bucket.
	scope metering.Filter
	// isDefault marks the default bucket of a charge with filters.
	isDefault bool
	// props are the model properties of the bucket.
	props pricing.Properties
}

// filterID returns the bucket's charge filter, zero for the default
// bucket.
func (b bucket) filterID() core.ChargeFilterID {
	if b.filter == nil {
		return core.ChargeFilterID{}
	}
	return b.filter.ID
}

// groupKeys returns the pricing group keys of the bucket of the charge c,
// with [catalog.EventTargetWalletCode] appended when c accepts a target
// wallet.
//
// source: app/services/fees/charge_service/sources/charge.rb:94-102
func (b bucket) groupKeys(c *catalog.Charge) []string {
	keys := b.props.Groups().PricingGroupKeys
	if c.AcceptsTargetWallet && !slices.Contains(keys, catalog.EventTargetWalletCode) {
		keys = append(slices.Clone(keys), catalog.EventTargetWalletCode)
	}
	return keys
}

// chargeContext is a charge with what pricing it needs.
type chargeContext struct {
	charge  *catalog.Charge
	metric  *catalog.BillableMetric
	cur     pricingCurrency
	filters []metering.ChargeFilter
	metricF []metering.MetricFilter
}

// loadCharge loads the metric and pricing currency of c.
func loadCharge(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, c *catalog.Charge) (*chargeContext, error) {
	m, err := r.Metrics.Get(ctx, env.OrganizationID(), c.BillableMetricID)
	if err != nil {
		return nil, fmt.Errorf("load metric of charge %s: %w", c.ID, err)
	}
	pc, err := chargeCurrency(ctx, r, env, c)
	if err != nil {
		return nil, err
	}
	return &chargeContext{
		charge: c, metric: m, cur: pc,
		filters: kernel.MeteringChargeFilters(c),
		metricF: kernel.MeteringMetricFilters(m),
	}, nil
}

// properties decodes model properties of the charge, falling back to the
// charge's own when raw is empty.
func (cc *chargeContext) properties(p catalog.Properties) (pricing.Properties, error) {
	if p.IsZero() {
		p = cc.charge.Properties
	}
	props, err := pricing.DecodeProperties(pricing.ChargeModel(cc.charge.ChargeModel), p.Raw())
	if err != nil {
		return nil, fmt.Errorf("properties of charge %s: %w", cc.charge.ID, err)
	}
	return props, nil
}

// structure returns the pricing structure of a bucket.
func (cc *chargeContext) structure(props pricing.Properties) pricing.PricingStructure {
	return pricing.PricingStructure{
		Properties: props, Prorated: cc.charge.Prorated, AcceptsTargetWallet: cc.charge.AcceptsTargetWallet,
		Currency: cc.cur.unit,
	}
}

// buckets returns the pricing buckets of the charge for a period: each
// filter with values, then the default bucket; or the whole charge when it
// has no filters (lago Sources::Charge#pricing_buckets).
func (cc *chargeContext) buckets() ([]bucket, error) {
	if len(cc.charge.Filters) == 0 {
		props, err := cc.properties(cc.charge.Properties)
		if err != nil {
			return nil, err
		}
		return []bucket{{props: props}}, nil
	}
	var out []bucket
	for i := range cc.charge.Filters {
		f := &cc.charge.Filters[i]
		if len(f.Values) == 0 {
			// A filter without values matches no event; billing it would
			// duplicate the default bucket.
			continue
		}
		b, err := cc.filterBucket(f)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	def, err := cc.defaultBucket()
	if err != nil {
		return nil, err
	}
	return append(out, def), nil
}

// filterBucket returns the bucket of the filter f.
func (cc *chargeContext) filterBucket(f *catalog.ChargeFilter) (bucket, error) {
	scope, err := metering.ChargeFilterScope(cc.filters, cc.metricF, f.ID.String())
	if err != nil {
		return bucket{}, fmt.Errorf("scope of charge filter %s: %w", f.ID, err)
	}
	props, err := cc.properties(f.Properties)
	if err != nil {
		return bucket{}, err
	}
	return bucket{filter: f, scope: scope, props: props}, nil
}

// defaultBucket returns the bucket of the events no filter matches.
func (cc *chargeContext) defaultBucket() (bucket, error) {
	scope, err := metering.ChargeFilterScope(cc.filters, cc.metricF, "")
	if err != nil {
		return bucket{}, fmt.Errorf("default scope of charge %s: %w", cc.charge.ID, err)
	}
	props, err := cc.properties(cc.charge.Properties)
	if err != nil {
		return bucket{}, err
	}
	return bucket{scope: scope, props: props, isDefault: true}, nil
}

// request returns the aggregation request of a bucket over the charges
// period of b.
func (cc *chargeContext) request(env *kernel.SubscriptionEnv, bk bucket, b period.Boundaries, opts Options) metering.Request {
	c := cc.charge
	req := kernel.MeteringRequest(env, cc.metric, b.Charges, b.ChargesDays)
	req.Filter = bk.scope
	req.GroupBy = bk.groupKeys(c)
	req.Prorated = c.Prorated
	req.PayInAdvance = c.PayInAdvance
	req.Dynamic = c.ChargeModel == catalog.ChargeModel(pricing.Dynamic)
	req.CurrentUsage = opts.CurrentUsage
	req.TerminatedByUpgrade = env.Subscription.TerminatedAt != nil && env.Change == period.Upgrade
	if p, ok := bk.props.(pricing.PercentageProperties); ok {
		req.FreeUnitsPerEvents = int(p.FreeUnitsPerEvents)
		req.FreeUnitsPerTotalAggregation = deref(p.FreeUnitsPerTotalAggregation)
	}
	if opts.Cache != nil {
		req.Cache = opts.Cache(cc.cacheScope(env, bk, bk.isDefault))
	}
	if !opts.MaxTimestamp.IsZero() {
		req.MaxTimestamp = opts.MaxTimestamp
	}
	return req
}

// cacheScope returns the cached aggregation scope of a bucket; defaultBucket
// restricts the default bucket of a charge with filters to the states
// without a filter (event.CachedAggregationScope).
func (cc *chargeContext) cacheScope(env *kernel.SubscriptionEnv, bk bucket, defaultBucket bool) event.CachedAggregationScope {
	return event.CachedAggregationScope{
		ExternalSubscriptionID: env.Subscription.ExternalID, ChargeID: cc.charge.ID,
		ChargeFilterID: bk.filterID(), DefaultBucket: defaultBucket,
	}
}

// needsPerEvent reports whether the model of a bucket prices event by
// event.
func (cc *chargeContext) needsPerEvent(props pricing.Properties) bool {
	switch p := props.(type) {
	case pricing.PercentageProperties:
		return p.PerTransactionMinAmount != nil || p.PerTransactionMaxAmount != nil
	case pricing.GraduatedProperties:
		return cc.charge.Prorated
	}
	return false
}

// perEvent returns the per-event values of an aggregation. Aggregations that
// have no per-event values (latest, weighted_sum) fail, as lago raises for them
// (app/services/billable_metrics/aggregations/base_service.rb:113-123).
// Prorated graduated charges never reach this on weighted_sum
// (catalog.ChargeProratable); a percentage charge with per-transaction
// bounds on a latest or weighted_sum metric fails to bill, as in lago.
func (s *Service) perEvent(ctx context.Context, req metering.Request, groups metering.Groups) (*pricing.PerEventAggregation, error) {
	pe, err := metering.PerEvent(ctx, s.d.Events, req, metering.PerEventOptions{GroupValues: groups})
	if err != nil {
		return nil, fmt.Errorf("per-event values of %s: %w", req.Metric.Code, err)
	}
	return &pricing.PerEventAggregation{Values: pe.Values, Prorated: pe.Prorated}, nil
}

// aggregationOf converts a metering result to the charge models' input.
func aggregationOf(m metering.Result) pricing.AggregationResult {
	return pricing.AggregationResult{
		Units:                   m.Aggregation,
		CurrentUsageUnits:       m.CurrentUsageUnits,
		FullUnits:               m.FullUnitsNumber,
		Count:                   int64Of(m.Count),
		TotalAggregatedUnits:    m.TotalAggregatedUnits,
		PreciseTotalAmountCents: m.PreciseTotalAmountCents,
		RunningTotal:            m.RunningTotal,
		GroupedBy:               groupedBy(m.Groups),
		CurrentAggregation:      m.CurrentAggregation,
		MaxAggregation:          m.MaxAggregation,
		UnitsApplied:            m.UnitsApplied,
	}
}

// aggregate aggregates a bucket and converts the result for pricing.
func (s *Service) aggregate(ctx context.Context, cc *chargeContext, bk bucket, req metering.Request) (metering.Result, pricing.AggregationResult, error) {
	m, err := metering.Aggregate(ctx, s.d.Events, req)
	if err != nil {
		return metering.Result{}, pricing.AggregationResult{}, fmt.Errorf("aggregate charge %s: %w", cc.charge.ID, err)
	}
	agg := aggregationOf(m)
	perEvent := cc.needsPerEvent(bk.props)
	if perEvent {
		if agg.PerEvent, err = s.perEvent(ctx, req, nil); err != nil {
			return metering.Result{}, pricing.AggregationResult{}, err
		}
	}
	if len(req.GroupBy) > 0 {
		agg.Groups = make([]pricing.AggregationResult, 0, len(m.Grouped))
		for _, g := range m.Grouped {
			ga := aggregationOf(g)
			if perEvent {
				if ga.PerEvent, err = s.perEvent(ctx, req, g.Groups); err != nil {
					return metering.Result{}, pricing.AggregationResult{}, err
				}
			}
			agg.Groups = append(agg.Groups, ga)
		}
	}
	return m, agg, nil
}

// ChargeFees returns the fees of a charge for the charges period of sc
// (domain §22.6): one fee per pricing bucket (charge filter, the default
// bucket) and pricing group, then a true-up fee when the charge's minimum
// amount is not reached. It returns nil when the charge is already billed
// for the period or not billed in it. Fees without units, amount or events
// are dropped unless opts.Recurring is set.
func (s *Service) ChargeFees(ctx context.Context, r port.Repos, sc Scope, c *catalog.Charge, opts Options) (*ChargeResult, error) {
	env, b := sc.Env, sc.Boundaries
	if !opts.CurrentUsage {
		billed, err := s.chargeBilled(ctx, r, sc, c)
		if err != nil || billed {
			return nil, err
		}
	}
	if !b.ChargesBilled && !opts.CurrentUsage {
		return nil, nil
	}
	cc, err := loadCharge(ctx, r, env, c)
	if err != nil {
		return nil, err
	}
	buckets, err := cc.buckets()
	if err != nil {
		return nil, err
	}
	adj, err := loadAdjustments(ctx, r, sc, opts)
	if err != nil {
		return nil, err
	}

	out := &ChargeResult{}
	for _, bk := range buckets {
		fs, err := s.bucketFees(ctx, sc, cc, bk, adj, opts, out)
		if err != nil {
			return nil, err
		}
		if opts.CurrentUsage {
			fs = currentUsageFees(fs)
		}
		out.Fees = append(out.Fees, fs...)
	}
	if opts.CurrentUsage {
		return out, nil
	}

	if sc.Invoice == nil || sc.Invoice.Type != invoice.TypeProgressiveBilling {
		if err := s.addTrueUp(env, b, cc, out); err != nil {
			return nil, err
		}
	}
	// A fee with an adjustment is kept even once the invoice is no longer a
	// draft.
	present, err := listAdjustments(ctx, r, sc, opts)
	if err != nil {
		return nil, err
	}
	kept := make([]*invoice.Fee, 0, len(out.Fees))
	for _, f := range out.Fees {
		if keepChargeFee(f, out.Fees, present, opts) {
			kept = append(kept, f)
		}
	}
	out.Fees = kept
	return out, nil
}

// chargeBilled reports whether the charge already has fees on the invoice
// of sc or, without an invoice, unbilled fees for the charges period.
func (s *Service) chargeBilled(ctx context.Context, r port.Repos, sc Scope, c *catalog.Charge) (bool, error) {
	if sc.Invoice != nil {
		fs, err := invoiceFees(ctx, r, sc)
		if err != nil {
			return false, err
		}
		for _, f := range fs {
			if f.ChargeID == c.ID && f.SubscriptionID == sc.Env.Subscription.ID {
				return true, nil
			}
		}
		return false, nil
	}
	fs, err := r.Fees.ListByCharge(ctx, sc.Env.OrganizationID(), c.ID)
	if err != nil {
		return false, fmt.Errorf("load fees of charge %s: %w", c.ID, err)
	}
	b := sc.Boundaries
	for _, f := range fs {
		if f.SubscriptionID == sc.Env.Subscription.ID && f.InvoiceID.IsZero() && f.PayInAdvanceEventID.IsZero() && f.PayInAdvanceEventTransactionID == "" &&
			f.Properties.ChargesFrom.Equal(b.Charges.From) && f.Properties.ChargesTo.Equal(b.Charges.To) {
			return true, nil
		}
	}
	return false, nil
}

// bucketFees prices one bucket and returns its fees, recording the
// recurring aggregation states in out.
func (s *Service) bucketFees(ctx context.Context, sc Scope, cc *chargeContext, bk bucket, adj adjustments, opts Options, out *ChargeResult) ([]*invoice.Fee, error) {
	req := cc.request(sc.Env, bk, sc.Boundaries, opts)
	m, agg, err := s.aggregate(ctx, cc, bk, req)
	if err != nil {
		return nil, err
	}
	res, err := pricing.Apply(cc.structure(bk.props), agg, pricing.ApplyOptions{})
	if err != nil {
		return nil, fmt.Errorf("price charge %s: %w", cc.charge.ID, err)
	}
	// The recurring states are kept per group, each at the instant of its
	// own last event; only recurring aggregations set it (lago
	// persist_recurring_value).
	results := m.Grouped
	if len(req.GroupBy) == 0 {
		results = []metering.Result{m}
	}
	if len(results) > 0 && results[0].RecurringUpdatedAt != nil && !opts.CurrentUsage {
		for _, g := range results {
			out.Aggregations = append(out.Aggregations, CachedAggregation{
				ChargeID:               cc.charge.ID,
				ChargeFilterID:         bk.filterID(),
				ExternalSubscriptionID: sc.Env.Subscription.ExternalID,
				Aggregation: metering.CachedAggregation{
					Timestamp:          *firstNonNil(g.RecurringUpdatedAt, results[0].RecurringUpdatedAt),
					Groups:             g.Groups,
					CurrentAggregation: cloneDecimal(firstNonNil(g.TotalAggregatedUnits, &g.Aggregation)),
				},
			})
		}
	}

	var fs []*invoice.Fee
	for _, gr := range res.GroupResults() {
		if opts.CurrentUsage && bk.filter != nil && gr.Units.IsZero() && opts.SkipZeroUnitsFilters {
			continue
		}
		grouped := gr.GroupedBy
		var a *invoice.AdjustedFee
		if !opts.CurrentUsage {
			a = adj.charge(cc.charge.ID, bk.filterID(), grouped, sc.Boundaries.Charges)
		}
		var f *invoice.Fee
		if a != nil && a.Mode != invoice.AdjustmentModeDisplayName {
			f, err = s.adjustedChargeFee(sc, cc, bk, a)
		} else {
			f, err = s.chargeFee(sc, cc, bk, gr, opts)
			if a != nil {
				f.InvoiceDisplayName = a.InvoiceDisplayName
			}
		}
		if err != nil {
			return nil, err
		}
		fs = append(fs, f)
	}
	return fs, nil
}

// chargeFee builds the fee of one model result.
func (s *Service) chargeFee(sc Scope, cc *chargeContext, bk bucket, gr pricing.Result, opts Options) (*invoice.Fee, error) {
	c := cc.charge
	amounts, err := pricing.ComputeAmounts(pricing.AmountsInput{Currency: cc.cur.fiat, Result: &gr, PricingUnit: cc.cur.applied})
	if err != nil {
		return nil, fmt.Errorf("amounts of charge %s: %w", c.ID, err)
	}
	// A negative result is a zero fee.
	if gr.Units.IsNegative() || gr.Amount.IsNegative() {
		gr.Units, gr.FullUnits = money.Zero, nil
	}
	units := gr.Units
	switch {
	case opts.CurrentUsage && (c.PayInAdvance || c.Prorated):
		units = deref(firstNonNil(gr.CurrentUsageUnits, &gr.Units))
	case c.Prorated:
		units = deref(firstNonNil(gr.FullUnits, &gr.Units))
	}

	f := s.newFee(sc, core.FeeTypeCharge)
	f.ChargeID = c.ID
	f.ChargeFilterID = bk.filterID()
	f.Properties = s.chargeProperties(sc)
	setAmount(f, amounts.Amount, cc.cur.pricingUnit)
	f.Units = units
	f.TotalAggregatedUnits = deref(firstNonNil(gr.TotalAggregatedUnits, &units))
	f.EventsCount = gr.Count
	f.GroupedBy = gr.GroupedBy
	f.Invoiceable = c.Invoiceable
	if !c.Invoiceable {
		f.PayInAdvance = c.PayInAdvance
	}
	if f.AmountDetails, err = details(gr.Details); err != nil {
		return nil, err
	}
	return f, nil
}

// adjustedChargeFee builds the fee of a charge bucket from its adjustment
// (lago Fees::InitFromAdjustedChargeFeeService): the adjusted units are
// priced by the charge model, or at the adjusted unit price.
func (s *Service) adjustedChargeFee(sc Scope, cc *chargeContext, bk bucket, a *invoice.AdjustedFee) (*invoice.Fee, error) {
	c := cc.charge
	var res pricing.Result
	if a.Mode == invoice.AdjustmentModeUnits {
		units := a.Units
		agg := pricing.AggregationResult{Units: units, CurrentUsageUnits: &units, FullUnits: &units}
		if c.ChargeModel == catalog.ChargeModel(pricing.Dynamic) {
			zero := money.Zero
			agg.PreciseTotalAmountCents = &zero
		}
		r, err := pricing.Apply(cc.structure(bk.props), agg, pricing.ApplyOptions{})
		if err != nil {
			return nil, fmt.Errorf("price adjusted fee of charge %s: %w", c.ID, err)
		}
		res = r
	} else {
		res = pricing.Result{Units: a.Units, Amount: a.Units.Mul(a.UnitPreciseAmount), UnitAmount: a.UnitPreciseAmount}
	}
	amounts, err := pricing.ComputeAmounts(pricing.AmountsInput{Currency: cc.cur.fiat, Result: &res, PricingUnit: cc.cur.applied})
	if err != nil {
		return nil, fmt.Errorf("amounts of adjusted fee of charge %s: %w", c.ID, err)
	}

	f := s.newFee(sc, core.FeeTypeCharge)
	f.ChargeID = c.ID
	f.ChargeFilterID = a.ChargeFilterID
	f.Properties = s.chargeProperties(sc)
	setAmount(f, amounts.Amount, cc.cur.pricingUnit)
	if a.Mode == invoice.AdjustmentModeUnitsAndUnitPrice && cc.cur.applied == nil {
		// The unit price is rounded, not truncated, to minor units.
		sub := cc.cur.fiat.SubunitToUnit()
		f.PreciseAmount = a.Units.Mul(a.UnitPreciseAmount).Mul(sub)
		f.Amount = kernel.RoundCents(f.PreciseAmount)
		f.UnitAmount = kernel.RoundCents(a.UnitPreciseAmount.Mul(sub))
	}
	f.Units, f.TotalAggregatedUnits = a.Units, a.Units
	f.InvoiceDisplayName = a.InvoiceDisplayName
	f.GroupedBy = a.GroupedBy
	f.Invoiceable = c.Invoiceable
	if a.Mode == invoice.AdjustmentModeUnits {
		if f.AmountDetails, err = details(res.Details); err != nil {
			return nil, err
		}
	} else {
		f.AmountDetails = []byte("{}")
	}
	return f, nil
}

// currentUsageFees keeps the fees of a bucket worth showing in current
// usage: those with units, amount or events, or every one when none has.
func currentUsageFees(fs []*invoice.Fee) []*invoice.Fee {
	var kept []*invoice.Fee
	for _, f := range fs {
		if nonZero(f) {
			kept = append(kept, f)
		}
	}
	if len(kept) == 0 {
		return fs
	}
	return kept
}

// nonZero reports whether a fee has units, an amount or events.
func nonZero(f *invoice.Fee) bool { return !f.Units.IsZero() || f.Amount != 0 || f.EventsCount != 0 }

// keepChargeFee reports whether a charge fee is stored (lago
// Fees::ChargeService#should_persist_fee?).
func keepChargeFee(f *invoice.Fee, all []*invoice.Fee, adj adjustments, opts Options) bool {
	if opts.Recurring || nonZero(f) || !f.TrueUpParentFeeID.IsZero() {
		return true
	}
	if adj.charge(f.ChargeID, f.ChargeFilterID, f.GroupedBy, period.Period{From: f.Properties.ChargesFrom, To: f.Properties.ChargesTo}) != nil {
		return true
	}
	for _, o := range all {
		if o.TrueUpParentFeeID == f.ID {
			return true
		}
	}
	return false
}

// addTrueUp appends the true-up fee of the charge's minimum amount to out
// when the usage is below it (lago Fees::CreateTrueUpService). It is
// cloned from the first fee of the default bucket.
func (s *Service) addTrueUp(env *kernel.SubscriptionEnv, b period.Boundaries, cc *chargeContext, out *ChargeResult) error {
	c := cc.charge
	var parent *invoice.Fee
	for _, f := range out.Fees {
		if f.ChargeFilterID.IsZero() {
			parent = f
			break
		}
	}
	if parent == nil || c.MinAmount <= 0 {
		return nil
	}
	used, precise := money.Zero, money.Zero
	for _, f := range out.Fees {
		if cc.cur.applied != nil {
			if f.PricingUnitUsage != nil {
				used = used.Add(f.PricingUnitUsage.Amount)
				precise = precise.Add(f.PricingUnitUsage.PreciseAmount)
			}
			continue
		}
		used = used.Add(kernel.Cents(f.Amount))
		precise = precise.Add(f.PreciseAmount)
	}
	to := billedTo(parent.Properties.ChargesTo, env.Subscription.TerminatedAt, env.Location())
	billed := env.Period().BilledDays(parent.Properties.ChargesFrom, to)
	amounts, err := pricing.ComputeAmounts(pricing.AmountsInput{
		Currency:    cc.cur.fiat,
		PricingUnit: cc.cur.applied,
		TrueUp: &pricing.TrueUp{
			MinimumAmountCents:     c.MinAmount,
			BilledDays:             int64(billed),
			PeriodDays:             int64(b.ChargesDays),
			UsedAmountCents:        &used,
			UsedPreciseAmountCents: &precise,
		},
	})
	if err != nil {
		return fmt.Errorf("true-up of charge %s: %w", c.ID, err)
	}
	if amounts.TrueUp == nil {
		return nil
	}
	clone := invoice.CloneFee(*parent)
	f := &clone
	f.ID = core.NewID[core.FeeID](s.d.IDs)
	setAmount(f, amounts.TrueUp, cc.cur.pricingUnit)
	f.Units, f.TotalAggregatedUnits = money.One, money.One
	f.EventsCount = 0
	f.ChargeFilterID = core.ChargeFilterID{}
	f.TrueUpParentFeeID = parent.ID
	out.Fees = append(out.Fees, f)
	return nil
}

// firstNonNil returns the first non-nil pointer.
func firstNonNil[T any](ps ...*T) *T {
	for _, p := range ps {
		if p != nil {
			return p
		}
	}
	return nil
}
