package store

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/analytics"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindAnalytics binds the analytics reports.
func (t *txState) bindAnalytics(r *port.Repos) {
	r.Analytics = analyticsRepo{t}
}

// analyticsRepo implements analytics.Repository over the reports of
// queries/analytics.sql.
type analyticsRepo struct{ t *txState }

var _ analytics.Repository = analyticsRepo{}

// analyticsMonths returns the first month and the last month of s, UTC:
// the last is Now's month, the first Months-1 months earlier, or nil (every
// month since the organization's creation) when Months is not positive.
func analyticsMonths(s analytics.Scope) (from *time.Time, to time.Time) {
	now := s.Now.UTC()
	to = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	if s.Months > 0 {
		f := to.AddDate(0, -(s.Months - 1), 0)
		from = &f
	}
	return from, to
}

func (r analyticsRepo) GrossRevenue(ctx context.Context, org core.OrganizationID, s analytics.Scope) ([]analytics.Revenue, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	from, to := analyticsMonths(s)
	rows, err := q.AnalyticsGrossRevenue(ctx, db.AnalyticsGrossRevenueParams{
		FromMonth:       from,
		Currency:        currencyCode(s.Currency),
		ToMonth:         to,
		OrganizationID:  org.UUID(),
		CustomerID:      optUUID(s.CustomerID),
		BillingEntityID: optUUID(s.BillingEntityID),
	})
	if err != nil {
		return nil, fmt.Errorf("store: gross revenue: %w", mapErr(err))
	}
	out := make([]analytics.Revenue, 0, len(rows))
	for _, row := range rows {
		c, err := mustCurrency(row.Currency)
		if err != nil {
			return nil, fmt.Errorf("store: gross revenue: %w", err)
		}
		out = append(out, analytics.Revenue{
			Month:           utc(row.Month),
			Currency:        c,
			Amount:          new(row.Amount),
			InvoicesCount:   row.InvoicesCount,
			BillingEntityID: idOf[core.BillingEntityID](row.BillingEntityID),
		})
	}
	return out, nil
}

func (r analyticsRepo) MRR(ctx context.Context, org core.OrganizationID, s analytics.Scope) ([]analytics.Revenue, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	from, to := analyticsMonths(s)
	rows, err := q.AnalyticsMRR(ctx, db.AnalyticsMRRParams{
		FromMonth:       from,
		Currency:        currencyCode(s.Currency),
		ToMonth:         to,
		OrganizationID:  org.UUID(),
		BillingEntityID: optUUID(s.BillingEntityID),
	})
	if err != nil {
		return nil, fmt.Errorf("store: mrr: %w", mapErr(err))
	}
	out := make([]analytics.Revenue, 0, len(rows))
	for _, row := range rows {
		v := analytics.Revenue{Month: utc(row.Month)}
		// An empty currency is a month without revenue.
		if row.Currency != "" {
			if v.Currency, err = mustCurrency(row.Currency); err != nil {
				return nil, fmt.Errorf("store: mrr: %w", err)
			}
			v.Amount = new(row.Amount)
		}
		out = append(out, v)
	}
	return out, nil
}

func (r analyticsRepo) InvoiceCollection(ctx context.Context, org core.OrganizationID, s analytics.Scope) ([]analytics.Collection, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	from, to := analyticsMonths(s)
	rows, err := q.AnalyticsInvoiceCollection(ctx, db.AnalyticsInvoiceCollectionParams{
		FromMonth:        from,
		Currency:         currencyCode(s.Currency),
		ToMonth:          to,
		OrganizationID:   org.UUID(),
		BillingEntityID:  optUUID(s.BillingEntityID),
		CustomerID:       optUUID(s.CustomerID),
		CustomerTinEmpty: s.CustomerTINEmpty,
	})
	if err != nil {
		return nil, fmt.Errorf("store: invoice collection: %w", mapErr(err))
	}
	out := make([]analytics.Collection, 0, len(rows))
	for _, row := range rows {
		c, err := currency(&row.Currency)
		if err != nil {
			return nil, fmt.Errorf("store: invoice collection: %w", err)
		}
		out = append(out, analytics.Collection{
			Month:         utc(row.Month),
			PaymentStatus: core.PaymentStatus(row.PaymentStatus),
			Currency:      c,
			InvoicesCount: row.InvoicesCount,
			Amount:        row.Amount,
		})
	}
	return out, nil
}

func (r analyticsRepo) InvoicedUsage(ctx context.Context, org core.OrganizationID, s analytics.Scope) ([]analytics.Usage, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	from, to := analyticsMonths(s)
	rows, err := q.AnalyticsInvoicedUsage(ctx, db.AnalyticsInvoicedUsageParams{
		FromMonth:       from,
		Currency:        currencyCode(s.Currency),
		ToMonth:         to,
		OrganizationID:  org.UUID(),
		BillingEntityID: optUUID(s.BillingEntityID),
	})
	if err != nil {
		return nil, fmt.Errorf("store: invoiced usage: %w", mapErr(err))
	}
	out := make([]analytics.Usage, 0, len(rows))
	for _, row := range rows {
		c, err := mustCurrency(row.Currency)
		if err != nil {
			return nil, fmt.Errorf("store: invoiced usage: %w", err)
		}
		out = append(out, analytics.Usage{
			Month:      utc(row.Month),
			MetricCode: row.Code,
			Currency:   c,
			Amount:     row.Amount,
		})
	}
	return out, nil
}

func (r analyticsRepo) OverdueBalance(ctx context.Context, org core.OrganizationID, s analytics.Scope) ([]analytics.Overdue, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	from, to := analyticsMonths(s)
	rows, err := q.AnalyticsOverdueBalance(ctx, db.AnalyticsOverdueBalanceParams{
		FromMonth:        from,
		Currency:         currencyCode(s.Currency),
		ToMonth:          to,
		OrganizationID:   org.UUID(),
		CustomerID:       optUUID(s.CustomerID),
		BillingEntityID:  optUUID(s.BillingEntityID),
		CustomerTinEmpty: s.CustomerTINEmpty,
	})
	if err != nil {
		return nil, fmt.Errorf("store: overdue balance: %w", mapErr(err))
	}
	out := make([]analytics.Overdue, 0, len(rows))
	for _, row := range rows {
		c, err := mustCurrency(row.Currency)
		if err != nil {
			return nil, fmt.Errorf("store: overdue balance: %w", err)
		}
		ids := make([]core.InvoiceID, len(row.InvoiceIds))
		for i, u := range row.InvoiceIds {
			ids[i] = idOf[core.InvoiceID](u)
		}
		out = append(out, analytics.Overdue{
			Month:           utc(row.Month),
			Currency:        c,
			Amount:          row.Amount,
			InvoiceIDs:      ids,
			BillingEntityID: idOf[core.BillingEntityID](row.BillingEntityID),
		})
	}
	return out, nil
}
