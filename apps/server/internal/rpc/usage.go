package rpc

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/libs/billing/subscriptions"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
	usagev1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/usage/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/usage/v1/usagev1connect"
)

// Usage serves tanvo.usage.v1.UsageService over the billing library's
// subscription service (lago Api::V1::Customers::UsageController and
// Api::V1::Customers::ProjectedUsageController, and the
// subscriptionHourlyUsage GraphQL query). ListDailyUsage has no
// implementation: the daily usage has no API.
type Usage struct {
	usagev1connect.UnimplementedUsageServiceHandler

	svc *subscriptions.Service
}

var _ usagev1connect.UsageServiceHandler = (*Usage)(nil)

// NewUsage returns the service over svc.
func NewUsage(svc *subscriptions.Service) *Usage { return &Usage{svc: svc} }

// GetCurrentUsage implements UsageService.GetCurrentUsage (lago
// UsageController#current, Invoices::CustomerUsageService): taxes apply
// unless apply_taxes is false, and include_projection adds the projected
// units and amounts. charge_id, charge_code and billable_metric_code
// restrict the charges, filter_by_group, filter_by_presentation,
// skip_grouping and full_usage narrow the usage (lago UsageFilters).
func (s *Usage) GetCurrentUsage(ctx context.Context, req *usagev1.GetCurrentUsageRequest) (*usagev1.GetCurrentUsageResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	in := subscriptions.CurrentUsageInput{
		OrganizationID:     p.OrganizationID,
		SkipTaxes:          req.HasApplyTaxes() && !req.GetApplyTaxes(),
		Projection:         req.GetIncludeProjection(),
		ChargeCode:         req.GetChargeCode(),
		BillableMetricCode: req.GetBillableMetricCode(),
		GroupFilter:        groupFilter(req.GetFilterByGroup()),
		SkipGrouping:       req.GetSkipGrouping(),
		FullUsage:          req.GetFullUsage(),
	}
	if keys := req.GetFilterByPresentation(); len(keys) > 0 {
		in.PresentationFilter = keys
	}
	if id := req.GetChargeId(); id != "" {
		if in.ChargeID, err = ParseID[core.ChargeID]("charge_id", id); err != nil {
			return nil, err
		}
	}
	if in.Customer, err = subCustomerRef("customer_id", req.GetCustomerId()); err != nil {
		return nil, err
	}
	if in.Subscription, err = subRef("subscription_id", req.GetSubscriptionId()); err != nil {
		return nil, err
	}
	u, err := s.svc.CurrentUsage(ctx, in)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return usagev1.GetCurrentUsageResponse_builder{Usage: usageProto(u)}.Build(), nil
}

// GetHourlyUsage implements UsageService.GetHourlyUsage (lago
// subscriptionHourlyUsage query, RealtimeUsage::HourlyBreakdownService):
// the usage of a count or sum charge per hour of the customer's time
// zone, by charge filter. A charge the usage buckets do not serve is
// PermissionDenied feature_unavailable, as lago's forbidden failure.
//
// source: app/graphql/resolvers/subscriptions/hourly_usage_resolver.rb, app/graphql/types/subscriptions/realtime_usage/hourly.rb
func (s *Usage) GetHourlyUsage(ctx context.Context, req *usagev1.GetHourlyUsageRequest) (*usagev1.GetHourlyUsageResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	in := subscriptions.HourlyUsageInput{OrganizationID: p.OrganizationID}
	if req.HasFrom() {
		in.From = req.GetFrom().AsTime()
	}
	if req.HasTo() {
		in.To = req.GetTo().AsTime()
	}
	if in.ChargeID, err = ParseID[core.ChargeID]("charge_id", req.GetChargeId()); err != nil {
		return nil, err
	}
	if in.Customer, err = subCustomerRef("customer_id", req.GetCustomerId()); err != nil {
		return nil, err
	}
	if in.Subscription, err = subRef("subscription_id", req.GetSubscriptionId()); err != nil {
		return nil, err
	}
	u, err := s.svc.HourlyUsage(ctx, in)
	if errors.Is(err, subscriptions.ErrHourlyUnavailable) {
		return nil, newError(connect.CodePermissionDenied, ReasonFeatureUnavailable, "the usage buckets do not serve the hourly usage of this charge")
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	return hourlyUsageProto(u), nil
}

// hourlyUsageProto converts an hourly usage (lago
// Types::Subscriptions::RealtimeUsage::Hourly); the charge default and the
// folded series have no charge filter.
func hourlyUsageProto(u *subscriptions.HourlyUsage) *usagev1.GetHourlyUsageResponse {
	r := u.Usage
	filterID := func(key string) *string {
		if key == "" || key == metering.HourlyOtherKey {
			return nil
		}
		return new(key)
	}
	b := usagev1.GetHourlyUsageResponse_builder{
		From:            Timestamp(r.From),
		To:              Timestamp(r.To),
		Timezone:        new(r.Location),
		AggregationType: new(aggregationTypes.Proto(u.Metric.AggregationType)),
	}
	if !r.LastIngestedAt.IsZero() {
		b.LastIngestedAt = Timestamp(r.LastIngestedAt)
	}
	for _, sr := range r.Series {
		fb := usagev1.HourlyUsageFilter_builder{
			ChargeFilterId: filterID(sr.Key),
			Units:          new(Decimal(sr.Units)),
			EventsCount:    new(sr.EventsCount),
			Other:          new(sr.Other),
		}
		if f := u.Filters[sr.Key]; f != nil && !sr.Other {
			fb.InvoiceDisplayName = f.InvoiceDisplayName
			fb.Values = make(map[string]string, len(f.Values))
			for k, vs := range f.Values {
				fb.Values[k] = strings.Join(vs, ",")
			}
		}
		b.Filters = append(b.Filters, fb.Build())
	}
	for _, h := range r.Hours {
		pb := usagev1.HourlyUsagePoint_builder{
			Time:        Timestamp(h.Time),
			Units:       new(Decimal(h.Units)),
			EventsCount: new(h.EventsCount),
		}
		for _, us := range h.Usages {
			pb.Breakdown = append(pb.Breakdown, usagev1.HourlyUsageBreakdown_builder{
				ChargeFilterId: filterID(us.Key),
				Units:          new(Decimal(us.Units)),
				EventsCount:    new(us.EventsCount),
				Other:          new(us.Other),
			}.Build())
		}
		b.Hours = append(b.Hours, pb.Build())
	}
	return b.Build()
}

// groupFilter parses the `<key>:<value>` entries of filter_by_group
// into the values of each key (lago filter_by_group, whose values are
// arrays); nil without entries.
func groupFilter(entries []string) map[string][]string {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string][]string, len(entries))
	for _, e := range entries {
		k, v, _ := strings.Cut(e, ":")
		if !slices.Contains(out[k], v) {
			out[k] = append(out[k], v)
		}
	}
	return out
}

// ListPastUsage implements UsageService.ListPastUsage (lago
// UsageController#past, PastUsageQuery): the usage of the invoiced
// periods, most recent first; an unknown customer or subscription lists
// nothing.
func (s *Usage) ListPastUsage(ctx context.Context, req *usagev1.ListPastUsageRequest) (*usagev1.ListPastUsageResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	in := subscriptions.PastUsageInput{
		OrganizationID:     p.OrganizationID,
		BillableMetricCode: req.GetBillableMetricCode(),
		PeriodsCount:       int(req.GetPeriodsCount()),
		Page:               Page(req.GetPageSize(), req.GetPageToken()),
	}
	if in.Customer, err = subCustomerRef("customer_id", req.GetCustomerId()); err != nil {
		return nil, err
	}
	if in.Subscription, err = subRef("subscription_id", req.GetSubscriptionId()); err != nil {
		return nil, err
	}
	page, err := s.svc.PastUsage(ctx, in)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return usagev1.ListPastUsageResponse_builder{
		Usages:        Items(page, usageProto),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// usageProto converts the usage of a period (lago
// V1::Customers::UsageSerializer and PastUsageSerializer).
func usageProto(u *subscriptions.PeriodUsage) *usagev1.Usage {
	cur := u.Currency
	b := usagev1.Usage_builder{
		SubscriptionId:         new(u.Subscription.ID.String()),
		ExternalSubscriptionId: new(u.Subscription.ExternalID),
		From:                   Timestamp(u.From),
		To:                     Timestamp(u.To),
		IssuingAt:              Timestamp(u.IssuingDate.Midnight(time.UTC)),
		InvoiceId:              subID(u.InvoiceID),
		Amount:                 Money(u.Amount, cur),
		TaxesAmount:            Money(u.TaxesAmount, cur),
		TotalAmount:            Money(u.TotalAmount, cur),
	}
	if u.ProjectedAmount != nil {
		b.ProjectedAmount = Money(*u.ProjectedAmount, cur)
	}
	for _, c := range u.Charges {
		b.Charges = append(b.Charges, usageChargeProto(c, cur))
	}
	return b.Build()
}

// usageChargeProto converts the usage of one charge (lago
// V1::Customers::ChargeUsageSerializer).
func usageChargeProto(c subscriptions.ChargeUsage, cur money.Currency) *usagev1.ChargeUsage {
	b := usagev1.ChargeUsage_builder{
		ChargeId:           new(c.Charge.ID.String()),
		BillableMetricId:   new(c.Metric.ID.String()),
		BillableMetricCode: new(c.Metric.Code),
		InvoiceDisplayName: c.Charge.InvoiceDisplayName,
		Units:              new(Decimal(c.Units)),
		Amount:             Money(c.Amount, cur),
		PricingUnitAmount:  DecimalPtr(c.PricingUnitAmount),
		EventsCount:        new(c.EventsCount),
		ProjectedUnits:     DecimalPtr(c.ProjectedUnits),

		PresentationBreakdowns:          PresentationBreakdowns(c.PresentationBreakdowns),
		ProjectedPresentationBreakdowns: PresentationBreakdowns(c.ProjectedPresentationBreakdowns),
	}
	if c.ProjectedAmount != nil {
		b.ProjectedAmount = Money(*c.ProjectedAmount, cur)
	}
	for _, f := range c.Filters {
		fb := usagev1.FilterUsage_builder{
			Units:          new(Decimal(f.Units)),
			Amount:         Money(f.Amount, cur),
			EventsCount:    new(f.EventsCount),
			ProjectedUnits: DecimalPtr(f.ProjectedUnits),

			PresentationBreakdowns:          PresentationBreakdowns(f.PresentationBreakdowns),
			ProjectedPresentationBreakdowns: PresentationBreakdowns(f.ProjectedPresentationBreakdowns),
		}
		if f.ProjectedAmount != nil {
			fb.ProjectedAmount = Money(*f.ProjectedAmount, cur)
		}
		if f.Filter != nil {
			fb.ChargeFilterId = new(f.Filter.ID.String())
			fb.InvoiceDisplayName = f.Filter.InvoiceDisplayName
			fb.Values = make(map[string]string, len(f.Filter.Values))
			for k, vs := range f.Filter.Values {
				fb.Values[k] = strings.Join(vs, ",")
			}
		}
		for _, g := range f.Grouped {
			fb.GroupedUsage = append(fb.GroupedUsage, usageGroupedProto(g, cur))
		}
		b.Filters = append(b.Filters, fb.Build())
	}
	for _, g := range c.Grouped {
		b.GroupedUsage = append(b.GroupedUsage, usageGroupedProto(g, cur))
	}
	return b.Build()
}

// usageGroupedProto converts the usage of one group.
func usageGroupedProto(g subscriptions.GroupedUsage, cur money.Currency) *usagev1.GroupedUsage {
	b := usagev1.GroupedUsage_builder{
		GroupedBy:      maps.Clone(g.GroupedBy),
		Units:          new(Decimal(g.Units)),
		Amount:         Money(g.Amount, cur),
		EventsCount:    new(g.EventsCount),
		ProjectedUnits: DecimalPtr(g.ProjectedUnits),

		PresentationBreakdowns:          PresentationBreakdowns(g.PresentationBreakdowns),
		ProjectedPresentationBreakdowns: PresentationBreakdowns(g.ProjectedPresentationBreakdowns),
	}
	if g.ProjectedAmount != nil {
		b.ProjectedAmount = Money(*g.ProjectedAmount, cur)
	}
	return b.Build()
}
