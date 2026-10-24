package fees

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period/schedule"
	"github.com/tanvoxyz/tanvo/libs/pricing"
)

// ProductSegment is a billing segment of a contract rate card with what
// pricing it needs.
type ProductSegment struct {
	// Segment is the billing segment.
	Segment *subscription.BillingSegment
	// Contract is the segment's contract.
	Contract *subscription.Subscription
	// Card is the contract rate card the segment prices.
	Card *subscription.RateCard
	// RateCard is the applied rate card.
	RateCard *catalog.RateCard
	// Product is the rate card's product.
	Product *catalog.Product
	// Location is the customer's time zone.
	Location *time.Location
	// Events, when not nil, is what the events of the segment's invoice
	// group hold: a metered segment none of them reaches is priced
	// without reading its events.
	Events *SegmentEvents
}

// productRate is the rate a segment is priced with: the phase's override,
// else the rate card rate.
type productRate struct {
	minAmount      int64
	conversionRate *money.Decimal
}

// segmentPricing is a segment with its rate and pricing currency.
type segmentPricing struct {
	ProductSegment
	rate productRate
	cur  pricingCurrency
	days int
}

// rateNotFound is the failure of a segment whose rate is gone (lago
// not_found_failure!(resource: "rate")).
func rateNotFound() error { return core.NotFound("rate") }

// loadSegment resolves the rate and the pricing currency of ps.
//
// source: app/models/billing_segment.rb:98-122
func loadSegment(ctx context.Context, r port.Repos, ps ProductSegment) (*segmentPricing, error) {
	seg := ps.Segment
	org := seg.OrganizationID
	sp := &segmentPricing{ProductSegment: ps, days: schedule.DaysBetween(seg.StartedAt, seg.EndedAt, ps.Location)}
	if !seg.RatePhaseID.IsZero() {
		phases, err := r.ContractRateCards.ListPhases(ctx, org, ps.Card.ID)
		if err != nil {
			return nil, fmt.Errorf("list phases of %s: %w", ps.Card.ID, err)
		}
		i := slices.IndexFunc(phases, func(p *catalog.RatePhase) bool { return p.ID == seg.RatePhaseID })
		if i < 0 || phases[i].Override == nil {
			return nil, rateNotFound()
		}
		o := phases[i].Override
		sp.rate = productRate{minAmount: o.MinAmount, conversionRate: o.PricingUnitConversionRate}
	} else {
		rt, err := r.Rates.Get(ctx, org, seg.RateCardRateID)
		if errors.Is(err, core.ErrNotFound) {
			return nil, rateNotFound()
		}
		if err != nil {
			return nil, fmt.Errorf("load rate %s: %w", seg.RateCardRateID, err)
		}
		sp.rate = productRate{minAmount: rt.MinAmount, conversionRate: rt.PricingUnitConversionRate}
	}
	sp.cur = pricingCurrency{fiat: seg.Currency, unit: seg.Currency}
	if !seg.PricingUnitID.IsZero() {
		pu, err := r.PricingUnits.Get(ctx, org, seg.PricingUnitID)
		if err != nil {
			return nil, fmt.Errorf("load pricing unit %s: %w", seg.PricingUnitID, err)
		}
		unit, err := money.NewCustomCurrency(pu.Code, pu.Name, catalog.PricingUnitExponent)
		if err != nil {
			return nil, fmt.Errorf("pricing unit %s: %w", pu.ID, err)
		}
		if sp.rate.conversionRate == nil {
			return nil, fmt.Errorf("segment %s: %w: no pricing unit conversion rate", seg.ID, core.ErrInvalidValue)
		}
		sp.cur.unit, sp.cur.pricingUnit = unit, pu
		sp.cur.applied = &pricing.AppliedPricingUnit{Unit: unit, ConversionRate: *sp.rate.conversionRate}
	}
	return sp, nil
}

// proratedMinimum returns the segment's minimum prorated by its ratio, in
// minor units of the pricing unit when the card has one (lago
// BillingSegment#prorated_min_amount_cents).
//
// source: app/models/billing_segment.rb:114-122
func (sp *segmentPricing) proratedMinimum() money.Decimal {
	m := money.NewFromInt(sp.rate.minAmount).Mul(sp.Segment.ProrationRatio)
	if sp.cur.applied == nil {
		return m
	}
	return m.Div(sp.cur.fiat.SubunitToUnit()).Div(sp.cur.applied.ConversionRate).Mul(sp.cur.unit.SubunitToUnit())
}

// structure returns the pricing structure of the segment's rate (lago
// ChargeModels::PricingStructure.from_billing_segment).
//
// source: app/services/charge_models/pricing_structure.rb:43-55
func (sp *segmentPricing) structure(props pricing.Properties) pricing.PricingStructure {
	return pricing.PricingStructure{Properties: props, Prorated: sp.RateCard.Proration, Currency: sp.cur.unit, ProductCatalog: true}
}

// properties decodes the segment's rate properties.
func (sp *segmentPricing) properties() (pricing.Properties, error) {
	seg := sp.Segment
	props, err := pricing.DecodeProperties(pricing.ChargeModel(seg.RateModel), seg.RateProperties.Raw())
	if err != nil {
		return nil, fmt.Errorf("properties of segment %s: %w", seg.ID, err)
	}
	return props, nil
}

// newProductFee returns a product fee of the segment on inv, with its period
// boundaries (lago BillingPeriodBoundaries#to_contract_fee_properties).
func (s *Service) newProductFee(sp *segmentPricing, inv *invoice.Invoice) *invoice.Fee {
	seg := sp.Segment
	now := s.d.Now()
	return &invoice.Fee{
		ID:                     core.NewID[core.FeeID](s.d.IDs),
		OrganizationID:         seg.OrganizationID,
		InvoiceID:              inv.ID,
		SubscriptionID:         seg.SubscriptionID,
		Type:                   core.FeeTypeProduct,
		SubscriptionRateCardID: seg.RateCardID,
		ProductID:              sp.Product.ID,
		RateCardRateID:         seg.RateCardRateID,
		RatePhaseID:            seg.RatePhaseID,
		PreciseAmount:          money.Zero,
		PreciseUnitAmount:      money.Zero,
		Units:                  money.Zero,
		TotalAggregatedUnits:   money.Zero,
		TaxesPreciseAmount:     money.Zero,
		TaxesRate:              money.Zero,
		Currency:               seg.Currency,
		Invoiceable:            true,
		PaymentStatus:          invoice.FeePaymentStatusPending,
		Properties: invoice.FeeProperties{
			From: seg.StartedAt, To: seg.EndedAt,
			ChargesFrom: seg.StartedAt, ChargesTo: seg.EndedAt,
			Timestamp: seg.BillingAt, ChargesDurationDays: sp.days,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// productTrueUp returns the true-up fee of parent when the usage used (and its
// precise counterpart) is below the segment's prorated minimum, nil
// otherwise (lago Fees::CreateTrueUpService with a billing segment).
//
// source: app/services/fees/create_true_up_service.rb:17-41, :54-64
func (s *Service) productTrueUp(sp *segmentPricing, parent *invoice.Fee, used, precise money.Decimal) (*invoice.Fee, error) {
	minimum := sp.proratedMinimum()
	amounts, err := pricing.ComputeAmounts(pricing.AmountsInput{
		Currency:    sp.cur.fiat,
		PricingUnit: sp.cur.applied,
		TrueUp:      &pricing.TrueUp{ProratedMinimum: &minimum, UsedAmountCents: &used, UsedPreciseAmountCents: &precise},
	})
	if err != nil {
		return nil, fmt.Errorf("true-up of segment %s: %w", sp.Segment.ID, err)
	}
	if amounts.TrueUp == nil {
		return nil, nil
	}
	clone := invoice.CloneFee(*parent)
	f := &clone
	f.ID = core.NewID[core.FeeID](s.d.IDs)
	setAmount(f, amounts.TrueUp, sp.cur.pricingUnit)
	f.Units, f.TotalAggregatedUnits = money.One, money.One
	f.EventsCount = 0
	f.ProductFilterID = core.ProductFilterID{}
	f.TrueUpParentFeeID = parent.ID
	return f, nil
}

// ProductFees returns the fees of the billing segment ps on inv (domain
// §21.4), not stored: for a fixed product, the fee of the card's units
// prorated by the segment's ratio and its true-up; for a metered one, the
// fees of the usage the segment covers, per pricing group, and their
// true-up, without the empty ones. A metered segment whose fees inv
// already holds (same card and period) returns them (fee identity).
func (s *Service) ProductFees(ctx context.Context, r port.Repos, inv *invoice.Invoice, ps ProductSegment) ([]*invoice.Fee, error) {
	sp, err := loadSegment(ctx, r, ps)
	if err != nil {
		return nil, err
	}
	if ps.Product.ProductType == catalog.ProductTypeFixed {
		return s.fixedProductFees(sp, inv)
	}
	return s.meteredProductFees(ctx, r, sp, inv)
}

// fixedProductFees prices the card's units over a fixed product's
// segment (lago BillingSegments::Fees::ComputeService).
//
// source: app/services/billing_segments/fees/compute_service.rb:15-131
func (s *Service) fixedProductFees(sp *segmentPricing, inv *invoice.Invoice) ([]*invoice.Fee, error) {
	props, err := sp.properties()
	if err != nil {
		return nil, err
	}
	units := money.Zero
	if sp.Card.Units != nil {
		units = *sp.Card.Units
	}
	prorated := units.Mul(sp.Segment.ProrationRatio)
	zero := money.Zero
	agg := pricing.AggregationResult{
		Units: prorated, CurrentUsageUnits: &prorated, FullUnits: &units, Count: 1, PreciseTotalAmountCents: &zero,
	}
	res, err := pricing.Apply(sp.structure(props), agg, pricing.ApplyOptions{})
	if err != nil {
		return nil, fmt.Errorf("price segment %s: %w", sp.Segment.ID, err)
	}
	minimum := sp.proratedMinimum()
	amounts, err := pricing.ComputeAmounts(pricing.AmountsInput{
		Currency: sp.cur.fiat, Result: &res, PricingUnit: sp.cur.applied,
		TrueUp: &pricing.TrueUp{ProratedMinimum: &minimum},
	})
	if err != nil {
		return nil, fmt.Errorf("amounts of segment %s: %w", sp.Segment.ID, err)
	}
	f := s.newProductFee(sp, inv)
	setAmount(f, amounts.Amount, sp.cur.pricingUnit)
	f.Units = units
	if f.AmountDetails, err = details(res.Details); err != nil {
		return nil, err
	}
	out := []*invoice.Fee{f}
	if amounts.TrueUp != nil {
		clone := invoice.CloneFee(*f)
		t := &clone
		t.ID = core.NewID[core.FeeID](s.d.IDs)
		setAmount(t, amounts.TrueUp, sp.cur.pricingUnit)
		t.Units = money.One
		t.TrueUpParentFeeID = f.ID
		out = append(out, t)
	}
	return out, nil
}

// meteredProductFees aggregates and prices the usage of a metered
// product's segment (lago Fees::ChargeService with a billing segment
// source, in the finalize context).
//
// source: app/services/fees/charge_service.rb:43-75, :292-366, :393-414, app/services/fees/charge_service/sources/billing_segment.rb
func (s *Service) meteredProductFees(ctx context.Context, r port.Repos, sp *segmentPricing, inv *invoice.Invoice) ([]*invoice.Fee, error) {
	if existing, err := billedSegmentFees(ctx, r, inv, sp.Segment); err != nil || len(existing) > 0 {
		return existing, err
	}
	org := sp.Segment.OrganizationID
	m, err := r.Metrics.Get(ctx, org, sp.Product.BillableMetricID)
	if err != nil {
		return nil, fmt.Errorf("load metric of product %s: %w", sp.Product.ID, err)
	}
	filters, err := r.ProductFilters.ListAll(ctx, org, sp.Product.ID)
	if err != nil {
		return nil, fmt.Errorf("list filters of product %s: %w", sp.Product.ID, err)
	}
	var selected *catalog.ProductFilter
	if id := sp.RateCard.ProductFilterID; !id.IsZero() {
		if selected, err = r.ProductFilters.Get(ctx, org, id); err != nil {
			return nil, fmt.Errorf("load product filter %s: %w", id, err)
		}
	}
	props, err := sp.properties()
	if err != nil {
		return nil, err
	}
	metric := kernel.MeteringMetricFilters(m)
	seg := sp.Segment
	req := metering.Request{
		Metric:         kernel.MeteringMetric(m),
		OrganizationID: org.String(),
		SubscriptionID: sp.Contract.ExternalID,
		From:           seg.StartedAt,
		To:             kernel.MeteringTo(seg.EndedAt),
		PeriodDays:     sp.days,
		Location:       sp.Location,
		Filter:         ProductFilterScope(filters, metric, selected),
		GroupBy:        props.Groups().PricingGroupKeys,
		Prorated:       sp.RateCard.Proration,
		Dynamic:        seg.RateModel == catalog.ChargeModel(pricing.Dynamic),
	}
	req.BypassAggregation = sp.Events.bypass(sp.ProductSegment, m, filters, metric, selected)
	if p, ok := props.(pricing.PercentageProperties); ok {
		req.FreeUnitsPerEvents = int(p.FreeUnitsPerEvents)
		req.FreeUnitsPerTotalAggregation = deref(p.FreeUnitsPerTotalAggregation)
	}
	agg, err := s.aggregateProduct(ctx, sp, props, req)
	if err != nil {
		return nil, err
	}
	res, err := pricing.Apply(sp.structure(props), agg, pricing.ApplyOptions{})
	if err != nil {
		return nil, fmt.Errorf("price segment %s: %w", seg.ID, err)
	}
	var fs []*invoice.Fee
	for _, gr := range res.GroupResults() {
		amounts, err := pricing.ComputeAmounts(pricing.AmountsInput{Currency: sp.cur.fiat, Result: &gr, PricingUnit: sp.cur.applied})
		if err != nil {
			return nil, fmt.Errorf("amounts of segment %s: %w", seg.ID, err)
		}
		if gr.Units.IsNegative() || gr.Amount.IsNegative() {
			gr.Units, gr.FullUnits = money.Zero, nil
		}
		units := gr.Units
		if sp.RateCard.Proration {
			units = deref(firstNonNil(gr.FullUnits, &gr.Units))
		}
		f := s.newProductFee(sp, inv)
		if selected != nil {
			f.ProductFilterID = selected.ID
		}
		setAmount(f, amounts.Amount, sp.cur.pricingUnit)
		f.Units = units
		f.TotalAggregatedUnits = deref(firstNonNil(gr.TotalAggregatedUnits, &units))
		f.EventsCount = gr.Count
		f.GroupedBy = gr.GroupedBy
		if f.AmountDetails, err = details(gr.Details); err != nil {
			return nil, err
		}
		fs = append(fs, f)
	}
	if fs, err = s.addProductTrueUp(sp, fs); err != nil {
		return nil, err
	}
	return slices.DeleteFunc(fs, func(f *invoice.Fee) bool { return !keepProductFee(f, fs) }), nil
}

// aggregateProduct aggregates the segment's usage for pricing, with the
// per-event values the model needs.
func (s *Service) aggregateProduct(ctx context.Context, sp *segmentPricing, props pricing.Properties, req metering.Request) (pricing.AggregationResult, error) {
	m, err := metering.Aggregate(ctx, s.d.Events, req)
	if err != nil {
		return pricing.AggregationResult{}, fmt.Errorf("aggregate segment %s: %w", sp.Segment.ID, err)
	}
	agg := aggregationOf(m)
	perEvent := false
	switch p := props.(type) {
	case pricing.PercentageProperties:
		perEvent = p.PerTransactionMinAmount != nil || p.PerTransactionMaxAmount != nil
	case pricing.GraduatedProperties:
		perEvent = sp.RateCard.Proration
	}
	if perEvent {
		if agg.PerEvent, err = s.perEvent(ctx, req, nil); err != nil {
			return pricing.AggregationResult{}, err
		}
	}
	if len(req.GroupBy) > 0 {
		agg.Groups = make([]pricing.AggregationResult, 0, len(m.Grouped))
		for _, g := range m.Grouped {
			ga := aggregationOf(g)
			if perEvent {
				if ga.PerEvent, err = s.perEvent(ctx, req, g.Groups); err != nil {
					return pricing.AggregationResult{}, err
				}
			}
			agg.Groups = append(agg.Groups, ga)
		}
	}
	return agg, nil
}

// addProductTrueUp appends the true-up of the segment's minimum to fs,
// cloned from the fee of the rate card's filter (lago
// Fees::ChargeService#init_true_up_fee).
//
// source: app/services/fees/charge_service.rb:368-391
func (s *Service) addProductTrueUp(sp *segmentPricing, fs []*invoice.Fee) ([]*invoice.Fee, error) {
	i := slices.IndexFunc(fs, func(f *invoice.Fee) bool { return f.ProductFilterID == sp.RateCard.ProductFilterID })
	if i < 0 {
		return fs, nil
	}
	used, precise := money.Zero, money.Zero
	for _, f := range fs {
		if sp.cur.applied != nil {
			if f.PricingUnitUsage != nil {
				used = used.Add(f.PricingUnitUsage.Amount)
				precise = precise.Add(f.PricingUnitUsage.PreciseAmount)
			}
			continue
		}
		used = used.Add(kernel.Cents(f.Amount))
		precise = precise.Add(f.PreciseAmount)
	}
	t, err := s.productTrueUp(sp, fs[i], used, precise)
	if err != nil || t == nil {
		return fs, err
	}
	return append(fs, t), nil
}

// keepProductFee reports whether a metered product fee is stored: it has
// units, an amount or events, is a true-up or has one (lago
// Fees::ChargeService#should_persist_fee?).
//
// source: app/services/fees/charge_service.rb:342-350
func keepProductFee(f *invoice.Fee, all []*invoice.Fee) bool {
	if !f.Units.IsZero() || f.Amount != 0 || f.EventsCount != 0 || !f.TrueUpParentFeeID.IsZero() {
		return true
	}
	return slices.ContainsFunc(all, func(o *invoice.Fee) bool { return o.TrueUpParentFeeID == f.ID })
}

// billedSegmentFees returns the fees inv already holds for the segment's
// card and period (lago Fees::ChargeService#already_billed_segment? and
// Fee.matching_contract_period).
//
// source: app/services/fees/charge_service.rb:407-426
func billedSegmentFees(ctx context.Context, r port.Repos, inv *invoice.Invoice, seg *subscription.BillingSegment) ([]*invoice.Fee, error) {
	fs, err := r.Fees.ListByInvoice(ctx, inv.OrganizationID, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("list fees of invoice %s: %w", inv.ID, err)
	}
	return slices.DeleteFunc(fs, func(f *invoice.Fee) bool {
		return f.SubscriptionRateCardID != seg.RateCardID ||
			!f.Properties.ChargesFrom.Equal(seg.StartedAt) || !f.Properties.ChargesTo.Equal(seg.EndedAt)
	}), nil
}

// ProductFilterScope returns the events a rate card of a metered product
// bills: those matching the values of its filter (every value of a
// metric filter for a value left open; no constraint for a card pricing
// the whole product), less those of the product's other filters it
// contains, which their own cards bill (lago
// Events::BillingPeriodFilters::MatchingAndIgnoredService#child_filters
// over a billing segment target).
//
// source: app/services/events/billing_period_filters/matching_and_ignored_service.rb:13-20, :52-95, app/services/events/billing_period_filters/sources/billing_segment.rb
func ProductFilterScope(filters []*catalog.ProductFilter, metric []metering.MetricFilter, selected *catalog.ProductFilter) metering.Filter {
	empty := &catalog.ProductFilter{}
	if selected == nil {
		selected = empty
	}
	matching := filterValues(selected, metric)
	var ignored []map[string][]string
	for _, f := range filters {
		if selected != empty && f.ID == selected.ID {
			continue
		}
		child := filterValues(f, metric)
		if !containsMatching(child, matching) {
			continue
		}
		if sameKeys(child, matching) {
			switch {
			case identical(child, matching):
				if !older(f, selected) {
					continue
				}
			case !subset(child, matching):
				for k, values := range child {
					if allValues(selected, k) {
						continue
					}
					child[k] = slices.DeleteFunc(slices.Clone(values), func(v string) bool { return slices.Contains(matching[k], v) })
				}
			}
		}
		ignored = append(ignored, child)
	}
	scope := metering.Filter{Ignored: metering.MinimizeIgnoredFilters(ignored)}
	if len(matching) > 0 {
		scope.Matching = matching
	}
	return scope
}

// filterValues returns the values of f by key, a value left open standing
// for every value of the metric filter (lago
// ProductFilter#to_h_with_all_values).
//
// source: app/models/product_filter.rb:44-51
func filterValues(f *catalog.ProductFilter, metric []metering.MetricFilter) map[string][]string {
	out := map[string][]string{}
	for _, v := range f.Values {
		if v.Value != nil {
			out[v.Key] = append(out[v.Key], *v.Value)
			continue
		}
		for _, m := range metric {
			if m.Key == v.Key {
				out[v.Key] = append(out[v.Key], m.Values...)
				break
			}
		}
		if _, ok := out[v.Key]; !ok {
			out[v.Key] = nil
		}
	}
	return out
}

// allValues reports whether f leaves key open (lago
// Sources::BillingSegment#all_filter_values?).
func allValues(f *catalog.ProductFilter, key string) bool {
	n, open := 0, false
	for _, v := range f.Values {
		if v.Key == key {
			n++
			open = v.Value == nil
		}
	}
	return n == 1 && open
}

// containsMatching reports whether child has, for every key of matching,
// one of its values.
func containsMatching(child, matching map[string][]string) bool {
	for k, values := range matching {
		if !slices.ContainsFunc(values, func(v string) bool { return slices.Contains(child[k], v) }) {
			return false
		}
	}
	return true
}

// sameKeys reports whether a and b have the same keys.
func sameKeys(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

// identical reports whether child has the values of matching for every
// key.
func identical(child, matching map[string][]string) bool {
	for k, values := range child {
		a, b := slices.Clone(values), slices.Clone(matching[k])
		slices.Sort(a)
		slices.Sort(b)
		if !slices.Equal(a, b) {
			return false
		}
	}
	return true
}

// subset reports whether every value of child is one of matching.
func subset(child, matching map[string][]string) bool {
	for k, values := range child {
		for _, v := range values {
			if !slices.Contains(matching[k], v) {
				return false
			}
		}
	}
	return true
}

// older reports whether f was created before selected, by creation time
// then ID; every filter is older than an unsaved one.
func older(f, selected *catalog.ProductFilter) bool {
	if selected.CreatedAt.IsZero() {
		return true
	}
	if c := f.CreatedAt.Compare(selected.CreatedAt); c != 0 {
		return c < 0
	}
	return f.ID.String() < selected.ID.String()
}
