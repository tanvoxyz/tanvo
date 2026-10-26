// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package server

import (
	"context"
	"fmt"

	"github.com/cockroachdb/apd/v3"

	"github.com/tanvoxyz/tanvo/ee/analytics/period"
	"github.com/tanvoxyz/tanvo/ee/analytics/store"
	dataapiv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/dataapi/v1"
)

// GetMrr implements DataApiService.
func (s *Service) GetMrr(ctx context.Context, req *dataapiv1.GetMrrRequest) (*dataapiv1.GetMrrResponse, error) {
	f, err := s.filter(req.GetFilter(), period.Monthly)
	if err != nil {
		return nil, err
	}
	rows, err := s.reports.MRR(ctx, organization(ctx), f)
	if err != nil {
		return nil, internal(err)
	}
	points := make([]*dataapiv1.MrrPoint, 0, len(rows))
	for _, r := range rows {
		m := &moneys{currency: r.Currency}
		change, err := subtract(r.Ending, r.Starting)
		if err != nil {
			return nil, internal(err)
		}
		points = append(points, dataapiv1.MrrPoint_builder{
			Period:         protoPeriod(f, r.Period),
			StartingMrr:    m.of(r.Starting),
			EndingMrr:      m.of(r.Ending),
			NewMrr:         m.of(r.New),
			ExpansionMrr:   m.of(r.Expansion),
			ContractionMrr: m.of(r.Contraction),
			ChurnedMrr:     m.of(r.Churn),
			MrrChange:      m.of(change),
		}.Build())
		if m.err != nil {
			return nil, internal(m.err)
		}
	}
	return dataapiv1.GetMrrResponse_builder{Points: points}.Build(), nil
}

// ListMrrByPlan implements DataApiService.
func (s *Service) ListMrrByPlan(ctx context.Context, req *dataapiv1.ListMrrByPlanRequest) (*dataapiv1.ListMrrByPlanResponse, error) {
	f, err := s.filter(req.GetFilter(), period.Monthly)
	if err != nil {
		return nil, err
	}
	p, err := page(req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, err
	}
	rows, total, err := s.reports.MRRByPlan(ctx, organization(ctx), f, p)
	if err != nil {
		return nil, internal(err)
	}
	plans := make([]*dataapiv1.MrrByPlan, 0, len(rows))
	for _, r := range rows {
		m := &moneys{currency: r.Currency}
		plans = append(plans, dataapiv1.MrrByPlan_builder{
			Plan:                 planRef(r.Plan),
			Mrr:                  m.of(r.MRR),
			MrrShare:             &r.Share,
			ActiveCustomersCount: &r.Customers,
			ActiveCustomersShare: &r.CustomersShare,
		}.Build())
		if m.err != nil {
			return nil, internal(m.err)
		}
	}
	next := nextToken(p, len(rows), total)
	return dataapiv1.ListMrrByPlanResponse_builder{Plans: plans, NextPageToken: &next, TotalSize: &total}.Build(), nil
}

// GetRevenueStreams implements DataApiService.
func (s *Service) GetRevenueStreams(ctx context.Context, req *dataapiv1.GetRevenueStreamsRequest) (*dataapiv1.GetRevenueStreamsResponse, error) {
	f, err := s.filter(req.GetFilter(), period.Monthly)
	if err != nil {
		return nil, err
	}
	rows, err := s.reports.Revenue(ctx, organization(ctx), f)
	if err != nil {
		return nil, internal(err)
	}
	points := make([]*dataapiv1.RevenueStreamPoint, 0, len(rows))
	for _, r := range rows {
		m := &moneys{currency: r.Currency}
		points = append(points, dataapiv1.RevenueStreamPoint_builder{
			Period:                    protoPeriod(f, r.Period),
			SubscriptionFees:          m.of(r.Subscription),
			UsageBasedFees:            m.of(r.UsageBased),
			CommitmentFees:            m.of(r.Commitment),
			OneOffFees:                m.of(r.OneOff),
			GrossRevenue:              m.of(r.Gross),
			Coupons:                   m.of(r.Coupons),
			CreditNotesCredits:        m.of(r.CreditNotes),
			FreeCredits:               m.of(r.FreeCredits),
			ProgressiveBillingCredits: m.of(r.Progressive),
			ContraRevenue:             m.of(r.Contra),
			NetRevenue:                m.of(r.Net),
			PrepaidCredits:            m.of(r.Prepaid),
		}.Build())
		if m.err != nil {
			return nil, internal(m.err)
		}
	}
	return dataapiv1.GetRevenueStreamsResponse_builder{Points: points}.Build(), nil
}

// ListRevenueStreamsByCustomer implements DataApiService.
func (s *Service) ListRevenueStreamsByCustomer(ctx context.Context, req *dataapiv1.ListRevenueStreamsByCustomerRequest) (*dataapiv1.ListRevenueStreamsByCustomerResponse, error) {
	f, err := s.filter(req.GetFilter(), period.Monthly)
	if err != nil {
		return nil, err
	}
	p, err := page(req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, err
	}
	rows, total, err := s.reports.RevenueByCustomer(ctx, organization(ctx), f, revenueOrder(req.GetOrderBy()), p)
	if err != nil {
		return nil, internal(err)
	}
	out := make([]*dataapiv1.RevenueByCustomer, 0, len(rows))
	for _, r := range rows {
		m := &moneys{currency: r.Currency}
		c := r.Customer
		out = append(out, dataapiv1.RevenueByCustomer_builder{
			Customer: dataapiv1.CustomerRef_builder{
				CustomerId: &c.ID, ExternalCustomerId: &c.ExternalID, CustomerName: &c.Name, CustomerDeleted: &c.Deleted,
			}.Build(),
			GrossRevenue:      m.of(r.Gross),
			GrossRevenueShare: &r.GrossShare,
			NetRevenue:        m.of(r.Net),
			NetRevenueShare:   &r.NetShare,
		}.Build())
		if m.err != nil {
			return nil, internal(m.err)
		}
	}
	next := nextToken(p, len(rows), total)
	return dataapiv1.ListRevenueStreamsByCustomerResponse_builder{Customers: out, NextPageToken: &next, TotalSize: &total}.Build(), nil
}

// ListRevenueStreamsByPlan implements DataApiService.
func (s *Service) ListRevenueStreamsByPlan(ctx context.Context, req *dataapiv1.ListRevenueStreamsByPlanRequest) (*dataapiv1.ListRevenueStreamsByPlanResponse, error) {
	f, err := s.filter(req.GetFilter(), period.Monthly)
	if err != nil {
		return nil, err
	}
	p, err := page(req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, err
	}
	rows, total, err := s.reports.RevenueByPlan(ctx, organization(ctx), f, revenueOrder(req.GetOrderBy()), p)
	if err != nil {
		return nil, internal(err)
	}
	out := make([]*dataapiv1.RevenueByPlan, 0, len(rows))
	for _, r := range rows {
		m := &moneys{currency: r.Currency}
		out = append(out, dataapiv1.RevenueByPlan_builder{
			Plan:              planRef(r.Plan),
			CustomersCount:    &r.Customers,
			CustomersShare:    &r.CustomersShare,
			GrossRevenue:      m.of(r.Gross),
			GrossRevenueShare: &r.GrossShare,
			NetRevenue:        m.of(r.Net),
			NetRevenueShare:   &r.NetShare,
		}.Build())
		if m.err != nil {
			return nil, internal(m.err)
		}
	}
	next := nextToken(p, len(rows), total)
	return dataapiv1.ListRevenueStreamsByPlanResponse_builder{Plans: out, NextPageToken: &next, TotalSize: &total}.Build(), nil
}

// GetPrepaidCredits implements DataApiService.
func (s *Service) GetPrepaidCredits(ctx context.Context, req *dataapiv1.GetPrepaidCreditsRequest) (*dataapiv1.GetPrepaidCreditsResponse, error) {
	f, err := s.filter(req.GetFilter(), period.Monthly)
	if err != nil {
		return nil, err
	}
	rows, err := s.reports.PrepaidCredits(ctx, organization(ctx), f)
	if err != nil {
		return nil, internal(err)
	}
	points := make([]*dataapiv1.PrepaidCreditPoint, 0, len(rows))
	for _, r := range rows {
		m := &moneys{currency: r.Currency}
		points = append(points, dataapiv1.PrepaidCreditPoint_builder{
			Period:           protoPeriod(f, r.Period),
			PurchasedAmount:  m.of(r.PurchasedAmount),
			PurchasedCredits: &r.PurchasedCredits,
			OfferedAmount:    m.of(r.OfferedAmount),
			OfferedCredits:   &r.OfferedCredits,
			ConsumedAmount:   m.of(r.ConsumedAmount),
			ConsumedCredits:  &r.ConsumedCredits,
			VoidedAmount:     m.of(r.VoidedAmount),
			VoidedCredits:    &r.VoidedCredits,
		}.Build())
		if m.err != nil {
			return nil, internal(m.err)
		}
	}
	return dataapiv1.GetPrepaidCreditsResponse_builder{Points: points}.Build(), nil
}

func usageFilter(u *dataapiv1.UsageFilter) store.UsageFilter {
	out := store.UsageFilter{MetricCode: u.GetBillableMetricCode()}
	if u.HasIsBillableMetricRecurring() {
		v := u.GetIsBillableMetricRecurring()
		out.Recurring = &v
	}
	return out
}

// GetUsage implements DataApiService.
func (s *Service) GetUsage(ctx context.Context, req *dataapiv1.GetUsageRequest) (*dataapiv1.GetUsageResponse, error) {
	f, err := s.filter(req.GetFilter(), period.Daily)
	if err != nil {
		return nil, err
	}
	rows, err := s.reports.Usage(ctx, organization(ctx), f, usageFilter(req.GetUsageFilter()), false)
	if err != nil {
		return nil, internal(err)
	}
	points := make([]*dataapiv1.UsagePoint, 0, len(rows))
	for _, r := range rows {
		m := &moneys{currency: r.Currency}
		points = append(points, dataapiv1.UsagePoint_builder{
			Period:                protoPeriod(f, r.Period),
			BillableMetricCode:    &r.MetricCode,
			BillableMetricDeleted: &r.MetricDeleted,
			Units:                 &r.Units,
			Amount:                m.of(r.Amount),
		}.Build())
		if m.err != nil {
			return nil, internal(m.err)
		}
	}
	return dataapiv1.GetUsageResponse_builder{Points: points}.Build(), nil
}

// GetUsageAmounts implements DataApiService.
func (s *Service) GetUsageAmounts(ctx context.Context, req *dataapiv1.GetUsageAmountsRequest) (*dataapiv1.GetUsageAmountsResponse, error) {
	f, err := s.filter(req.GetFilter(), period.Daily)
	if err != nil {
		return nil, err
	}
	rows, err := s.reports.Usage(ctx, organization(ctx), f, usageFilter(req.GetUsageFilter()), true)
	if err != nil {
		return nil, internal(err)
	}
	points := make([]*dataapiv1.UsageAmountPoint, 0, len(rows))
	for _, r := range rows {
		m := &moneys{currency: r.Currency}
		points = append(points, dataapiv1.UsageAmountPoint_builder{Period: protoPeriod(f, r.Period), Amount: m.of(r.Amount)}.Build())
		if m.err != nil {
			return nil, internal(m.err)
		}
	}
	return dataapiv1.GetUsageAmountsResponse_builder{Points: points}.Build(), nil
}

// GetInvoicedUsage implements DataApiService.
func (s *Service) GetInvoicedUsage(ctx context.Context, req *dataapiv1.GetInvoicedUsageRequest) (*dataapiv1.GetInvoicedUsageResponse, error) {
	f, err := s.filter(req.GetFilter(), period.Daily)
	if err != nil {
		return nil, err
	}
	rows, err := s.reports.InvoicedUsage(ctx, organization(ctx), f, store.InvoicedUsageFilter{
		MetricCode: req.GetBillableMetricCode(), GroupedBy: req.GetGroupedBy(), FilterValues: req.GetFilterValues(),
	})
	if err != nil {
		return nil, internal(err)
	}
	points := make([]*dataapiv1.InvoicedUsagePoint, 0, len(rows))
	for _, r := range rows {
		m := &moneys{currency: r.Currency}
		points = append(points, dataapiv1.InvoicedUsagePoint_builder{
			Period: protoPeriod(f, r.Period), BillableMetricCode: &r.MetricCode, Amount: m.of(r.Amount),
		}.Build())
		if m.err != nil {
			return nil, internal(m.err)
		}
	}
	return dataapiv1.GetInvoicedUsageResponse_builder{Points: points}.Build(), nil
}

// GetRevenueRecognition implements DataApiService.
func (s *Service) GetRevenueRecognition(ctx context.Context, req *dataapiv1.GetRevenueRecognitionRequest) (*dataapiv1.GetRevenueRecognitionResponse, error) {
	f, err := s.filter(req.GetFilter(), period.Monthly)
	if err != nil {
		return nil, err
	}
	rows, err := s.reports.Recognition(ctx, organization(ctx), f)
	if err != nil {
		return nil, internal(err)
	}
	points := make([]*dataapiv1.RevenueRecognitionPoint, 0, len(rows))
	for _, r := range rows {
		m := &moneys{currency: r.Currency}
		points = append(points, dataapiv1.RevenueRecognitionPoint_builder{
			Period: protoPeriod(f, r.Period), Invoiced: m.of(r.Invoiced), Recognized: m.of(r.Recognized), Deferred: m.of(r.Deferred),
		}.Build())
		if m.err != nil {
			return nil, internal(m.err)
		}
	}
	return dataapiv1.GetRevenueRecognitionResponse_builder{Points: points}.Build(), nil
}

// subtract returns a − b of two decimal strings from PostgreSQL.
func subtract(a, b string) (string, error) {
	var x, y, z apd.Decimal
	if _, _, err := x.SetString(a); err != nil {
		return "", fmt.Errorf("amount %q: %w", a, err)
	}
	if _, _, err := y.SetString(b); err != nil {
		return "", fmt.Errorf("amount %q: %w", b, err)
	}
	if _, err := apd.BaseContext.WithPrecision(34).Sub(&z, &x, &y); err != nil {
		return "", fmt.Errorf("subtract: %w", err)
	}
	return z.Text('f'), nil
}
