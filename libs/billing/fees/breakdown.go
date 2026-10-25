package fees

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/period"
	"github.com/tanvoxyz/tanvo/libs/pricing"
)

// HasRecurringBreakdown reports whether an invoice document prints the
// daily breakdown of the charge fee f of charge c and metric m: a charge
// billed in arrears on a recurring sum or unique count metric (lago
// Invoice#recurring_fees, app/models/invoice.rb:335-341).
func HasRecurringBreakdown(f *invoice.Fee, c *catalog.Charge, m *catalog.BillableMetric) bool {
	if f.Type != core.FeeTypeCharge || c == nil || m == nil || !m.Recurring || c.PayInAdvance || f.SubscriptionID.IsZero() {
		return false
	}
	return m.AggregationType == catalog.AggregationTypeSum || m.AggregationType == catalog.AggregationTypeUniqueCount
}

// RecurringBreakdown returns the day-by-day breakdown of the units of the
// recurring charge fee f over its charges period, restricted to its charge
// filter (lago Invoice#recurring_breakdown, app/models/invoice.rb:343-385,
// with BillableMetrics::Breakdown::SumService and UniqueCountService). It
// reads the subscription as it is now, as lago does when the document is
// rendered. A fee without a breakdown ([HasRecurringBreakdown]), or whose
// charge filter is no longer kept, has none.
func (s *Service) RecurringBreakdown(ctx context.Context, r port.Repos, f *invoice.Fee, c *catalog.Charge, m *catalog.BillableMetric) ([]metering.BreakdownItem, error) {
	if !HasRecurringBreakdown(f, c, m) || f.Properties.ChargesFrom.IsZero() || f.Properties.ChargesTo.IsZero() {
		return nil, nil
	}
	env, err := kernel.LoadSubscriptionEnv(ctx, r, f.OrganizationID, f.SubscriptionID)
	if err != nil {
		return nil, fmt.Errorf("breakdown of fee %s: %w", f.ID, err)
	}
	cc := &chargeContext{charge: c, metric: m, filters: kernel.MeteringChargeFilters(c), metricF: kernel.MeteringMetricFilters(m)}
	filterID := ""
	if !f.ChargeFilterID.IsZero() {
		kept := false
		for i := range c.Filters {
			kept = kept || c.Filters[i].ID == f.ChargeFilterID
		}
		if !kept {
			return nil, nil
		}
		filterID = f.ChargeFilterID.String()
	}
	scope, err := metering.ChargeFilterScope(cc.filters, cc.metricF, filterID)
	if err != nil {
		return nil, fmt.Errorf("breakdown of fee %s: %w", f.ID, err)
	}
	p := period.Period{From: f.Properties.ChargesFrom, To: f.Properties.ChargesTo}
	req := kernel.MeteringRequest(env, m, p, f.Properties.ChargesDurationDays)
	req.Filter = scope
	items, err := metering.Breakdown(ctx, s.d.Events, req)
	if err != nil {
		return nil, fmt.Errorf("breakdown of fee %s: %w", f.ID, err)
	}
	return items, nil
}

// groupKeyOf is a canonical form of a fee's pricing group values; nil and
// empty are the same group.
func groupKeyOf(g map[string]string) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(g)) {
		fmt.Fprintf(&b, "%q=%q;", k, g[k])
	}
	return b.String()
}

// breakdownsByGroup splits the presentation breakdowns of an aggregation
// by the pricing group of each model result: a group gets the breakdowns
// whose group values are its own, without its group keys (lago
// Fees::ChargeService#breakdowns_by_grouped_by).
//
// source: app/services/fees/charge_service.rb:226-238
func breakdownsByGroup(bs []metering.GroupedValue, results []pricing.Result) map[string][]invoice.PresentationBreakdown {
	out := map[string][]invoice.PresentationBreakdown{}
	for _, gr := range results {
		key := groupKeyOf(gr.GroupedBy)
		if _, ok := out[key]; ok {
			continue
		}
		list := []invoice.PresentationBreakdown{}
		for _, b := range bs {
			if !groupMatches(b.Groups, gr.GroupedBy) {
				continue
			}
			by := map[string]*string{}
			for k, v := range b.Groups {
				if _, grouped := gr.GroupedBy[k]; !grouped {
					by[k] = v
				}
			}
			list = append(list, invoice.PresentationBreakdown{PresentationBy: by, Units: b.Value})
		}
		out[key] = invoice.ClonePresentationBreakdowns(list)
	}
	return out
}

// groupMatches reports whether the groups g, restricted to the keys of
// grouped, equal grouped (Hash#slice ==): a missing key differs from a
// present one.
func groupMatches(g metering.Groups, grouped map[string]string) bool {
	for k, want := range grouped {
		v, ok := g[k]
		if !ok {
			return false
		}
		got := ""
		if v != nil {
			got = *v
		}
		if got != want {
			return false
		}
	}
	return true
}

// meteringBreakdowns converts fee breakdowns for a cached aggregation.
func meteringBreakdowns(bs []invoice.PresentationBreakdown) []metering.GroupedValue {
	if len(bs) == 0 {
		return nil
	}
	out := make([]metering.GroupedValue, len(bs))
	for i, b := range invoice.ClonePresentationBreakdowns(bs) {
		out[i] = metering.GroupedValue{Groups: metering.Groups(b.PresentationBy), Value: b.Units}
	}
	return out
}

// withoutGroupKeys returns the breakdowns bs with the keys of groups
// removed (lago CreatePayInAdvanceService#remove_formated_grouped_by_keys).
//
// source: app/services/fees/create_pay_in_advance_service.rb:192-196
func withoutGroupKeys(bs []metering.GroupedValue, groups metering.Groups) []invoice.PresentationBreakdown {
	var out []invoice.PresentationBreakdown
	for _, b := range bs {
		by := map[string]*string{}
		for k, v := range b.Groups {
			if _, grouped := groups[k]; !grouped {
				by[k] = v
			}
		}
		out = append(out, invoice.PresentationBreakdown{PresentationBy: by, Units: b.Value})
	}
	return invoice.ClonePresentationBreakdowns(out)
}
