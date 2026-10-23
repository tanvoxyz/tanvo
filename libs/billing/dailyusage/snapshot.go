package dailyusage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// head is the period and totals of a snapshot.
type head struct {
	from, to    time.Time
	issuingDate core.Date
	currency    money.Currency
	amount      int64
	taxes       int64
	total       int64
}

// catalogCache loads the charges and metrics the fees of the snapshots
// refer to, once each.
type catalogCache struct {
	r       port.Repos
	org     core.OrganizationID
	charges map[core.ChargeID]*catalog.Charge
	filters map[core.ChargeFilterID]*catalog.ChargeFilter
	metrics map[core.BillableMetricID]*catalog.BillableMetric
}

func newCatalogCache(r port.Repos, org core.OrganizationID) *catalogCache {
	return &catalogCache{
		r: r, org: org,
		charges: map[core.ChargeID]*catalog.Charge{},
		filters: map[core.ChargeFilterID]*catalog.ChargeFilter{},
		metrics: map[core.BillableMetricID]*catalog.BillableMetric{},
	}
}

// filter returns the charge filter id, kept or discarded (lago fee.rb:18,
// charge_filter with_discarded), nil when it is gone.
func (c *catalogCache) filter(ctx context.Context, id core.ChargeFilterID) (*catalog.ChargeFilter, error) {
	if f, ok := c.filters[id]; ok {
		return f, nil
	}
	f, err := c.r.Charges.GetFilter(ctx, c.org, id)
	switch {
	case errors.Is(err, core.ErrNotFound):
		f = nil
	case err != nil:
		return nil, fmt.Errorf("load charge filter %s: %w", id, err)
	}
	c.filters[id] = f
	return f, nil
}

// charge returns the charge id, kept or discarded (lago fee.rb:12, charge
// with_discarded), nil when it is gone.
func (c *catalogCache) charge(ctx context.Context, id core.ChargeID) (*catalog.Charge, error) {
	if ch, ok := c.charges[id]; ok {
		return ch, nil
	}
	ch, err := c.r.Charges.Get(ctx, c.org, id)
	switch {
	case errors.Is(err, core.ErrNotFound):
		ch = nil
	case err != nil:
		return nil, fmt.Errorf("load charge %s: %w", id, err)
	}
	c.charges[id] = ch
	return ch, nil
}

// metric returns the metric id, kept or discarded (lago fee.rb:33,
// billable_metric with_discarded), nil when it is gone.
func (c *catalogCache) metric(ctx context.Context, id core.BillableMetricID) (*catalog.BillableMetric, error) {
	if m, ok := c.metrics[id]; ok {
		return m, nil
	}
	m, err := c.r.Metrics.GetWithDiscarded(ctx, c.org, id)
	switch {
	case errors.Is(err, core.ErrNotFound):
		m = nil
	case err != nil:
		return nil, fmt.Errorf("load billable metric %s: %w", id, err)
	}
	c.metrics[id] = m
	return m, nil
}

// snapshot serializes the charge fees fees under h (lago
// V1::Customers::UsageSerializer with charges_usage and
// V1::Customers::ChargeUsageSerializer): the fees grouped by charge in
// order of first appearance, each with its filters, pricing groups and
// presentation breakdowns.
// The charge, its filters and its metric are read even when discarded
// since the fees were computed, as lago reads them
// (app/serializers/v1/customers/charge_usage_serializer.rb:63-100 through
// the with_discarded associations of app/models/fee.rb:12-33).
func snapshot(ctx context.Context, cc *catalogCache, h head, fees []*invoice.Fee) (subscription.UsageSnapshot, error) {
	out := subscription.UsageSnapshot{
		From: h.from, To: h.to, IssuingDate: h.issuingDate, Currency: h.currency.Code(),
		Amount: h.amount, TaxesAmount: h.taxes, TotalAmount: h.total,
		Charges: []subscription.ChargeUsage{},
	}
	for _, group := range groupBy(fees, func(f *invoice.Fee) core.ChargeID { return f.ChargeID }) {
		first := group[0]
		ch, err := cc.charge(ctx, first.ChargeID)
		if err != nil {
			return out, err
		}
		cu := subscription.ChargeUsage{
			UsageValues: values(group),
			Currency:    first.Currency.Code(),
			Charge:      subscription.ChargeRef{ID: first.ChargeID},
		}
		if ch != nil {
			cu.Charge.Code, cu.Charge.ChargeModel, cu.Charge.InvoiceDisplayName = ch.Code, string(ch.ChargeModel), ch.InvoiceDisplayName
			m, err := cc.metric(ctx, ch.BillableMetricID)
			if err != nil {
				return out, err
			}
			cu.BillableMetric.ID = ch.BillableMetricID
			if m != nil {
				cu.BillableMetric.Name, cu.BillableMetric.Code, cu.BillableMetric.AggregationType = m.Name, m.Code, string(m.AggregationType)
			}
		}
		if cu.Filters, err = filters(ctx, cc, group, ch); err != nil {
			return out, err
		}
		if cu.Grouped, err = grouped(ctx, cc, group, ch); err != nil {
			return out, err
		}
		cu.PresentationBreakdowns = breakdowns(group, ungroupedFees)
		out.Charges = append(out.Charges, cu)
	}
	return out, nil
}

// values sums the quantities of fees (charge_usage_serializer.rb:24-59):
// the pricing unit is the first fee's, with the amounts of every fee that
// has one.
func values(fees []*invoice.Fee) subscription.UsageValues {
	v := subscription.UsageValues{Units: money.Zero, TotalAggregatedUnits: money.Zero}
	for _, f := range fees {
		v.Units = v.Units.Add(f.Units)
		v.TotalAggregatedUnits = v.TotalAggregatedUnits.Add(f.TotalAggregatedUnits)
		v.EventsCount += f.EventsCount
		v.Amount += f.Amount
	}
	if pu := fees[0].PricingUnitUsage; pu != nil {
		p := &subscription.PricingUnitUsage{Amount: money.Zero, ShortName: pu.ShortName, ConversionRate: pu.ConversionRate}
		for _, f := range fees {
			if f.PricingUnitUsage != nil {
				p.Amount = p.Amount.Add(f.PricingUnitUsage.Amount)
			}
		}
		v.PricingUnit = p
	}
	return v
}

// filters returns the usage per charge filter of fees, the default bucket
// included, when the charge has kept filters (charge_usage_serializer.rb:82-100);
// a fee's filter is read even when discarded (fee.rb:18).
func filters(ctx context.Context, cc *catalogCache, fees []*invoice.Fee, ch *catalog.Charge) ([]subscription.FilterUsage, error) {
	out := []subscription.FilterUsage{}
	if ch == nil || len(ch.Filters) == 0 {
		return out, nil
	}
	for _, group := range groupBy(fees, func(f *invoice.Fee) core.ChargeFilterID { return f.ChargeFilterID }) {
		fu := subscription.FilterUsage{UsageValues: values(group), PresentationBreakdowns: breakdowns(group, allFees)}
		if id := group[0].ChargeFilterID; !id.IsZero() {
			cf, err := cc.filter(ctx, id)
			if err != nil {
				return nil, err
			}
			if cf != nil {
				fu.InvoiceDisplayName, fu.Values = cf.InvoiceDisplayName, cf.Values
			}
		}
		out = append(out, fu)
	}
	return out, nil
}

// grouped returns the usage per pricing group of fees when one of them has
// a group (charge_usage_serializer.rb:102-118).
func grouped(ctx context.Context, cc *catalogCache, fees []*invoice.Fee, ch *catalog.Charge) ([]subscription.GroupedUsage, error) {
	out := []subscription.GroupedUsage{}
	if !anyGrouped(fees) {
		return out, nil
	}
	for _, group := range groupBy(fees, func(f *invoice.Fee) string { return groupKey(f.GroupedBy) }) {
		fs, err := filters(ctx, cc, group, ch)
		if err != nil {
			return nil, err
		}
		out = append(out, subscription.GroupedUsage{
			UsageValues:            values(group),
			GroupedBy:              group[0].GroupedBy,
			Filters:                fs,
			PresentationBreakdowns: breakdowns(group, groupedFees),
		})
	}
	return out, nil
}

func anyGrouped(fees []*invoice.Fee) bool {
	for _, f := range fees {
		if len(f.GroupedBy) > 0 {
			return true
		}
	}
	return false
}

// feeSelection selects the fees whose presentation breakdowns are listed
// (lago V1::Customers::PresentationBreakdownBuilder ALL, UNGROUPED and
// GROUPED).
type feeSelection int

const (
	allFees feeSelection = iota
	ungroupedFees
	groupedFees
)

// breakdowns lists the presentation breakdowns of the fees sel selects
// (app/serializers/v1/customers/presentation_breakdown_builder.rb:22-35).
func breakdowns(fees []*invoice.Fee, sel feeSelection) []subscription.PresentationBreakdownUsage {
	out := []subscription.PresentationBreakdownUsage{}
	for _, f := range fees {
		isGrouped, filtered := len(f.GroupedBy) > 0, !f.ChargeFilterID.IsZero()
		if sel == ungroupedFees && (isGrouped || filtered) || sel == groupedFees && (!isGrouped || filtered) {
			continue
		}
		for _, b := range invoice.ClonePresentationBreakdowns(f.PresentationBreakdowns) {
			out = append(out, subscription.PresentationBreakdownUsage{PresentationBy: b.PresentationBy, Units: b.Units})
		}
	}
	return out
}

// groupBy splits s by key, in order of first appearance (Ruby group_by).
func groupBy[T any, K comparable](s []T, key func(T) K) [][]T {
	index := map[K]int{}
	var out [][]T
	for _, v := range s {
		k := key(v)
		i, ok := index[k]
		if !ok {
			i = len(out)
			index[k] = i
			out = append(out, nil)
		}
		out[i] = append(out[i], v)
	}
	return out
}

// groupKey returns the canonical form of a group's values.
func groupKey(by map[string]string) string {
	if len(by) == 0 {
		return ""
	}
	b, err := json.Marshal(by)
	if err != nil {
		panic(err) // a map of strings always encodes
	}
	return string(b)
}

// nonZero keeps the fees with units, an amount or events (lago
// Fee#non_zero?, app/models/fee.rb:334-336).
func nonZero(fees []*invoice.Fee) []*invoice.Fee {
	var out []*invoice.Fee
	for _, f := range fees {
		if f.NonZero() {
			out = append(out, f)
		}
	}
	return out
}
