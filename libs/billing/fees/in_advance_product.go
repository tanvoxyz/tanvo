package fees

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/pricing"
)

// InAdvanceProductFees returns the fee the in-advance metered segment ps
// bills for one event (lago Fees::CreatePayInAdvanceService over a
// billing segment): the price of the segment's usage up to the event with
// it minus the price without it, in the event's pricing group. An event
// whose best matching product filter is not the rate card's (the default
// bucket when the card prices the whole product) bills nothing. The fee
// is attached to no invoice and is invoiceable when the rate card shows
// on invoices; the caller stores it.
//
// source: app/services/fees/create_pay_in_advance_service.rb:59-118, app/services/fees/charge_service/sources/billing_segment.rb:50-61, app/services/charges/pay_in_advance_aggregation_service.rb:14-72
func (s *Service) InAdvanceProductFees(ctx context.Context, r port.Repos, ps ProductSegment, ev InAdvanceEvent) ([]*invoice.Fee, error) {
	sp, err := loadSegment(ctx, r, ps)
	if err != nil {
		return nil, err
	}
	org := ps.Segment.OrganizationID
	m, err := r.Metrics.Get(ctx, org, ps.Product.BillableMetricID)
	if err != nil {
		return nil, fmt.Errorf("load metric of product %s: %w", ps.Product.ID, err)
	}
	filters, err := r.ProductFilters.ListAll(ctx, org, ps.Product.ID)
	if err != nil {
		return nil, fmt.Errorf("list filters of product %s: %w", ps.Product.ID, err)
	}
	metric := kernel.MeteringMetricFilters(m)
	match := MatchProductFilter(filters, metric, ev.Event.Properties)
	var selected *catalog.ProductFilter
	if id := ps.RateCard.ProductFilterID; !id.IsZero() {
		if match == nil || match.ID != id {
			return nil, nil
		}
		selected = match
	} else if match != nil {
		return nil, nil
	}
	props, err := sp.properties()
	if err != nil {
		return nil, err
	}
	groups := metering.GroupsOf(ev.Event.Properties, props.Groups().PricingGroupKeys)
	ts := ev.Event.Timestamp.UTC()
	seg := ps.Segment
	req := metering.Request{
		Metric:         kernel.MeteringMetric(m),
		OrganizationID: org.String(),
		SubscriptionID: ps.Contract.ExternalID,
		From:           seg.StartedAt,
		To:             kernel.MeteringTo(seg.EndedAt),
		PeriodDays:     sp.days,
		Location:       ps.Location,
		Filter:         ProductFilterScope(filters, metric, selected),
		GroupValues:    groups,
		Prorated:       ps.RateCard.Proration,
		Dynamic:        seg.RateModel == catalog.ChargeModel(pricing.Dynamic),
		PayInAdvance:   true,
		Event:          &ev.Event,
		EventEstimate:  ev.Estimate,
		MaxTimestamp:   ts,
	}
	p, isPercentage := props.(pricing.PercentageProperties)
	if isPercentage {
		req.FreeUnitsPerEvents = int(p.FreeUnitsPerEvents)
		req.FreeUnitsPerTotalAggregation = deref(p.FreeUnitsPerTotalAggregation)
	}
	agg, err := metering.Aggregate(ctx, s.d.Events, req)
	if err != nil {
		return nil, fmt.Errorf("aggregate segment of %s for event %s: %w", ps.Card.ID, ev.Event.TransactionID, err)
	}
	ar := aggregationOf(agg)
	in := pricing.InAdvanceEvent{
		Persisted:               !ev.Estimate,
		Units:                   deref(agg.PayInAdvanceAggregation),
		PreciseTotalAmountCents: deref(agg.PayInAdvancePreciseTotalAmountCents),
		Value:                   deref(agg.PayInAdvanceAggregation),
	}
	if isPercentage && (p.PerTransactionMinAmount != nil || p.PerTransactionMaxAmount != nil) {
		pe, err := metering.PerEvent(ctx, s.d.Events, req, metering.PerEventOptions{GroupValues: groups})
		if err != nil {
			return nil, fmt.Errorf("per-event values of %s: %w", ps.Card.ID, err)
		}
		ar.PerEvent = &pricing.PerEventAggregation{Values: pe.Values}
		if in.Persisted {
			without, err := metering.PerEvent(ctx, s.d.Events, req, metering.PerEventOptions{GroupValues: groups, ExcludeEvent: true})
			if err != nil {
				return nil, fmt.Errorf("per-event values of %s: %w", ps.Card.ID, err)
			}
			in.ValuesWithoutEvent = without.Values
		}
	}
	ir, err := pricing.ApplyInAdvance(sp.structure(props), ar, in)
	if err != nil {
		return nil, fmt.Errorf("price event %s of %s: %w", ev.Event.TransactionID, ps.Card.ID, err)
	}
	amounts, err := pricing.ComputeAmounts(pricing.AmountsInput{Currency: sp.cur.fiat, InAdvance: &ir, PricingUnit: sp.cur.applied})
	if err != nil {
		return nil, fmt.Errorf("amounts of %s: %w", ps.Card.ID, err)
	}
	f := s.newProductFee(sp, &invoice.Invoice{})
	if selected != nil {
		f.ProductFilterID = selected.ID
	}
	setAmount(f, amounts.Amount, sp.cur.pricingUnit)
	f.Units, f.TotalAggregatedUnits = ir.Units, ir.Units
	f.EventsCount = ir.Count
	f.GroupedBy = groupedBy(groups)
	f.PayInAdvance = true
	f.Invoiceable = ps.RateCard.DisplayOnInvoice
	f.PayInAdvanceEventID = ev.ID
	f.PayInAdvanceEventTransactionID = ev.Event.TransactionID
	if f.AmountDetails, err = details(ir.Details); err != nil {
		return nil, err
	}
	if f.AmountDetails == nil {
		f.AmountDetails = []byte("{}")
	}
	return []*invoice.Fee{f}, nil
}

// MatchProductFilter returns the product filter an event with properties
// p is billed on: among the filters whose every key the event carries
// with one of the filter's values (a value left open standing for every
// value of the metric filter), the one with the most keys, ties keeping
// the first of filters; nil when none matches (lago
// Events::BillingPeriodFilters::EventMatchingService over a billing
// segment target).
//
// source: app/services/events/billing_period_filters/event_matching_service.rb:15-31, app/services/events/billing_period_filters/sources/billing_segment.rb:19-30
func MatchProductFilter(filters []*catalog.ProductFilter, metric []metering.MetricFilter, p metering.Properties) *catalog.ProductFilter {
	var best *catalog.ProductFilter
	bestKeys := -1
	for _, f := range filters {
		values := filterValues(f, metric)
		if !valuesMatch(values, p) {
			continue
		}
		if len(values) > bestKeys {
			best, bestKeys = f, len(values)
		}
	}
	return best
}
