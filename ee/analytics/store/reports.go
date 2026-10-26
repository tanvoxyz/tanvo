// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package store

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Amounts are decimal strings in minor units, as PostgreSQL prints
// numeric values; shares are decimal strings from 0 to 1.

// Point is the key of a report row: a period, by its index in
// Filter.Periods, and a currency.
type Point struct {
	// Period indexes Filter.Periods.
	Period int
	// Currency is the ISO 4217 code.
	Currency string
}

// MRRPoint is the MRR of a period in a currency.
type MRRPoint struct {
	Point
	// Starting is the MRR on the day before the period.
	Starting string
	// Ending is the MRR on the period's last day.
	Ending string
	// New, Expansion, Contraction and Churn are the movements.
	New, Expansion, Contraction, Churn string
}

// MRR returns the MRR of each period and currency, every period of a
// currency that has any (or of the filter's currency), by currency and
// period. Movements compare each customer's MRR on the day before the
// period with its MRR on the last day.
func (s *Store) MRR(ctx context.Context, org string, f Filter) ([]MRRPoint, error) {
	q := newQuery(org)
	sql := `WITH c AS (
  SELECT b.ord, i.customer_id, i.currency,
    COALESCE(sum(i.amount) FILTER (WHERE i.start_date < b.start_date AND (i.end_date IS NULL OR i.end_date >= b.start_date)), 0) AS s,
    COALESCE(sum(i.amount) FILTER (WHERE i.start_date <= b.end_date AND (i.end_date IS NULL OR i.end_date > b.end_date)), 0) AS e
  FROM ` + q.periods(f.Periods) + `
  JOIN analytics.mrr_intervals i ON i.organization_id = $1 AND i.start_date <= b.end_date AND (i.end_date IS NULL OR i.end_date >= b.start_date)
  WHERE ` + and(mrrFilter(q, f, "i")...) + `
  GROUP BY b.ord, i.customer_id, i.currency
)
SELECT ord, currency, sum(s)::text, sum(e)::text,
  COALESCE(sum(e) FILTER (WHERE s = 0 AND e > 0), 0)::text,
  COALESCE(sum(e - s) FILTER (WHERE s > 0 AND e > s), 0)::text,
  COALESCE(sum(s - e) FILTER (WHERE e > 0 AND e < s), 0)::text,
  COALESCE(sum(s) FILTER (WHERE s > 0 AND e = 0), 0)::text
FROM c GROUP BY ord, currency`
	var rows []MRRPoint
	err := s.read(ctx, org, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, sql, q.args...)
		if err != nil {
			return err
		}
		rows, err = pgx.CollectRows(r, func(r pgx.CollectableRow) (MRRPoint, error) {
			var p MRRPoint
			err := r.Scan(&p.Period, &p.Currency, &p.Starting, &p.Ending, &p.New, &p.Expansion, &p.Contraction, &p.Churn)
			p.Period--
			return p, err
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("store: mrr: %w", err)
	}
	return fill(rows, f, func(p Point) MRRPoint {
		return MRRPoint{Point: p, Starting: "0", Ending: "0", New: "0", Expansion: "0", Contraction: "0", Churn: "0"}
	}, func(r MRRPoint) Point { return r.Point }), nil
}

// mrrFilter returns the conditions of f on the MRR intervals of alias a.
func mrrFilter(q *query, f Filter, a string) []string {
	return []string{
		q.currency(f, a+".currency"),
		q.customers(f, a+".customer_id"),
		q.subscription(f, a+".subscription_id"),
		q.plan(f, a+".plan_id"),
		q.billingEntity(f, a+".billing_entity_id"),
	}
}

// fill returns rows with a row made by empty for each period of each
// currency that has none, by currency and period.
func fill[R any](rows []R, f Filter, empty func(Point) R, key func(R) Point) []R {
	byKey := map[Point]R{}
	currencies := map[string]bool{}
	if f.Currency != "" {
		currencies[f.Currency] = true
	}
	for _, r := range rows {
		byKey[key(r)] = r
		currencies[key(r).Currency] = true
	}
	var out []R
	for _, c := range slices.Sorted(maps.Keys(currencies)) {
		for i := range f.Periods {
			p := Point{Period: i, Currency: c}
			if r, ok := byKey[p]; ok {
				out = append(out, r)
			} else {
				out = append(out, empty(p))
			}
		}
	}
	return out
}

// Plan is a plan of a report.
type Plan struct {
	// ID is the plan's ID.
	ID string
	// Code, Name and Interval describe it; empty for a plan deleted
	// before the analytics saw it.
	Code, Name, Interval string
	// Deleted reports a deleted plan.
	Deleted bool
}

// PlanMRR is the MRR of a plan in a currency.
type PlanMRR struct {
	Plan
	// Currency is the ISO 4217 code.
	Currency string
	// MRR is the plan's MRR; Share its part of the currency's.
	MRR, Share string
	// Customers counts the customers with an MRR on the plan;
	// CustomersShare is their part of the currency's.
	Customers      int64
	CustomersShare string
}

// Page is a page of a list.
type Page struct {
	// Offset is the index of the first row.
	Offset int
	// Size is the number of rows.
	Size int
}

// MRRByPlan returns the MRR by plan and currency on the filter's last
// day, largest first, and the number of rows of every page.
func (s *Store) MRRByPlan(ctx context.Context, org string, f Filter, page Page) ([]PlanMRR, int64, error) {
	q := newQuery(org)
	day := q.arg(f.To())
	sql := `WITH m AS (
  SELECT i.plan_id, i.customer_id, i.currency, sum(i.amount) AS amount
  FROM analytics.mrr_intervals i
  WHERE i.organization_id = $1 AND i.start_date <= ` + day + `::date AND (i.end_date IS NULL OR i.end_date > ` + day + `::date)
    AND ` + and(mrrFilter(q, f, "i")...) + `
  GROUP BY i.plan_id, i.customer_id, i.currency
),
p AS (
  SELECT plan_id, currency, sum(amount) AS mrr, count(*) FILTER (WHERE amount > 0) AS customers FROM m GROUP BY plan_id, currency
),
t AS (
  SELECT currency, sum(amount) AS mrr, count(DISTINCT customer_id) FILTER (WHERE amount > 0) AS customers FROM m GROUP BY currency
)
SELECT p.plan_id::text, COALESCE(pl.code, ''), COALESCE(pl.name, ''), COALESCE(pl.billing_interval, ''), COALESCE(pl.deleted, true),
  p.currency, p.mrr::text, COALESCE(round(p.mrr / NULLIF(t.mrr, 0), 6), 0)::text,
  p.customers, COALESCE(round(p.customers::numeric / NULLIF(t.customers, 0), 6), 0)::text, count(*) OVER ()
FROM p JOIN t ON t.currency = p.currency
LEFT JOIN analytics.plans pl ON pl.organization_id = $1 AND pl.id = p.plan_id
ORDER BY p.mrr DESC, p.currency, p.plan_id
LIMIT ` + q.arg(page.Size) + ` OFFSET ` + q.arg(page.Offset)
	var total int64
	var out []PlanMRR
	err := s.read(ctx, org, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, sql, q.args...)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(r, func(r pgx.CollectableRow) (PlanMRR, error) {
			var p PlanMRR
			err := r.Scan(&p.ID, &p.Code, &p.Name, &p.Interval, &p.Deleted, &p.Currency, &p.MRR, &p.Share, &p.Customers, &p.CustomersShare, &total)
			return p, err
		})
		return err
	})
	if err != nil {
		return nil, 0, fmt.Errorf("store: mrr by plan: %w", err)
	}
	if len(out) == 0 && page.Offset > 0 {
		_, total, err = s.MRRByPlan(ctx, org, f, Page{Size: 1})
	}
	return out, total, err
}

// RevenuePoint is the revenue of a period in a currency.
type RevenuePoint struct {
	Point
	// Subscription, UsageBased, Commitment and OneOff are the fees by
	// kind; Gross their sum.
	Subscription, UsageBased, Commitment, OneOff, Gross string
	// Coupons, CreditNotes, FreeCredits and Progressive reduce the gross
	// revenue; Contra is their sum, Net the gross less it.
	Coupons, CreditNotes, FreeCredits, Progressive, Contra, Net string
	// Prepaid is the purchased prepaid credits applied.
	Prepaid string
}

// revenueFilter returns the conditions of f on the CTE rev.
func revenueFilter(q *query, f Filter) []string {
	return []string{
		q.currency(f, "rev.currency"),
		q.customers(f, "rev.customer_id"),
		q.subscription(f, "rev.subscription_id"),
		q.plan(f, "rev.plan_id"),
		q.billingEntity(f, "rev.billing_entity_id"),
	}
}

// Revenue returns the revenue of each period and currency, by issuing
// day.
func (s *Store) Revenue(ctx context.Context, org string, f Filter) ([]RevenuePoint, error) {
	q := newQuery(org)
	sql := "WITH " + revenueCTE(q, &[2]time.Time{f.From(), f.To()}) + `
SELECT b.ord, rev.currency,
  COALESCE(sum(rev.gross) FILTER (WHERE rev.fee_type IN ('subscription', 'fixed_charge', 'product')), 0)::text,
  COALESCE(sum(rev.gross) FILTER (WHERE rev.fee_type = 'charge'), 0)::text,
  COALESCE(sum(rev.gross) FILTER (WHERE rev.fee_type = 'commitment'), 0)::text,
  COALESCE(sum(rev.gross) FILTER (WHERE rev.fee_type = 'add_on'), 0)::text,
  sum(rev.gross)::text, sum(rev.coupons)::text, sum(rev.credit_notes)::text, sum(rev.free_credits)::text,
  sum(rev.progressive)::text, sum(rev.coupons + rev.credit_notes + rev.free_credits + rev.progressive)::text,
  sum(rev.net)::text, sum(rev.prepaid)::text
FROM rev JOIN ` + q.periods(f.Periods) + ` ON rev.issuing_date BETWEEN b.start_date AND b.end_date
WHERE ` + and(revenueFilter(q, f)...) + `
GROUP BY b.ord, rev.currency`
	var rows []RevenuePoint
	err := s.read(ctx, org, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, sql, q.args...)
		if err != nil {
			return err
		}
		rows, err = pgx.CollectRows(r, func(r pgx.CollectableRow) (RevenuePoint, error) {
			var p RevenuePoint
			err := r.Scan(&p.Period, &p.Currency, &p.Subscription, &p.UsageBased, &p.Commitment, &p.OneOff, &p.Gross,
				&p.Coupons, &p.CreditNotes, &p.FreeCredits, &p.Progressive, &p.Contra, &p.Net, &p.Prepaid)
			p.Period--
			return p, err
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("store: revenue: %w", err)
	}
	return fill(rows, f, func(p Point) RevenuePoint {
		return RevenuePoint{
			Point: p, Subscription: "0", UsageBased: "0", Commitment: "0", OneOff: "0", Gross: "0",
			Coupons: "0", CreditNotes: "0", FreeCredits: "0", Progressive: "0", Contra: "0", Net: "0", Prepaid: "0",
		}
	}, func(r RevenuePoint) Point { return r.Point }), nil
}

// RevenueOrder orders revenue lists.
type RevenueOrder int

const (
	// ByGross orders by gross revenue, largest first.
	ByGross RevenueOrder = iota
	// ByNet orders by net revenue, largest first.
	ByNet
)

// Customer is a customer of a report.
type Customer struct {
	// ID, ExternalID and Name describe the customer.
	ID, ExternalID, Name string
	// Deleted reports a deleted customer.
	Deleted bool
}

// GroupRevenue is the revenue of a customer or a plan in a currency.
type GroupRevenue struct {
	// Customer is set in customer lists.
	Customer Customer
	// Plan is set in plan lists.
	Plan Plan
	// Customers counts the plan's invoiced customers; CustomersShare is
	// their part of the currency's.
	Customers      int64
	CustomersShare string
	// Currency is the ISO 4217 code.
	Currency string
	// Gross and Net are the revenues, with their parts of the currency's.
	Gross, GrossShare, Net, NetShare string
}

// RevenueByCustomer returns the revenue of the range by customer and
// currency.
func (s *Store) RevenueByCustomer(ctx context.Context, org string, f Filter, order RevenueOrder, page Page) ([]GroupRevenue, int64, error) {
	q := newQuery(org)
	sql := "WITH " + revenueCTE(q, &[2]time.Time{f.From(), f.To()}) + `,
g AS (
  SELECT rev.customer_id AS id, rev.currency, sum(rev.gross) AS gross, sum(rev.net) AS net
  FROM rev WHERE ` + and(revenueFilter(q, f)...) + `
  GROUP BY rev.customer_id, rev.currency
),
t AS (SELECT currency, sum(gross) AS gross, sum(net) AS net FROM g GROUP BY currency)
SELECT g.id::text, COALESCE(c.external_id, ''), COALESCE(c.name, ''), c.deleted_at IS NOT NULL, g.currency,
  g.gross::text, COALESCE(round(g.gross / NULLIF(t.gross, 0), 6), 0)::text,
  g.net::text, COALESCE(round(g.net / NULLIF(t.net, 0), 6), 0)::text, count(*) OVER ()
FROM g JOIN t ON t.currency = g.currency
LEFT JOIN exports_customers c ON c.organization_id = $1 AND c.id = g.id
ORDER BY ` + orderColumn(order) + ` DESC, g.currency, g.id
LIMIT ` + q.arg(page.Size) + ` OFFSET ` + q.arg(page.Offset)
	var total int64
	var out []GroupRevenue
	err := s.read(ctx, org, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, sql, q.args...)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(r, func(r pgx.CollectableRow) (GroupRevenue, error) {
			var g GroupRevenue
			err := r.Scan(&g.Customer.ID, &g.Customer.ExternalID, &g.Customer.Name, &g.Customer.Deleted, &g.Currency,
				&g.Gross, &g.GrossShare, &g.Net, &g.NetShare, &total)
			return g, err
		})
		return err
	})
	if err != nil {
		return nil, 0, fmt.Errorf("store: revenue by customer: %w", err)
	}
	if len(out) == 0 && page.Offset > 0 {
		_, total, err = s.RevenueByCustomer(ctx, org, f, order, Page{Size: 1})
	}
	return out, total, err
}

// RevenueByPlan returns the revenue of the range by plan and currency,
// from the fees of subscriptions.
func (s *Store) RevenueByPlan(ctx context.Context, org string, f Filter, order RevenueOrder, page Page) ([]GroupRevenue, int64, error) {
	q := newQuery(org)
	sql := "WITH " + revenueCTE(q, &[2]time.Time{f.From(), f.To()}) + `,
g AS (
  SELECT rev.plan_id AS id, rev.currency, sum(rev.gross) AS gross, sum(rev.net) AS net, count(DISTINCT rev.customer_id) AS customers
  FROM rev WHERE rev.plan_id IS NOT NULL AND ` + and(revenueFilter(q, f)...) + `
  GROUP BY rev.plan_id, rev.currency
),
t AS (
  SELECT currency, sum(gross) AS gross, sum(net) AS net FROM g GROUP BY currency
),
tc AS (
  SELECT rev.currency, count(DISTINCT rev.customer_id) AS customers
  FROM rev WHERE rev.plan_id IS NOT NULL AND ` + and(revenueFilter(q, f)...) + `
  GROUP BY rev.currency
)
SELECT g.id::text, COALESCE(pl.code, ''), COALESCE(pl.name, ''), COALESCE(pl.billing_interval, ''), COALESCE(pl.deleted, true),
  g.customers, COALESCE(round(g.customers::numeric / NULLIF(tc.customers, 0), 6), 0)::text, g.currency,
  g.gross::text, COALESCE(round(g.gross / NULLIF(t.gross, 0), 6), 0)::text,
  g.net::text, COALESCE(round(g.net / NULLIF(t.net, 0), 6), 0)::text, count(*) OVER ()
FROM g JOIN t ON t.currency = g.currency JOIN tc ON tc.currency = g.currency
LEFT JOIN analytics.plans pl ON pl.organization_id = $1 AND pl.id = g.id
ORDER BY ` + orderColumn(order) + ` DESC, g.currency, g.id
LIMIT ` + q.arg(page.Size) + ` OFFSET ` + q.arg(page.Offset)
	var total int64
	var out []GroupRevenue
	err := s.read(ctx, org, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, sql, q.args...)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(r, func(r pgx.CollectableRow) (GroupRevenue, error) {
			var g GroupRevenue
			err := r.Scan(&g.Plan.ID, &g.Plan.Code, &g.Plan.Name, &g.Plan.Interval, &g.Plan.Deleted,
				&g.Customers, &g.CustomersShare, &g.Currency, &g.Gross, &g.GrossShare, &g.Net, &g.NetShare, &total)
			return g, err
		})
		return err
	})
	if err != nil {
		return nil, 0, fmt.Errorf("store: revenue by plan: %w", err)
	}
	if len(out) == 0 && page.Offset > 0 {
		_, total, err = s.RevenueByPlan(ctx, org, f, order, Page{Size: 1})
	}
	return out, total, err
}

func orderColumn(o RevenueOrder) string {
	if o == ByNet {
		return "g.net"
	}
	return "g.gross"
}

// PrepaidPoint is the prepaid credits of a period in a currency.
type PrepaidPoint struct {
	Point
	// The amounts and the credits purchased, offered, consumed and voided.
	PurchasedAmount, PurchasedCredits, OfferedAmount, OfferedCredits string
	ConsumedAmount, ConsumedCredits, VoidedAmount, VoidedCredits     string
}

// PrepaidCredits returns the prepaid credits of each period and currency,
// from the wallet transactions settled in it.
func (s *Store) PrepaidCredits(ctx context.Context, org string, f Filter) ([]PrepaidPoint, error) {
	q := newQuery(org)
	sum := func(typ, status, col string) string {
		return "COALESCE(sum(wt." + col + ") FILTER (WHERE wt.transaction_type = '" + typ + "' AND wt.transaction_status = '" + status + "'), 0)::text"
	}
	sql := `SELECT b.ord, wt.currency, ` + sum("inbound", "purchased", "amount") + `, ` + sum("inbound", "purchased", "credit_amount") + `,
  ` + sum("inbound", "granted", "amount") + `, ` + sum("inbound", "granted", "credit_amount") + `,
  ` + sum("outbound", "invoiced", "amount") + `, ` + sum("outbound", "invoiced", "credit_amount") + `,
  ` + sum("outbound", "voided", "amount") + `, ` + sum("outbound", "voided", "credit_amount") + `
FROM exports_wallet_transactions wt
JOIN exports_wallets w ON w.organization_id = wt.organization_id AND w.id = wt.wallet_id
JOIN exports_customers c ON c.organization_id = w.organization_id AND c.id = w.customer_id
JOIN ` + q.periods(f.Periods) + ` ON (wt.settled_at AT TIME ZONE c.applicable_timezone)::date BETWEEN b.start_date AND b.end_date
WHERE wt.organization_id = $1 AND wt.status = 'settled' AND ` + and(
		q.currency(f, "wt.currency"),
		q.customers(f, "w.customer_id"),
		q.billingEntity(f, "COALESCE(wt.billing_entity_id, w.billing_entity_id, c.billing_entity_id)"),
	) + `
GROUP BY b.ord, wt.currency`
	var rows []PrepaidPoint
	err := s.read(ctx, org, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, sql, q.args...)
		if err != nil {
			return err
		}
		rows, err = pgx.CollectRows(r, func(r pgx.CollectableRow) (PrepaidPoint, error) {
			var p PrepaidPoint
			err := r.Scan(&p.Period, &p.Currency, &p.PurchasedAmount, &p.PurchasedCredits, &p.OfferedAmount, &p.OfferedCredits,
				&p.ConsumedAmount, &p.ConsumedCredits, &p.VoidedAmount, &p.VoidedCredits)
			p.Period--
			return p, err
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("store: prepaid credits: %w", err)
	}
	return fill(rows, f, func(p Point) PrepaidPoint {
		return PrepaidPoint{
			Point: p, PurchasedAmount: "0", PurchasedCredits: "0", OfferedAmount: "0", OfferedCredits: "0",
			ConsumedAmount: "0", ConsumedCredits: "0", VoidedAmount: "0", VoidedCredits: "0",
		}
	}, func(r PrepaidPoint) Point { return r.Point }), nil
}

// UsageFilter restricts the usage reports.
type UsageFilter struct {
	// MetricCode is one billable metric's code.
	MetricCode string
	// Recurring keeps the recurring (true) or the other (false) metrics.
	Recurring *bool
}

// UsagePoint is the usage of a period, currency and billable metric.
type UsagePoint struct {
	Point
	// MetricCode is the billable metric's code; empty in totals.
	MetricCode string
	// MetricDeleted reports a deleted billable metric.
	MetricDeleted bool
	// Units and Amount are the usage.
	Units, Amount string
}

// Usage returns the usage of each period and currency, by billable
// metric unless total, from the daily usage increments.
func (s *Store) Usage(ctx context.Context, org string, f Filter, u UsageFilter, total bool) ([]UsagePoint, error) {
	q := newQuery(org)
	code := "cu->'billable_metric'->>'code'"
	conds := []string{
		q.currency(f, "COALESCE(cu->>'amount_currency', du.usage_diff->>'currency')"),
		q.customers(f, "du.customer_id"),
		q.subscription(f, "du.subscription_id"),
		q.subscriptionPlan(f, "du.subscription_id"),
		q.subscriptionBillingEntity(f, "du.subscription_id"),
	}
	if u.MetricCode != "" && !total {
		conds = append(conds, code+" = "+q.arg(u.MetricCode))
	}
	if u.Recurring != nil {
		conds = append(conds, code+" IN (SELECT bm.code FROM exports_billable_metrics bm WHERE bm.organization_id = $1 AND bm.recurring = "+q.arg(*u.Recurring)+")")
	}
	group, cols := "b.ord, currency, "+code, code+`, bool_and(NOT EXISTS (SELECT 1 FROM exports_billable_metrics bm WHERE bm.organization_id = $1 AND bm.code = `+code+`)),
  COALESCE(sum((cu->>'units')::numeric), 0)::text`
	if total {
		group, cols = "b.ord, currency", "'', false, '0'"
	}
	sql := `SELECT b.ord, COALESCE(cu->>'amount_currency', du.usage_diff->>'currency') AS currency, ` + cols + `,
  COALESCE(sum((cu->>'amount_cents')::numeric), 0)::text
FROM exports_daily_usages du
CROSS JOIN LATERAL jsonb_array_elements(CASE jsonb_typeof(du.usage_diff->'charges_usage') WHEN 'array' THEN du.usage_diff->'charges_usage' ELSE '[]'::jsonb END) AS cu
JOIN ` + q.periods(f.Periods) + ` ON du.usage_date BETWEEN b.start_date AND b.end_date
WHERE du.organization_id = $1 AND ` + and(conds...) + `
GROUP BY ` + group
	var rows []UsagePoint
	err := s.read(ctx, org, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, sql, q.args...)
		if err != nil {
			return err
		}
		rows, err = pgx.CollectRows(r, func(r pgx.CollectableRow) (UsagePoint, error) {
			var p UsagePoint
			var currency *string
			err := r.Scan(&p.Period, &currency, &p.MetricCode, &p.MetricDeleted, &p.Units, &p.Amount)
			if currency != nil {
				p.Currency = *currency
			}
			p.Period--
			return p, err
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("store: usage: %w", err)
	}
	if total {
		return fill(rows, f, func(p Point) UsagePoint { return UsagePoint{Point: p, Units: "0", Amount: "0"} },
			func(r UsagePoint) Point { return r.Point }), nil
	}
	sortPoints(rows, func(r UsagePoint) string { return r.MetricCode })
	return rows, nil
}

// sortPoints sorts rows by currency, then by the key, then by period.
func sortPoints[R interface{ point() Point }](rows []R, key func(R) string) {
	slices.SortFunc(rows, func(a, b R) int {
		pa, pb := a.point(), b.point()
		if pa.Currency != pb.Currency {
			return strings.Compare(pa.Currency, pb.Currency)
		}
		if ka, kb := key(a), key(b); ka != kb {
			return strings.Compare(ka, kb)
		}
		return pa.Period - pb.Period
	})
}

func (p UsagePoint) point() Point         { return p.Point }
func (p InvoicedUsagePoint) point() Point { return p.Point }

// InvoicedUsagePoint is the usage invoiced in a period and currency for a
// billable metric.
type InvoicedUsagePoint struct {
	Point
	// MetricCode is the billable metric's code.
	MetricCode string
	// Amount is the amount of the charge fees.
	Amount string
}

// InvoicedUsageFilter restricts the invoiced usage.
type InvoicedUsageFilter struct {
	// MetricCode is one billable metric's code.
	MetricCode string
	// GroupedBy keeps the fees of pricing groups with these values.
	GroupedBy map[string]string
	// FilterValues keeps the fees of charge filters allowing these values.
	FilterValues map[string]string
}

// InvoicedUsage returns the charge fees of each period and currency by
// billable metric, by issuing day.
func (s *Store) InvoicedUsage(ctx context.Context, org string, f Filter, u InvoicedUsageFilter) ([]InvoicedUsagePoint, error) {
	q := newQuery(org)
	conds := append(revenueFilter(q, f), "rev.fee_type = 'charge'")
	if u.MetricCode != "" {
		conds = append(conds, "fe.item->>'code' = "+q.arg(u.MetricCode))
	}
	if len(u.GroupedBy) > 0 {
		b, err := json.Marshal(u.GroupedBy)
		if err != nil {
			return nil, fmt.Errorf("store: invoiced usage: %w", err)
		}
		conds = append(conds, "(fe.item->'grouped_by')::jsonb @> "+q.arg(string(b))+"::jsonb")
	}
	if len(u.FilterValues) > 0 {
		values := map[string][]string{}
		for k, v := range u.FilterValues {
			values[k] = []string{v}
		}
		b, err := json.Marshal(values)
		if err != nil {
			return nil, fmt.Errorf("store: invoiced usage: %w", err)
		}
		conds = append(conds, "EXISTS (SELECT 1 FROM json_array_elements(CASE json_typeof(fe.item->'filters') WHEN 'array' THEN fe.item->'filters' ELSE '[]'::json END) AS cf WHERE cf->>'id' = fe.charge_filter_id::text AND (cf->'values')::jsonb @> "+q.arg(string(b))+"::jsonb)")
	}
	sql := "WITH " + revenueCTE(q, &[2]time.Time{f.From(), f.To()}) + `
SELECT b.ord, rev.currency, fe.item->>'code', sum(rev.gross)::text
FROM rev JOIN exports_fees fe ON fe.organization_id = $1 AND fe.id = rev.fee_id
JOIN ` + q.periods(f.Periods) + ` ON rev.issuing_date BETWEEN b.start_date AND b.end_date
WHERE ` + and(conds...) + `
GROUP BY b.ord, rev.currency, fe.item->>'code'`
	var rows []InvoicedUsagePoint
	err := s.read(ctx, org, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, sql, q.args...)
		if err != nil {
			return err
		}
		rows, err = pgx.CollectRows(r, func(r pgx.CollectableRow) (InvoicedUsagePoint, error) {
			var p InvoicedUsagePoint
			var code *string
			err := r.Scan(&p.Period, &p.Currency, &code, &p.Amount)
			if code != nil {
				p.MetricCode = *code
			}
			p.Period--
			return p, err
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("store: invoiced usage: %w", err)
	}
	sortPoints(rows, func(r InvoicedUsagePoint) string { return r.MetricCode })
	return rows, nil
}

// RecognitionPoint is the revenue recognition of a period in a currency.
type RecognitionPoint struct {
	Point
	// Invoiced is the net revenue issued in the period; Recognized the
	// net revenue recognized in it; Deferred the revenue issued but not
	// recognized at its end.
	Invoiced, Recognized, Deferred string
}

// Recognition returns the revenue recognition of each period and
// currency.
func (s *Store) Recognition(ctx context.Context, org string, f Filter) ([]RecognitionPoint, error) {
	q := newQuery(org)
	sql := `SELECT b.ord, r.currency,
  COALESCE(sum(r.amount) FILTER (WHERE r.issuing_date BETWEEN b.start_date AND b.end_date), 0)::text,
  COALESCE(sum(r.amount * GREATEST(0, LEAST(r.end_date, b.end_date + 1) - GREATEST(r.start_date, b.start_date))::numeric
    / (r.end_date - r.start_date)), 0)::text,
  (COALESCE(sum(r.amount) FILTER (WHERE r.issuing_date <= b.end_date), 0)
    - COALESCE(sum(r.amount * LEAST(1, GREATEST(0, b.end_date + 1 - r.start_date)::numeric / (r.end_date - r.start_date))), 0))::text
FROM ` + q.periods(f.Periods) + `
JOIN analytics.recognition_schedules r ON r.organization_id = $1 AND (r.issuing_date <= b.end_date OR r.start_date <= b.end_date)
WHERE ` + and(
		q.currency(f, "r.currency"),
		q.customers(f, "r.customer_id"),
		q.subscription(f, "r.subscription_id"),
		q.plan(f, "r.plan_id"),
		q.billingEntity(f, "r.billing_entity_id"),
	) + `
GROUP BY b.ord, r.currency`
	var rows []RecognitionPoint
	err := s.read(ctx, org, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, sql, q.args...)
		if err != nil {
			return err
		}
		rows, err = pgx.CollectRows(r, func(r pgx.CollectableRow) (RecognitionPoint, error) {
			var p RecognitionPoint
			err := r.Scan(&p.Period, &p.Currency, &p.Invoiced, &p.Recognized, &p.Deferred)
			p.Period--
			return p, err
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("store: revenue recognition: %w", err)
	}
	return fill(rows, f, func(p Point) RecognitionPoint {
		return RecognitionPoint{Point: p, Invoiced: "0", Recognized: "0", Deferred: "0"}
	}, func(r RecognitionPoint) Point { return r.Point }), nil
}
