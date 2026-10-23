package subscriptions

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// CodeNoActiveSubscription: the customer has no active subscription with
// that reference.
const CodeNoActiveSubscription = "no_active_subscription"

// UsageValues are the totals of a set of usage fees.
type UsageValues struct {
	// Units is Σ units.
	Units money.Decimal
	// Amount is Σ amounts, in minor units.
	Amount int64
	// EventsCount is Σ events counts.
	EventsCount int64
	// PricingUnitAmount is Σ amounts in the charge's pricing unit; nil
	// when the charge has none.
	PricingUnitAmount *money.Decimal
	// ProjectedUnits is Σ projected units; nil without projection.
	ProjectedUnits *money.Decimal
	// ProjectedAmount is Σ projected amounts; nil without projection.
	ProjectedAmount *int64
}

// GroupedUsage is the usage of one pricing group.
type GroupedUsage struct {
	UsageValues
	// GroupedBy are the group's values.
	GroupedBy map[string]string
}

// FilterUsage is the usage of one charge filter.
type FilterUsage struct {
	UsageValues
	// Filter is the charge filter; nil for the events no filter matched.
	Filter *catalog.ChargeFilter
	// Grouped is the filter's usage per pricing group; empty when no fee
	// is grouped.
	Grouped []GroupedUsage
}

// ChargeUsage is the usage of one charge.
type ChargeUsage struct {
	UsageValues
	// Charge is the charge.
	Charge *catalog.Charge
	// Metric is its billable metric.
	Metric *catalog.BillableMetric
	// Filters is the usage per filter; empty when the charge has none.
	Filters []FilterUsage
	// Grouped is the usage per pricing group; empty when no fee is
	// grouped.
	Grouped []GroupedUsage
}

// PeriodUsage is the usage of one billing period of a subscription.
type PeriodUsage struct {
	// Subscription is the subscription.
	Subscription *subscription.Subscription
	// InvoiceID is the invoice of a past period; zero for the current one.
	InvoiceID core.InvoiceID
	// From and To are the charges period.
	From, To time.Time
	// IssuingDate is the date the period is invoiced.
	IssuingDate core.Date
	// Currency is the currency of the amounts.
	Currency money.Currency
	// Amount, TaxesAmount and TotalAmount are the totals, in minor units.
	Amount, TaxesAmount, TotalAmount int64
	// ProjectedAmount is the projected amount; nil without projection.
	ProjectedAmount *int64
	// Charges is the usage per charge, in fee order.
	Charges []ChargeUsage
}

// CurrentUsageInput is the request of [Service.CurrentUsage].
type CurrentUsageInput struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// Customer is the customer.
	Customer CustomerRef
	// Subscription is one of the customer's active subscriptions.
	Subscription SubscriptionRef
	// SkipTaxes leaves the taxes out.
	SkipTaxes bool
	// Projection adds the projection to the period end.
	Projection bool
	// BillableMetricCode, when set, keeps the charges of that metric.
	BillableMetricCode string
}

// CurrentUsage computes the usage of the current period of a customer's
// active subscription (lago Invoices::CustomerUsageService with its
// ChargeUsageSerializer, and ProjectedUsageSerializer with a projection).
//
// source: app/controllers/api/v1/customers/usage_controller.rb:7, app/controllers/api/v1/customers/projected_usage_controller.rb:7, app/services/invoices/customer_usage_service.rb
func (s *Service) CurrentUsage(ctx context.Context, in CurrentUsageInput) (*PeriodUsage, error) {
	org := in.OrganizationID
	var out *PeriodUsage
	err := s.read(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := findCustomer(ctx, r, org, in.Customer)
		if err != nil {
			return err
		}
		sub, err := activeSubscriptionOf(ctx, r, org, c.ID, in.Subscription)
		if err != nil {
			return err
		}
		env, err := kernel.LoadSubscriptionEnvOf(ctx, r, sub)
		if err != nil {
			return err
		}
		opts := usage.Options{SkipTaxes: in.SkipTaxes, Projection: in.Projection}
		if in.BillableMetricCode != "" {
			// customer_usage_service.rb:60: no charge of the metric is a
			// missing charge.
			charges, err := env.Charges(ctx, r)
			if err != nil {
				return err
			}
			m, err := r.Metrics.GetByCode(ctx, org, in.BillableMetricCode)
			if err != nil && !errors.Is(err, core.ErrNotFound) {
				return fmt.Errorf("load billable metric %q: %w", in.BillableMetricCode, err)
			}
			for _, ch := range charges {
				if m != nil && ch.BillableMetricID == m.ID {
					opts.ChargeIDs = append(opts.ChargeIDs, ch.ID)
				}
			}
			if len(opts.ChargeIDs) == 0 {
				return core.NotFound(ResourceCharge)
			}
		}
		u, err := s.usage.EnvUsage(ctx, r, env, opts)
		if errors.Is(err, usage.ErrNoActiveSubscription) {
			return noActiveSubscription()
		}
		if err != nil {
			return err
		}
		out = &PeriodUsage{
			Subscription: sub, From: u.From, To: u.To, IssuingDate: u.IssuingDate, Currency: u.Currency,
			Amount: u.Amount, TaxesAmount: u.TaxesAmount, TotalAmount: u.TotalAmount,
		}
		if in.Projection {
			var total int64
			for _, p := range u.Projections {
				total += p.Amount
			}
			out.ProjectedAmount = &total
		}
		out.Charges, err = chargesUsage(ctx, r, org, u.Fees, u.Projections)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("current usage: %w", err)
	}
	return out, nil
}

// PastUsageInput is the request of [Service.PastUsage].
type PastUsageInput struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// Customer is the customer.
	Customer CustomerRef
	// Subscription names the subscription chain: by external ID, or by
	// the ID of one of its subscriptions.
	Subscription SubscriptionRef
	// BillableMetricCode, when set, keeps the fees of that metric.
	BillableMetricCode string
	// PeriodsCount keeps the latest periods only; 0 keeps them all.
	PeriodsCount int
	// Page is the page.
	Page core.PageRequest
}

// PastUsage lists the invoiced usage periods of a subscription chain,
// latest first (lago PastUsageQuery and PastUsageSerializer). An unknown
// customer has no periods.
//
// source: app/controllers/api/v1/customers/usage_controller.rb:33, app/queries/past_usage_query.rb, app/serializers/v1/customers/past_usage_serializer.rb
func (s *Service) PastUsage(ctx context.Context, in PastUsageInput) (core.Page[*PeriodUsage], error) {
	org := in.OrganizationID
	var out core.Page[*PeriodUsage]
	err := s.read(ctx, func(ctx context.Context, r port.Repos) error {
		var metric core.BillableMetricID
		if in.BillableMetricCode != "" {
			m, err := r.Metrics.GetByCode(ctx, org, in.BillableMetricCode)
			if errors.Is(err, core.ErrNotFound) {
				return core.NotFound(ResourceBillableMetric)
			}
			if err != nil {
				return fmt.Errorf("load billable metric %q: %w", in.BillableMetricCode, err)
			}
			metric = m.ID
		}
		c, err := findCustomer(ctx, r, org, in.Customer)
		var nf *core.NotFoundError
		if errors.As(err, &nf) {
			return nil
		}
		if err != nil {
			return err
		}
		externalID := in.Subscription.ExternalID
		if externalID == "" {
			sub, err := findSubscription(ctx, r, org, in.Subscription)
			if errors.As(err, &nf) {
				return nil
			}
			if err != nil {
				return err
			}
			externalID = sub.ExternalID
		}
		periods, err := s.usage.PastUsage(ctx, r, org, usage.PastUsageFilter{
			CustomerID: c.ID, ExternalSubscriptionID: externalID, PeriodsCount: in.PeriodsCount, BillableMetricID: metric,
		})
		if err != nil {
			return err
		}
		start, err := offsetOf(in.Page.Token)
		if err != nil {
			return err
		}
		start = min(start, len(periods))
		end := min(start+in.Page.Limit(), len(periods))
		if end < len(periods) {
			out.NextToken = offsetToken(end)
		}
		for _, p := range periods[start:end] {
			pu, err := pastPeriodUsage(ctx, r, org, p)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, pu)
		}
		return nil
	})
	if err != nil {
		return core.Page[*PeriodUsage]{}, fmt.Errorf("past usage: %w", err)
	}
	return out, nil
}

// pastPeriodUsage builds the usage of one invoiced period: the invoice's
// fees amount, the taxes of all its fees (past_usage_serializer.rb:27).
func pastPeriodUsage(ctx context.Context, r port.Repos, org core.OrganizationID, p usage.PastPeriod) (*PeriodUsage, error) {
	sub, err := r.Subscriptions.Get(ctx, org, p.InvoiceSubscription.SubscriptionID)
	if err != nil {
		return nil, fmt.Errorf("load subscription %s: %w", p.InvoiceSubscription.SubscriptionID, err)
	}
	fees, err := r.Fees.ListByInvoice(ctx, org, p.Invoice.ID)
	if err != nil {
		return nil, fmt.Errorf("list fees of invoice %s: %w", p.Invoice.ID, err)
	}
	var taxes int64
	for _, f := range fees {
		taxes += f.TaxesAmount
	}
	pu := &PeriodUsage{
		Subscription: sub, InvoiceID: p.Invoice.ID,
		From: p.InvoiceSubscription.ChargesPeriod.From, To: p.InvoiceSubscription.ChargesPeriod.To,
		IssuingDate: p.Invoice.IssuingDate, Currency: p.Invoice.Currency,
		Amount: p.Invoice.FeesAmount, TaxesAmount: taxes, TotalAmount: p.Invoice.FeesAmount + taxes,
	}
	if pu.Charges, err = chargesUsage(ctx, r, org, p.Fees, nil); err != nil {
		return nil, err
	}
	return pu, nil
}

// activeSubscriptionOf returns the customer's active subscription ref
// designates; none is the rule error no_active_subscription
// (customer_usage_service.rb:47).
func activeSubscriptionOf(ctx context.Context, r port.Repos, org core.OrganizationID, cust core.CustomerID, ref SubscriptionRef) (*subscription.Subscription, error) {
	subs, err := r.Subscriptions.ListByCustomer(ctx, org, cust, subscription.StatusActive)
	if err != nil {
		return nil, fmt.Errorf("list active subscriptions of %s: %w", cust, err)
	}
	for _, sub := range subs {
		if (ref.ExternalID != "" && sub.ExternalID == ref.ExternalID) || (ref.ExternalID == "" && sub.ID == ref.ID) {
			return sub, nil
		}
	}
	return nil, noActiveSubscription()
}

func noActiveSubscription() error {
	return core.NewRuleError(CodeNoActiveSubscription, "the customer has no active subscription with this reference")
}

// usageLoader loads and caches the charges, metrics and filters of fees.
type usageLoader struct {
	r        port.Repos
	org      core.OrganizationID
	charges  map[core.ChargeID]*catalog.Charge
	metrics  map[core.BillableMetricID]*catalog.BillableMetric
	filters  map[core.ChargeFilterID]*catalog.ChargeFilter
	filtered map[core.ChargeID]bool
}

func (l *usageLoader) charge(ctx context.Context, id core.ChargeID) (*catalog.Charge, *catalog.BillableMetric, bool, error) {
	c, ok := l.charges[id]
	if !ok {
		var err error
		if c, err = l.r.Charges.Get(ctx, l.org, id); err != nil {
			return nil, nil, false, fmt.Errorf("load charge %s: %w", id, err)
		}
		l.charges[id] = c
		fs, err := l.r.Charges.ListFilters(ctx, l.org, id)
		if err != nil {
			return nil, nil, false, fmt.Errorf("list filters of charge %s: %w", id, err)
		}
		l.filtered[id] = len(fs) > 0
		for _, f := range fs {
			l.filters[f.ID] = f
		}
	}
	m, ok := l.metrics[c.BillableMetricID]
	if !ok {
		var err error
		if m, err = l.r.Metrics.Get(ctx, l.org, c.BillableMetricID); err != nil {
			return nil, nil, false, fmt.Errorf("load billable metric %s: %w", c.BillableMetricID, err)
		}
		l.metrics[m.ID] = m
	}
	return c, m, l.filtered[id], nil
}

func (l *usageLoader) filter(ctx context.Context, id core.ChargeFilterID) (*catalog.ChargeFilter, error) {
	if id.IsZero() {
		return nil, nil
	}
	if f, ok := l.filters[id]; ok {
		return f, nil
	}
	// A filter deleted since the fee was billed.
	f, err := l.r.Charges.GetFilter(ctx, l.org, id)
	if errors.Is(err, core.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load charge filter %s: %w", id, err)
	}
	l.filters[id] = f
	return f, nil
}

// usageFee is a fee with its projection.
type usageFee struct {
	fee  *invoice.Fee
	proj *usage.Projection
}

// chargesUsage breaks fees down per charge, then per filter (when the
// charge has filters) and per pricing group (when a fee is grouped), in
// first-fee order (lago V1::Customers::ChargeUsageSerializer).
// projections, when not nil, are index-aligned with fees.
//
// source: app/serializers/v1/customers/charge_usage_serializer.rb, app/serializers/v1/customers/projected_charge_usage_serializer.rb
func chargesUsage(ctx context.Context, r port.Repos, org core.OrganizationID, fees []*invoice.Fee, projections []usage.Projection) ([]ChargeUsage, error) {
	l := &usageLoader{
		r: r, org: org, charges: map[core.ChargeID]*catalog.Charge{}, metrics: map[core.BillableMetricID]*catalog.BillableMetric{},
		filters: map[core.ChargeFilterID]*catalog.ChargeFilter{}, filtered: map[core.ChargeID]bool{},
	}
	all := make([]usageFee, 0, len(fees))
	for i, f := range fees {
		if f.ChargeID.IsZero() {
			continue
		}
		uf := usageFee{fee: f}
		if projections != nil {
			uf.proj = &projections[i]
		}
		all = append(all, uf)
	}
	var out []ChargeUsage
	for _, group := range groupBy(all, func(f usageFee) core.ChargeID { return f.fee.ChargeID }) {
		c, m, filtered, err := l.charge(ctx, group[0].fee.ChargeID)
		if err != nil {
			return nil, err
		}
		cu := ChargeUsage{UsageValues: totals(group), Charge: c, Metric: m, Grouped: groupedUsage(group)}
		if filtered {
			for _, fg := range groupBy(group, func(f usageFee) core.ChargeFilterID { return f.fee.ChargeFilterID }) {
				cf, err := l.filter(ctx, fg[0].fee.ChargeFilterID)
				if err != nil {
					return nil, err
				}
				cu.Filters = append(cu.Filters, FilterUsage{UsageValues: totals(fg), Filter: cf, Grouped: groupedUsage(fg)})
			}
		}
		out = append(out, cu)
	}
	return out, nil
}

// groupedUsage returns the usage per pricing group of fees, or nil when no
// fee is grouped (charge_usage_serializer.rb:115).
func groupedUsage(fees []usageFee) []GroupedUsage {
	if !slices.ContainsFunc(fees, func(f usageFee) bool { return len(f.fee.GroupedBy) > 0 }) {
		return nil
	}
	var out []GroupedUsage
	for _, g := range groupBy(fees, func(f usageFee) string { return groupKey(f.fee.GroupedBy) }) {
		out = append(out, GroupedUsage{UsageValues: totals(g), GroupedBy: g[0].fee.GroupedBy})
	}
	return out
}

// totals sums the usage of fees.
func totals(fees []usageFee) UsageValues {
	var v UsageValues
	projected := false
	var pUnits money.Decimal
	var pAmount int64
	for _, f := range fees {
		v.Units = v.Units.Add(f.fee.Units)
		v.Amount += f.fee.Amount
		v.EventsCount += f.fee.EventsCount
		if pu := f.fee.PricingUnitUsage; pu != nil {
			sum := pu.Amount
			if v.PricingUnitAmount != nil {
				sum = v.PricingUnitAmount.Add(sum)
			}
			v.PricingUnitAmount = &sum
		}
		if f.proj != nil {
			projected = true
			pUnits = pUnits.Add(f.proj.Units)
			pAmount += f.proj.Amount
		}
	}
	if projected {
		v.ProjectedUnits, v.ProjectedAmount = &pUnits, &pAmount
	}
	return v
}

// groupBy splits items by key, in the order each key first appears.
func groupBy[K comparable, T any](items []T, key func(T) K) [][]T {
	idx := map[K]int{}
	var out [][]T
	for _, it := range items {
		k := key(it)
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, nil)
		}
		out[i] = append(out[i], it)
	}
	return out
}

// groupKey is a canonical form of a pricing group's values.
func groupKey(g map[string]string) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(g)) {
		fmt.Fprintf(&b, "%q=%q;", k, g[k])
	}
	return b.String()
}

// offsetToken is the page token of the periods from offset n on.
func offsetToken(n int) string {
	return base64.RawURLEncoding.EncodeToString([]byte("offset:" + strconv.Itoa(n)))
}

// offsetOf decodes a page token of offsetToken; "" is offset 0.
func offsetOf(token string) (int, error) {
	if token == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err == nil {
		if s, ok := strings.CutPrefix(string(b), "offset:"); ok {
			if n, err := strconv.Atoi(s); err == nil && n >= 0 {
				return n, nil
			}
		}
	}
	return 0, fmt.Errorf("%w: page token", core.ErrInvalidValue)
}
