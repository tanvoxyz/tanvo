package rpc

import (
	"context"
	"maps"
	"strings"
	"time"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/libs/billing/subscriptions"
	"github.com/tanvoxyz/tanvo/libs/money"
	usagev1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/usage/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/usage/v1/usagev1connect"
)

// Usage serves tanvo.usage.v1.UsageService over the billing library's
// subscription service (lago Api::V1::Customers::UsageController and
// Api::V1::Customers::ProjectedUsageController). GetHourlyUsage and
// ListDailyUsage have no implementation: the hourly usage is a GraphQL
// query of another shape, and the daily usage has no API.
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
// units and amounts. filter_by and group_by_attributions have no
// implementation.
func (s *Usage) GetCurrentUsage(ctx context.Context, req *usagev1.GetCurrentUsageRequest) (*usagev1.GetCurrentUsageResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if len(req.GetFilterBy()) > 0 || len(req.GetGroupByAttributions()) > 0 {
		return nil, connect.NewError(connect.CodeUnimplemented, "filter_by and group_by_attributions are not supported")
	}
	in := subscriptions.CurrentUsageInput{
		OrganizationID:     p.OrganizationID,
		SkipTaxes:          req.HasApplyTaxes() && !req.GetApplyTaxes(),
		Projection:         req.GetIncludeProjection(),
		BillableMetricCode: req.GetBillableMetricCode(),
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
	}
	if g.ProjectedAmount != nil {
		b.ProjectedAmount = Money(*g.ProjectedAmount, cur)
	}
	return b.Build()
}
