package rpc

import (
	"context"
	"strings"
	"time"

	"google.golang.org/genproto/googleapis/type/date"

	"github.com/tanvoxyz/tanvo/libs/billing/analytics"
	"github.com/tanvoxyz/tanvo/libs/core"
	coreanalytics "github.com/tanvoxyz/tanvo/libs/core/analytics"
	"github.com/tanvoxyz/tanvo/libs/money"
	analyticsv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/analytics/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/analytics/v1/analyticsv1connect"
)

// Analytics serves tanvo.analytics.v1.AnalyticsService over the analytics
// service (lago Api::V1::Analytics controllers and Analytics::* services).
type Analytics struct {
	analyticsv1connect.UnimplementedAnalyticsServiceHandler

	svc *analytics.Service
}

var _ analyticsv1connect.AnalyticsServiceHandler = (*Analytics)(nil)

// NewAnalytics returns the service over svc.
func NewAnalytics(svc *analytics.Service) *Analytics { return &Analytics{svc: svc} }

// GetGrossRevenue implements AnalyticsService.GetGrossRevenue (lago
// Analytics::GrossRevenuesService).
func (s *Analytics) GetGrossRevenue(ctx context.Context, req *analyticsv1.GetGrossRevenueRequest) (*analyticsv1.GetGrossRevenueResponse, error) {
	rows, err := runReport(ctx, req.GetScope(), s.svc.GrossRevenue, func(f *analytics.Filter) error {
		f.ExpireCache = req.GetExpireCache()
		return analyticsCustomer(f, req.GetCustomerId())
	})
	if err != nil {
		return nil, err
	}
	return analyticsv1.GetGrossRevenueResponse_builder{Points: revenuePoints(rows, true)}.Build(), nil
}

// GetMrr implements AnalyticsService.GetMrr (lago Analytics::MrrsService).
func (s *Analytics) GetMrr(ctx context.Context, req *analyticsv1.GetMrrRequest) (*analyticsv1.GetMrrResponse, error) {
	rows, err := runReport(ctx, req.GetScope(), s.svc.MRR, nil)
	if err != nil {
		return nil, err
	}
	return analyticsv1.GetMrrResponse_builder{Points: revenuePoints(rows, false)}.Build(), nil
}

// GetInvoiceCollection implements AnalyticsService.GetInvoiceCollection
// (lago Analytics::InvoiceCollectionsService).
func (s *Analytics) GetInvoiceCollection(ctx context.Context, req *analyticsv1.GetInvoiceCollectionRequest) (*analyticsv1.GetInvoiceCollectionResponse, error) {
	rows, err := runReport(ctx, req.GetScope(), s.svc.InvoiceCollection, func(f *analytics.Filter) error {
		f.CustomerTINEmpty = Opt(req.HasIsCustomerTinEmpty(), req.GetIsCustomerTinEmpty())
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]*analyticsv1.InvoiceCollectionPoint, len(rows))
	for i, r := range rows {
		out[i] = analyticsv1.InvoiceCollectionPoint_builder{
			Month:         monthDate(r.Month),
			PaymentStatus: new(payStatuses.Proto(r.PaymentStatus)),
			Amount:        Money(r.Amount, r.Currency),
			InvoicesCount: new(r.InvoicesCount),
		}.Build()
	}
	return analyticsv1.GetInvoiceCollectionResponse_builder{Points: out}.Build(), nil
}

// GetInvoicedUsage implements AnalyticsService.GetInvoicedUsage (lago
// Analytics::InvoicedUsagesService).
func (s *Analytics) GetInvoicedUsage(ctx context.Context, req *analyticsv1.GetInvoicedUsageRequest) (*analyticsv1.GetInvoicedUsageResponse, error) {
	rows, err := runReport(ctx, req.GetScope(), s.svc.InvoicedUsage, nil)
	if err != nil {
		return nil, err
	}
	out := make([]*analyticsv1.InvoicedUsagePoint, len(rows))
	for i, r := range rows {
		out[i] = analyticsv1.InvoicedUsagePoint_builder{
			Month:      monthDate(r.Month),
			MetricCode: new(r.MetricCode),
			Amount:     Money(r.Amount, r.Currency),
		}.Build()
	}
	return analyticsv1.GetInvoicedUsageResponse_builder{Points: out}.Build(), nil
}

// GetOverdueBalance implements AnalyticsService.GetOverdueBalance (lago
// Analytics::OverdueBalancesService).
func (s *Analytics) GetOverdueBalance(ctx context.Context, req *analyticsv1.GetOverdueBalanceRequest) (*analyticsv1.GetOverdueBalanceResponse, error) {
	rows, err := runReport(ctx, req.GetScope(), s.svc.OverdueBalance, func(f *analytics.Filter) error {
		f.CustomerTINEmpty = Opt(req.HasIsCustomerTinEmpty(), req.GetIsCustomerTinEmpty())
		f.ExpireCache = req.GetExpireCache()
		return analyticsCustomer(f, req.GetCustomerId())
	})
	if err != nil {
		return nil, err
	}
	out := make([]*analyticsv1.OverdueBalancePoint, len(rows))
	for i, r := range rows {
		ids := make([]string, len(r.InvoiceIDs))
		for j, id := range r.InvoiceIDs {
			ids[j] = id.String()
		}
		out[i] = analyticsv1.OverdueBalancePoint_builder{
			Month:           monthDate(r.Month),
			Amount:          Money(r.Amount, r.Currency),
			InvoiceIds:      ids,
			BillingEntityId: new(r.BillingEntityID.String()),
		}.Build()
	}
	return analyticsv1.GetOverdueBalanceResponse_builder{Points: out}.Build(), nil
}

// runReport parses the scope, completes the filter with the request's own
// fields by extra (nil for none) and runs fn for the caller's
// organization.
func runReport[T any](
	ctx context.Context, sc *analyticsv1.AnalyticsScope,
	fn func(context.Context, core.OrganizationID, analytics.Filter) ([]T, error),
	extra func(*analytics.Filter) error,
) ([]T, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := analyticsFilter(sc)
	if err != nil {
		return nil, err
	}
	if extra != nil {
		if err := extra(&f); err != nil {
			return nil, err
		}
	}
	rows, err := fn(ctx, p.OrganizationID, f)
	if err != nil {
		return nil, prefixedError(ctx, "scope.", "invalid analytics scope", err)
	}
	return rows, nil
}

// analyticsFilter parses an AnalyticsScope; the currency is upper-cased,
// as lago's controllers do.
func analyticsFilter(sc *analyticsv1.AnalyticsScope) (analytics.Filter, error) {
	f := analytics.Filter{Months: int(sc.GetMonths())}
	if c := sc.GetCurrency(); c != "" {
		cur, err := money.LookupCurrency(strings.ToUpper(c))
		if err != nil {
			return f, InvalidArgument("invalid analytics scope", []FieldViolation{{Field: "scope.currency", Reason: core.CodeInvalidCurrency}})
		}
		f.Currency = cur
	}
	be, err := CatalogRef("scope.billing_entity_id", sc.GetBillingEntityId())
	if err != nil {
		return f, err
	}
	f.BillingEntity = be
	return f, nil
}

// analyticsCustomer parses the customer_id of the gross revenue and
// overdue balance requests, the reports lago filters by customer
// (external_customer_id).
func analyticsCustomer(f *analytics.Filter, s string) error {
	if s == "" {
		return nil
	}
	ref, err := ParseRef("customer_id", s)
	if err != nil {
		return err
	}
	switch {
	case ref.Code != "":
		return InvalidArgument("invalid reference", []FieldViolation{{
			Field: "customer_id", Reason: core.CodeInvalid, Description: "this resource has no code",
		}})
	case ref.ExternalID != "":
		f.Customer.ExternalID = ref.ExternalID
	default:
		id, err := ParseID[core.CustomerID]("customer_id", s)
		if err != nil {
			return err
		}
		f.Customer.ID = id
	}
	return nil
}

// revenuePoints converts revenue rows; a row without amount (an MRR month
// without revenue) has no amount. withCount, for the gross revenue, sets
// the invoices count and the billing entity.
func revenuePoints(rows []coreanalytics.Revenue, withCount bool) []*analyticsv1.RevenuePoint {
	out := make([]*analyticsv1.RevenuePoint, len(rows))
	for i, r := range rows {
		b := analyticsv1.RevenuePoint_builder{Month: monthDate(r.Month)}
		if r.Amount != nil {
			b.Amount = Money(*r.Amount, r.Currency)
		}
		if withCount {
			b.InvoicesCount = new(r.InvoicesCount)
			b.BillingEntityId = new(r.BillingEntityID.String())
		}
		out[i] = b.Build()
	}
	return out
}

// monthDate is the first day of the month of t (UTC).
func monthDate(t time.Time) *date.Date {
	return Date(core.DateOf(t, time.UTC))
}
