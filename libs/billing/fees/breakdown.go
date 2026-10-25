package fees

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/period"
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
