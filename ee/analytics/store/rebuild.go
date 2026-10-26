// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/tanvoxyz/tanvo/ee/analytics/mrr"
	"github.com/tanvoxyz/tanvo/ee/analytics/period"
)

// rebuildPlans records the plans of org, marking those gone from the
// views deleted.
func rebuildPlans(ctx context.Context, tx pgx.Tx, org string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO analytics.plans (organization_id, id, code, name, billing_interval, deleted)
		SELECT organization_id, id, code, name, billing_interval, false FROM exports_plans WHERE organization_id = $1
		ON CONFLICT (organization_id, id) DO UPDATE SET code = EXCLUDED.code, name = EXCLUDED.name,
			billing_interval = EXCLUDED.billing_interval, deleted = false`, org); err != nil {
		return fmt.Errorf("plans: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE analytics.plans SET deleted = true
		WHERE organization_id = $1 AND NOT deleted AND id NOT IN (SELECT id FROM exports_plans WHERE organization_id = $1)`, org); err != nil {
		return fmt.Errorf("plans: %w", err)
	}
	return nil
}

// mrrFeesSQL lists the subscription fees MRR is made of: those of
// finalized invoices, not self-billed and without a lost dispute.
const mrrFeesSQL = `SELECT f.subscription_id::text, s.customer_id::text, s.plan_id::text,
  COALESCE(s.billing_entity_id, c.billing_entity_id)::text, s.terminated_at, c.applicable_timezone,
  f.currency, (f.amount - f.precise_coupons_amount)::text, f.from_datetime, f.to_datetime
FROM exports_fees f
JOIN exports_invoices i ON i.organization_id = f.organization_id AND i.id = f.invoice_id
JOIN exports_subscriptions s ON s.organization_id = f.organization_id AND s.id = f.subscription_id
JOIN exports_customers c ON c.organization_id = s.organization_id AND c.id = s.customer_id
WHERE f.organization_id = $1 AND f.fee_type = 'subscription' AND f.deleted_at IS NULL
  AND i.status = 'finalized' AND NOT i.self_billed AND i.payment_dispute_lost_at IS NULL
  AND f.from_datetime IS NOT NULL AND f.to_datetime IS NOT NULL
ORDER BY f.subscription_id, f.currency, f.from_datetime`

// subscriptionMRR is the MRR source of a subscription in a currency.
type subscriptionMRR struct {
	subscription, customer, plan, billingEntity, currency string
	terminated                                            *time.Time
	fees                                                  []mrr.Fee
}

// rebuildMRR replaces the MRR intervals of org.
func rebuildMRR(ctx context.Context, tx pgx.Tx, org string) error {
	rows, err := tx.Query(ctx, mrrFeesSQL, org)
	if err != nil {
		return fmt.Errorf("mrr fees: %w", err)
	}
	locs := map[string]*time.Location{}
	var subs []*subscriptionMRR
	var (
		sub, customer, plan, currency, net, tz string
		billingEntity                          *string
		terminated                             *time.Time
		from, to                               time.Time
	)
	if _, err := pgx.ForEachRow(rows, []any{&sub, &customer, &plan, &billingEntity, &terminated, &tz, &currency, &net, &from, &to}, func() error {
		loc, ok := locs[tz]
		if !ok {
			l, err := time.LoadLocation(tz)
			if err != nil {
				return fmt.Errorf("time zone %q: %w", tz, err)
			}
			loc, locs[tz] = l, l
		}
		if n := len(subs); n == 0 || subs[n-1].subscription != sub || subs[n-1].currency != currency {
			s := &subscriptionMRR{subscription: sub, customer: customer, plan: plan, currency: currency}
			if billingEntity != nil {
				s.billingEntity = *billingEntity
			}
			if terminated != nil {
				d := period.Day(*terminated, loc)
				s.terminated = &d
			}
			subs = append(subs, s)
		}
		f := mrr.Fee{Start: period.Day(from, loc), End: period.Day(to.Add(time.Second), loc)}
		if _, _, err := f.Net.SetString(net); err != nil {
			return fmt.Errorf("fee amount %q: %w", net, err)
		}
		subs[len(subs)-1].fees = append(subs[len(subs)-1].fees, f)
		return nil
	}); err != nil {
		return fmt.Errorf("mrr fees: %w", err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM analytics.mrr_intervals WHERE organization_id = $1", org); err != nil {
		return fmt.Errorf("mrr: %w", err)
	}
	var out [][]any
	for _, s := range subs {
		ivs, err := mrr.Intervals(s.fees, s.terminated)
		if err != nil {
			return fmt.Errorf("mrr of subscription %s: %w", s.subscription, err)
		}
		for _, iv := range ivs {
			row, err := mrrRow(org, s, iv)
			if err != nil {
				return err
			}
			out = append(out, row)
		}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"analytics", "mrr_intervals"},
		[]string{"organization_id", "subscription_id", "customer_id", "plan_id", "billing_entity_id", "currency", "start_date", "end_date", "amount"},
		pgx.CopyFromRows(out)); err != nil {
		return fmt.Errorf("mrr: copy: %w", err)
	}
	return nil
}

func mrrRow(org string, s *subscriptionMRR, iv mrr.Interval) ([]any, error) {
	ids := make([]pgtype.UUID, 5)
	for i, v := range []string{org, s.subscription, s.customer, s.plan, s.billingEntity} {
		if v == "" {
			continue
		}
		if err := ids[i].Scan(v); err != nil {
			return nil, fmt.Errorf("mrr: id %q: %w", v, err)
		}
	}
	var amount pgtype.Numeric
	if err := amount.Scan(iv.Amount.Text('f')); err != nil {
		return nil, fmt.Errorf("mrr: amount: %w", err)
	}
	var end any
	if iv.End != nil {
		end = *iv.End
	}
	return []any{ids[0], ids[1], ids[2], ids[3], ids[4], s.currency, iv.Start, end, amount}, nil
}

// rebuildRecognition replaces the recognition schedules of org: each
// fee's net revenue over its service period, or on its issuing day
// without one.
func rebuildRecognition(ctx context.Context, tx pgx.Tx, org string) error {
	if _, err := tx.Exec(ctx, "DELETE FROM analytics.recognition_schedules WHERE organization_id = $1", org); err != nil {
		return fmt.Errorf("recognition: %w", err)
	}
	q := newQuery(org)
	sql := "WITH " + revenueCTE(q, nil) + `
INSERT INTO analytics.recognition_schedules (organization_id, fee_id, invoice_id, customer_id, subscription_id, plan_id,
  billing_entity_id, currency, amount, issuing_date, start_date, end_date)
SELECT $1::uuid, fee_id, invoice_id, customer_id, subscription_id, plan_id, billing_entity_id, currency, net, issuing_date,
  CASE WHEN service_end > service_start THEN service_start ELSE issuing_date END,
  CASE WHEN service_end > service_start THEN service_end ELSE issuing_date + 1 END
FROM rev`
	if _, err := tx.Exec(ctx, sql, q.args...); err != nil {
		return fmt.Errorf("recognition: %w", err)
	}
	return nil
}

// revenueCTE returns the CTE rev of the revenue of each fee: the fees of
// finalized invoices, not self-billed and without a lost dispute, and the
// fees paid in advance outside invoices and not discarded, credit
// purchases excluded. An
// invoice's coupons, credit note credits, granted and purchased prepaid
// credits and progressive billing credits are allocated to its fees in
// proportion to their amounts; net is the amount less all but the
// purchased credits, a means of payment. Amounts are in minor units. With
// r, only the fees issued from r[0] to r[1] are kept.
func revenueCTE(q *query, r *[2]time.Time) string {
	dates := "TRUE"
	if r != nil {
		dates = "COALESCE(i.issuing_date, (f.created_at AT TIME ZONE c.applicable_timezone)::date) BETWEEN " +
			q.arg(r[0]) + "::date AND " + q.arg(r[1]) + "::date"
	}
	return `rev AS (
  SELECT x.*, x.gross - x.coupons - x.credit_notes - x.free_credits - x.progressive AS net FROM (
    SELECT f.id AS fee_id, f.invoice_id, f.fee_type, f.subscription_id, f.customer_id, s.plan_id,
      COALESCE(i.billing_entity_id, s.billing_entity_id, c.billing_entity_id) AS billing_entity_id,
      f.currency,
      COALESCE(i.issuing_date, (f.created_at AT TIME ZONE c.applicable_timezone)::date) AS issuing_date,
      f.amount::numeric AS gross,
      COALESCE(i.coupons_amount::numeric * f.amount / NULLIF(sum(f.amount) OVER w, 0), 0) AS coupons,
      COALESCE(i.credit_notes_amount::numeric * f.amount / NULLIF(sum(f.amount) OVER w, 0), 0) AS credit_notes,
      COALESCE(i.prepaid_granted_credit_amount::numeric * f.amount / NULLIF(sum(f.amount) OVER w, 0), 0) AS free_credits,
      COALESCE(i.progressive_billing_credit_amount::numeric * f.amount / NULLIF(sum(f.amount) OVER w, 0), 0) AS progressive,
      COALESCE(i.prepaid_purchased_credit_amount::numeric * f.amount / NULLIF(sum(f.amount) OVER w, 0), 0) AS prepaid,
      (f.from_datetime AT TIME ZONE c.applicable_timezone)::date AS service_start,
      ((f.to_datetime + interval '1 second') AT TIME ZONE c.applicable_timezone)::date AS service_end
    FROM exports_fees f
    LEFT JOIN exports_invoices i ON i.organization_id = f.organization_id AND i.id = f.invoice_id
      AND i.status = 'finalized' AND NOT i.self_billed AND i.payment_dispute_lost_at IS NULL
    LEFT JOIN exports_subscriptions s ON s.organization_id = f.organization_id AND s.id = f.subscription_id
    JOIN exports_customers c ON c.organization_id = f.organization_id AND c.id = f.customer_id
    WHERE f.organization_id = $1 AND f.fee_type <> 'credit' AND f.deleted_at IS NULL
      AND (i.id IS NOT NULL OR (f.invoice_id IS NULL AND f.pay_in_advance))
      AND ` + dates + `
    WINDOW w AS (PARTITION BY f.invoice_id)
  ) x
)`
}
