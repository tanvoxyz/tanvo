-- Revenue analytics (parity §1.44). Months are UTC: each report runs from
-- the organization's creation month, or from_month when set, to to_month.

-- name: AnalyticsGrossRevenue :many
-- The finalized invoices less their refunds, by issuing month, and the
-- fees paid in advance outside invoices, by creation month, per currency
-- and billing entity (gross_revenue.rb:105-119). A fee paid in advance
-- belongs to its subscription's applicable billing entity
-- (subscription.rb:122).
WITH months AS (
  SELECT m AS month
  FROM organizations o,
    generate_series(date_trunc('month', o.created_at AT TIME ZONE 'UTC'),
      sqlc.arg(to_month)::timestamptz AT TIME ZONE 'UTC', interval '1 month') AS m
  WHERE o.id = sqlc.arg(organization_id)
),
revenue AS (
  SELECT date_trunc('month', i.issuing_date::timestamp) AS month, i.currency, i.billing_entity_id, 1 AS invoices_count,
    i.total_amount - COALESCE((
      SELECT SUM(cn.refund_amount) FROM credit_notes cn
      WHERE cn.organization_id = i.organization_id AND cn.invoice_id = i.id), 0) AS amount
  FROM invoices i
  WHERE i.organization_id = sqlc.arg(organization_id)
    AND NOT i.self_billed
    AND i.status = 'finalized'
    AND i.payment_dispute_lost_at IS NULL
    AND (sqlc.narg(customer_id)::uuid IS NULL OR i.customer_id = sqlc.narg(customer_id))
    AND (sqlc.narg(billing_entity_id)::uuid IS NULL OR i.billing_entity_id = sqlc.narg(billing_entity_id))
  UNION ALL
  SELECT date_trunc('month', f.created_at AT TIME ZONE 'UTC'), f.currency,
    COALESCE(s.billing_entity_id, c.billing_entity_id), 0, f.amount
  FROM fees f
  JOIN subscriptions s ON s.organization_id = f.organization_id AND s.id = f.subscription_id
  JOIN customers c ON c.organization_id = s.organization_id AND c.id = s.customer_id
  WHERE f.organization_id = sqlc.arg(organization_id)
    AND f.invoice_id IS NULL
    AND f.pay_in_advance
    AND (sqlc.narg(customer_id)::uuid IS NULL OR c.id = sqlc.narg(customer_id))
    AND (sqlc.narg(billing_entity_id)::uuid IS NULL
         OR COALESCE(s.billing_entity_id, c.billing_entity_id) = sqlc.narg(billing_entity_id))
)
SELECT (m.month AT TIME ZONE 'UTC')::timestamptz AS month, r.currency::text AS currency,
  r.billing_entity_id::uuid AS billing_entity_id,
  SUM(r.invoices_count)::bigint AS invoices_count, SUM(r.amount)::bigint AS amount
FROM months m
JOIN revenue r ON r.month = m.month
WHERE (sqlc.narg(from_month)::timestamptz IS NULL OR m.month >= sqlc.narg(from_month)::timestamptz AT TIME ZONE 'UTC')
  AND (sqlc.narg(currency)::text IS NULL OR r.currency = sqlc.narg(currency))
GROUP BY m.month, r.currency, r.billing_entity_id
ORDER BY m.month, r.currency, r.billing_entity_id;

-- name: AnalyticsMRR :many
-- The subscription fees of finalized invoices, monthly: monthly fees in
-- their issuing month, weekly ones as their average × 4.33, and the
-- longer intervals spread over the months they bill, forward from the
-- issuing month when paid in advance and backward when in arrears; the
-- first in-advance month counts the days left in it.
WITH months AS (
  SELECT m AS month
  FROM organizations o,
    generate_series(date_trunc('month', o.created_at AT TIME ZONE 'UTC'),
      sqlc.arg(to_month)::timestamptz AT TIME ZONE 'UTC', interval '1 month') AS m
  WHERE o.id = sqlc.arg(organization_id)
),
details AS (
  SELECT (f.amount - f.precise_coupons_amount) + f.taxes_amount AS amount,
    f.currency,
    i.issuing_date::timestamp AS issuing_date,
    (EXTRACT(DAY FROM f.to_datetime - f.from_datetime)
      + EXTRACT(HOUR FROM f.to_datetime - f.from_datetime) / 24
      + EXTRACT(MINUTE FROM f.to_datetime - f.from_datetime) / 1440) / 30.44 AS billed_months,
    p.pay_in_advance,
    p.billing_interval
  FROM fees f
  JOIN invoices i ON i.organization_id = f.organization_id AND i.id = f.invoice_id
  JOIN subscriptions s ON s.organization_id = f.organization_id AND s.id = f.subscription_id
  JOIN plans p ON p.organization_id = s.organization_id AND p.id = s.plan_id
  WHERE f.organization_id = sqlc.arg(organization_id)
    AND f.fee_type = 'subscription'
    AND NOT i.self_billed
    AND i.status = 'finalized'
    AND i.payment_dispute_lost_at IS NULL
    AND (sqlc.narg(billing_entity_id)::uuid IS NULL OR i.billing_entity_id = sqlc.narg(billing_entity_id))
),
spread AS (
  SELECT d.*,
    DATE_PART('day', DATE_TRUNC('month', d.issuing_date + INTERVAL '1 month') - d.issuing_date)
      / DATE_PART('day', DATE_TRUNC('month', d.issuing_date + INTERVAL '1 month') - DATE_TRUNC('month', d.issuing_date)) AS first_ratio
  FROM details d
  WHERE d.billing_interval IN ('quarterly', 'semiannual', 'yearly') AND d.billed_months > 0
),
advance AS (
  SELECT DATE_TRUNC('month', s.issuing_date) + INTERVAL '1 month' * gs.month_index AS month,
    CASE
      WHEN gs.month_index = 0 THEN (s.amount / s.billed_months) * s.first_ratio::numeric
      WHEN gs.month_index = CEIL(s.billed_months) - 1 THEN
        s.amount - (s.amount / s.billed_months) * (FLOOR(s.billed_months) - 1 + s.first_ratio::numeric)
      WHEN s.billing_interval = 'semiannual' AND gs.month_index BETWEEN CEIL(s.billed_months) - 4 AND CEIL(s.billed_months) - 2 THEN
        s.amount - (s.amount / s.billed_months) * (FLOOR(s.billed_months) - (CEIL(s.billed_months) - gs.month_index) + s.first_ratio::numeric)
      ELSE s.amount / s.billed_months
    END AS amount,
    s.currency
  FROM spread s, LATERAL generate_series(0, CEIL(s.billed_months)::int - 1) AS gs(month_index)
  WHERE s.pay_in_advance
),
arrears AS (
  SELECT DATE_TRUNC('month', s.issuing_date) - INTERVAL '1 month' * gs.month_index AS month,
    CASE
      WHEN gs.month_index < CEIL(s.billed_months) - 1 THEN s.amount / s.billed_months
      ELSE s.amount - (s.amount / s.billed_months) * (CEIL(s.billed_months) - 1)
    END AS amount,
    s.currency
  FROM spread s, LATERAL generate_series(0, CEIL(s.billed_months)::int - 1) AS gs(month_index)
  WHERE NOT s.pay_in_advance
),
mrr AS (
  SELECT month, amount, currency FROM advance
  UNION ALL
  SELECT month, amount, currency FROM arrears
  UNION ALL
  SELECT DATE_TRUNC('month', d.issuing_date), d.amount, d.currency
  FROM details d WHERE d.billing_interval = 'monthly'
  UNION ALL
  SELECT DATE_TRUNC('month', d.issuing_date), (SUM(d.amount) / COUNT(*)) * 4.33, d.currency
  FROM details d WHERE d.billing_interval = 'weekly'
  GROUP BY DATE_TRUNC('month', d.issuing_date), d.currency
)
SELECT (m.month AT TIME ZONE 'UTC')::timestamptz AS month, COALESCE(r.currency, '')::text AS currency,
  COALESCE(TRUNC(SUM(r.amount)), 0)::bigint AS amount
FROM months m
LEFT JOIN mrr r ON r.month = m.month
WHERE (sqlc.narg(from_month)::timestamptz IS NULL OR m.month >= sqlc.narg(from_month)::timestamptz AT TIME ZONE 'UTC')
  AND (sqlc.narg(currency)::text IS NULL OR r.currency = sqlc.narg(currency))
GROUP BY m.month, r.currency
ORDER BY m.month, r.currency;

-- name: AnalyticsInvoiceCollection :many
-- The finalized invoices by issuing month, currency and payment status;
-- a month without invoices is one row with no status nor currency.
-- @customer_tin_empty keeps the customers without (true) or with (false) a
-- tax identification number (invoice_collection.rb:19-25).
WITH months AS (
  SELECT m AS month
  FROM organizations o,
    generate_series(date_trunc('month', o.created_at AT TIME ZONE 'UTC'),
      sqlc.arg(to_month)::timestamptz AT TIME ZONE 'UTC', interval '1 month') AS m
  WHERE o.id = sqlc.arg(organization_id)
),
collection AS (
  SELECT date_trunc('month', i.issuing_date::timestamp) AS month, i.currency, i.payment_status,
    COUNT(*) AS invoices_count, SUM(i.total_amount) AS amount
  FROM invoices i
  JOIN customers c ON c.organization_id = i.organization_id AND c.id = i.customer_id
  WHERE i.organization_id = sqlc.arg(organization_id)
    AND NOT i.self_billed
    AND i.status = 'finalized'
    AND i.payment_dispute_lost_at IS NULL
    AND (sqlc.narg(billing_entity_id)::uuid IS NULL OR i.billing_entity_id = sqlc.narg(billing_entity_id))
    AND (sqlc.narg(customer_tin_empty)::boolean IS NULL
         OR sqlc.narg(customer_tin_empty)::boolean = (trim(COALESCE(c.tax_identification_number, '')) = ''))
  GROUP BY 1, 2, 3
)
SELECT (m.month AT TIME ZONE 'UTC')::timestamptz AS month, COALESCE(c.payment_status, '')::text AS payment_status,
  COALESCE(c.currency, '')::text AS currency, COALESCE(c.invoices_count, 0)::bigint AS invoices_count,
  COALESCE(c.amount, 0)::bigint AS amount
FROM months m
LEFT JOIN collection c ON c.month = m.month
WHERE (sqlc.narg(from_month)::timestamptz IS NULL OR m.month >= sqlc.narg(from_month)::timestamptz AT TIME ZONE 'UTC')
  AND (sqlc.narg(currency)::text IS NULL OR c.currency = sqlc.narg(currency))
ORDER BY m.month, c.payment_status, c.currency;

-- name: AnalyticsInvoicedUsage :many
-- The usage fees of invoices by creation month and billable metric, after
-- coupons; the latest month first, then the largest amount.
WITH months AS (
  SELECT m AS month
  FROM organizations o,
    generate_series(date_trunc('month', o.created_at AT TIME ZONE 'UTC'),
      sqlc.arg(to_month)::timestamptz AT TIME ZONE 'UTC', interval '1 month') AS m
  WHERE o.id = sqlc.arg(organization_id)
),
usage AS (
  SELECT date_trunc('month', f.created_at AT TIME ZONE 'UTC') AS month, bm.code, f.currency,
    SUM(f.amount - f.precise_coupons_amount) AS amount
  FROM fees f
  JOIN invoices i ON i.organization_id = f.organization_id AND i.id = f.invoice_id
  LEFT JOIN charges ch ON ch.organization_id = f.organization_id AND ch.id = f.charge_id
  LEFT JOIN billable_metrics bm ON bm.organization_id = ch.organization_id AND bm.id = ch.billable_metric_id
  WHERE f.organization_id = sqlc.arg(organization_id)
    AND f.fee_type = 'charge'
    AND NOT i.self_billed
    AND i.payment_dispute_lost_at IS NULL
    AND (sqlc.narg(billing_entity_id)::uuid IS NULL OR i.billing_entity_id = sqlc.narg(billing_entity_id))
  GROUP BY 1, 2, 3
)
SELECT (m.month AT TIME ZONE 'UTC')::timestamptz AS month, COALESCE(u.code, '')::text AS code,
  u.currency::text AS currency, TRUNC(u.amount)::bigint AS amount
FROM months m
JOIN usage u ON u.month = m.month
WHERE (sqlc.narg(from_month)::timestamptz IS NULL OR m.month >= sqlc.narg(from_month)::timestamptz AT TIME ZONE 'UTC')
  AND (sqlc.narg(currency)::text IS NULL OR u.currency = sqlc.narg(currency))
ORDER BY m.month DESC, u.amount DESC, u.code;

-- name: AnalyticsOverdueBalance :many
-- The unpaid balance, less the finalized credit notes' offsets, of the
-- overdue invoices by payment due month, currency and billing entity
-- (overdue_balance.rb:87-102); @customer_tin_empty as for
-- AnalyticsInvoiceCollection.
WITH months AS (
  SELECT m AS month
  FROM organizations o,
    generate_series(date_trunc('month', o.created_at AT TIME ZONE 'UTC'),
      sqlc.arg(to_month)::timestamptz AT TIME ZONE 'UTC', interval '1 month') AS m
  WHERE o.id = sqlc.arg(organization_id)
),
overdue AS (
  SELECT date_trunc('month', i.payment_due_date::timestamp) AS month, i.currency, i.billing_entity_id, i.id,
    i.total_amount - i.total_paid_amount - COALESCE((
      SELECT SUM(cn.offset_amount) FROM credit_notes cn
      WHERE cn.organization_id = i.organization_id AND cn.invoice_id = i.id AND cn.status = 'finalized'), 0) AS amount
  FROM invoices i
  JOIN customers c ON c.organization_id = i.organization_id AND c.id = i.customer_id
  WHERE i.organization_id = sqlc.arg(organization_id)
    AND NOT i.self_billed
    AND i.payment_overdue
    AND (sqlc.narg(customer_id)::uuid IS NULL OR i.customer_id = sqlc.narg(customer_id))
    AND (sqlc.narg(billing_entity_id)::uuid IS NULL OR i.billing_entity_id = sqlc.narg(billing_entity_id))
    AND (sqlc.narg(customer_tin_empty)::boolean IS NULL
         OR sqlc.narg(customer_tin_empty)::boolean = (trim(COALESCE(c.tax_identification_number, '')) = ''))
)
SELECT (m.month AT TIME ZONE 'UTC')::timestamptz AS month, o.currency::text AS currency,
  o.billing_entity_id::uuid AS billing_entity_id, SUM(o.amount)::bigint AS amount, array_agg(o.id ORDER BY o.id)::uuid[] AS invoice_ids
FROM months m
JOIN overdue o ON o.month = m.month
WHERE (sqlc.narg(from_month)::timestamptz IS NULL OR m.month >= sqlc.narg(from_month)::timestamptz AT TIME ZONE 'UTC')
  AND (sqlc.narg(currency)::text IS NULL OR o.currency = sqlc.narg(currency))
GROUP BY m.month, o.currency, o.billing_entity_id
ORDER BY m.month, o.currency, o.billing_entity_id;
