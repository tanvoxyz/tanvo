-- Export views (parity §1.43): the read-only contract of the downstream
-- sync of business data to data warehouses. Each view exports one business
-- table with its enumerations as text, its owner and parent IDs and the
-- derived amounts readers need; the sync reads them incrementally by
-- updated_at (created_at for rows never updated) and applies the deletion
-- records of exports_record_deletions.
--
-- The views are security_invoker, so that the row-level security of the
-- reading role applies to the tables they read: an application role sees
-- its organization's rows only.
--
-- Fee taxes and item metadata are columns of their owners (fees.applied_taxes,
-- the metadata of plans, credit notes and wallets), exported with them: an
-- owner's update re-exports them, so they need no view or deletion records
-- of their own.

-- Billable metrics not deleted, with their live filters.
CREATE VIEW exports_billable_metrics WITH (security_invoker = true) AS
SELECT
  bm.organization_id,
  bm.id,
  bm.code,
  bm.name,
  bm.description,
  bm.aggregation_type,
  bm.field_name,
  bm.weighted_interval,
  bm.recurring,
  bm.expression,
  bm.rounding_function,
  bm.rounding_precision,
  COALESCE((
    SELECT json_agg(json_build_object('key', f.key, 'values', f.filter_values) ORDER BY f.position)
    FROM billable_metric_filters AS f
    WHERE f.organization_id = bm.organization_id AND f.billable_metric_id = bm.id AND f.deleted_at IS NULL
  ), '[]'::json) AS filters,
  bm.created_at,
  bm.updated_at
FROM billable_metrics AS bm
WHERE bm.deleted_at IS NULL;

CREATE VIEW exports_billing_entities WITH (security_invoker = true) AS
SELECT
  be.organization_id,
  be.id,
  be.code,
  be.name,
  be.legal_name,
  be.legal_number,
  be.tax_id,
  be.email,
  be.address_line1,
  be.address_line2,
  be.address_city,
  be.address_state,
  be.address_zipcode,
  be.address_country,
  be.timezone,
  be.default_currency,
  be.is_default,
  be.created_at,
  be.updated_at,
  be.archived_at
FROM billing_entities AS be;

-- Plans not deleted, with their taxes.
CREATE VIEW exports_plans WITH (security_invoker = true) AS
SELECT
  p.organization_id,
  p.id,
  p.code,
  p.name,
  p.invoice_display_name,
  p.description,
  p.billing_interval,
  p.amount,
  p.currency,
  p.trial_period_days,
  p.pay_in_advance,
  p.bill_charges_monthly,
  p.bill_fixed_charges_monthly,
  p.parent_id,
  p.status,
  p.metadata,
  to_json(ARRAY(
    SELECT pt.tax_id FROM plan_taxes AS pt
    WHERE pt.organization_id = p.organization_id AND pt.plan_id = p.id
    ORDER BY pt.position
  )) AS tax_ids,
  p.created_at,
  p.updated_at
FROM plans AS p
WHERE p.status <> 'deleted';

-- Charges, deleted ones included (deleted_at), with their filters.
CREATE VIEW exports_charges WITH (security_invoker = true) AS
SELECT
  c.organization_id,
  c.id,
  c.plan_id,
  c.parent_id,
  c.billable_metric_id,
  c.code,
  c.invoice_display_name,
  c.charge_model,
  c.properties,
  c.pay_in_advance,
  c.invoiceable,
  c.regroup_paid_fees,
  c.prorated,
  c.min_amount,
  c.applied_pricing_unit_id,
  c.conversion_rate,
  (
    SELECT json_agg(json_build_object(
      'id', cf.id,
      'code', cf.code,
      'invoice_display_name', cf.invoice_display_name,
      'properties', cf.properties,
      'values', cf.filter_values,
      'deleted_at', cf.deleted_at
    ) ORDER BY cf.position)
    FROM charge_filters AS cf
    WHERE cf.organization_id = c.organization_id AND cf.charge_id = c.id
  ) AS filters,
  c.created_at,
  c.updated_at,
  c.deleted_at
FROM charges AS c;

CREATE VIEW exports_taxes WITH (security_invoker = true) AS
SELECT
  tx.organization_id,
  tx.id,
  tx.code,
  tx.name,
  tx.rate,
  tx.description,
  to_json(ARRAY(
    SELECT bt.billing_entity_id FROM billing_entity_taxes AS bt
    WHERE bt.organization_id = tx.organization_id AND bt.tax_id = tx.id
    ORDER BY bt.billing_entity_id
  )) AS billing_entity_ids,
  tx.created_at,
  tx.updated_at,
  tx.deleted_at
FROM taxes AS tx;

-- Usage thresholds of plans and subscriptions, deleted ones included.
CREATE VIEW exports_usage_thresholds WITH (security_invoker = true) AS
SELECT
  ut.organization_id,
  ut.id,
  ut.plan_id,
  ut.subscription_id,
  ut.amount,
  ut.recurring,
  ut.threshold_display_name,
  ut.created_at,
  ut.updated_at,
  ut.deleted_at
FROM usage_thresholds AS ut;

-- Features, with their privileges, deleted ones included.
CREATE VIEW exports_features WITH (security_invoker = true) AS
SELECT
  f.organization_id,
  f.id,
  f.code,
  f.name,
  f.description,
  COALESCE((
    SELECT json_agg(json_build_object(
      'id', fp.id,
      'code', fp.code,
      'name', fp.name,
      'value_type', fp.value_type,
      'select_options', fp.select_options,
      'deleted_at', fp.deleted_at
    ) ORDER BY fp.position)
    FROM feature_privileges AS fp
    WHERE fp.organization_id = f.organization_id AND fp.feature_id = f.id
  ), '[]'::json) AS privileges,
  f.created_at,
  f.updated_at,
  f.deleted_at
FROM features AS f;

-- Entitlements of plans and subscriptions with their privilege values
-- (privilege code to value), deleted ones included.
CREATE VIEW exports_entitlements WITH (security_invoker = true) AS
SELECT
  e.organization_id,
  e.id,
  e.feature_id,
  e.plan_id,
  e.subscription_id,
  e.privilege_values,
  e.created_at,
  e.updated_at,
  e.deleted_at
FROM entitlements AS e;

-- Coupons with their plan and billable metric limitations.
CREATE VIEW exports_coupons WITH (security_invoker = true) AS
SELECT
  cp.organization_id,
  cp.id,
  cp.code,
  cp.name,
  cp.description,
  cp.coupon_type,
  cp.amount,
  cp.amount_currency,
  cp.percentage_rate,
  cp.frequency,
  cp.frequency_duration,
  cp.reusable,
  EXISTS (
    SELECT 1 FROM coupon_plans AS t WHERE t.organization_id = cp.organization_id AND t.coupon_id = cp.id
  ) AS limited_plans,
  EXISTS (
    SELECT 1 FROM coupon_billable_metrics AS t WHERE t.organization_id = cp.organization_id AND t.coupon_id = cp.id
  ) AS limited_billable_metrics,
  to_json(ARRAY(
    SELECT t.plan_id FROM coupon_plans AS t
    WHERE t.organization_id = cp.organization_id AND t.coupon_id = cp.id
    ORDER BY t.position
  )) AS plan_ids,
  to_json(ARRAY(
    SELECT t.billable_metric_id FROM coupon_billable_metrics AS t
    WHERE t.organization_id = cp.organization_id AND t.coupon_id = cp.id
    ORDER BY t.position
  )) AS billable_metric_ids,
  cp.expiration,
  cp.expiration_at,
  cp.status,
  cp.created_at,
  cp.updated_at,
  cp.terminated_at,
  cp.deleted_at
FROM coupons AS cp;

-- Customers with their default payment provider customer and their taxes.
-- The applicable time zone and grace period fall back to the billing
-- entity's (domain §6).
CREATE VIEW exports_customers WITH (security_invoker = true) AS
SELECT
  c.organization_id,
  c.id,
  c.billing_entity_id,
  c.external_id,
  c.account_type,
  c.customer_type,
  c.name,
  c.firstname,
  c.lastname,
  c.legal_name,
  c.legal_number,
  c.tax_identification_number,
  c.sequential_id,
  c.slug,
  c.email,
  c.phone,
  c.url,
  c.billing_address_line1,
  c.billing_address_line2,
  c.billing_address_city,
  c.billing_address_state,
  c.billing_address_zipcode,
  c.billing_address_country,
  c.shipping_address_line1,
  c.shipping_address_line2,
  c.shipping_address_city,
  c.shipping_address_state,
  c.shipping_address_zipcode,
  c.shipping_address_country,
  c.currency,
  c.timezone,
  COALESCE(c.timezone, be.timezone, 'UTC') AS applicable_timezone,
  c.net_payment_term_days,
  c.invoice_grace_period_days,
  COALESCE(c.invoice_grace_period_days, be.invoice_grace_period_days) AS applicable_invoice_grace_period_days,
  c.document_locale,
  c.finalize_zero_amount_invoice,
  c.skip_invoice_custom_sections,
  i.provider AS payment_provider,
  i.code AS payment_provider_code,
  ppc.provider_customer_id,
  c.metadata,
  to_json(ARRAY(
    SELECT ct.tax_id FROM customer_taxes AS ct
    WHERE ct.organization_id = c.organization_id AND ct.customer_id = c.id
    ORDER BY ct.position
  )) AS tax_ids,
  c.created_at,
  c.updated_at,
  c.deleted_at
FROM customers AS c
JOIN billing_entities AS be ON be.organization_id = c.organization_id AND be.id = c.billing_entity_id
LEFT JOIN payment_provider_customers AS ppc ON ppc.organization_id = c.organization_id AND ppc.customer_id = c.id
  AND ppc.is_default AND ppc.deleted_at IS NULL
LEFT JOIN integrations AS i ON i.organization_id = ppc.organization_id AND i.id = ppc.integration_id;

CREATE VIEW exports_integration_customers WITH (security_invoker = true) AS
SELECT
  ic.organization_id,
  ic.id,
  ic.customer_id,
  ic.integration_id,
  ic.category,
  i.provider,
  ic.external_customer_id,
  ic.subsidiary_id,
  ic.sync_with_provider,
  ic.settings,
  ic.created_at,
  ic.updated_at
FROM integration_customers AS ic
JOIN integrations AS i ON i.organization_id = ic.organization_id AND i.id = ic.integration_id;

-- Applied coupons with the credits they granted.
CREATE VIEW exports_applied_coupons WITH (security_invoker = true) AS
SELECT
  ac.organization_id,
  ac.id,
  ac.coupon_id,
  ac.customer_id,
  ac.status,
  ac.amount,
  ac.remaining_amount,
  ac.amount_currency,
  ac.percentage_rate,
  ac.frequency,
  ac.frequency_duration,
  ac.frequency_duration_remaining,
  (
    SELECT json_agg(json_build_object(
      'id', cr.id,
      'invoice_id', cr.invoice_id,
      'amount', cr.amount,
      'currency', cr.currency,
      'before_taxes', cr.before_taxes
    ) ORDER BY cr.created_at, cr.id)
    FROM invoice_credits AS cr
    WHERE cr.organization_id = ac.organization_id AND cr.applied_coupon_id = ac.id
  ) AS credits,
  ac.created_at,
  ac.updated_at,
  ac.terminated_at
FROM applied_coupons AS ac;

CREATE VIEW exports_subscriptions WITH (security_invoker = true) AS
SELECT
  s.organization_id,
  s.id,
  s.external_id,
  s.customer_id,
  s.plan_id,
  s.billing_entity_id,
  s.name,
  s.status,
  s.billing_time,
  s.subscription_at,
  s.started_at,
  s.trial_ended_at,
  s.ending_at,
  s.terminated_at,
  s.canceled_at,
  s.cancellation_reason,
  s.purchase_order_number,
  s.previous_subscription_id,
  to_json(ARRAY(
    SELECT ns.id FROM subscriptions AS ns
    WHERE ns.organization_id = s.organization_id AND ns.previous_subscription_id = s.id
    ORDER BY ns.created_at, ns.id
  )) AS next_subscription_ids,
  s.created_at,
  s.updated_at
FROM subscriptions AS s;

-- Invoices but those generating, closed or open: an open invoice is
-- exported only while its provider taxes are pending (the invoice lago
-- calls pending). Deleted invoices are exported with their status, so that
-- an upsert-only destination receives the deletion.
CREATE VIEW exports_invoices WITH (security_invoker = true) AS
SELECT
  i.organization_id,
  i.id,
  i.customer_id,
  i.billing_entity_id,
  i.sequential_id,
  i.billing_entity_sequential_id,
  i.number,
  i.invoice_type,
  i.status,
  i.payment_status,
  i.tax_status,
  i.issuing_date,
  i.payment_due_date,
  i.net_payment_term_days,
  i.payment_overdue,
  i.payment_dispute_lost_at,
  i.currency,
  i.fees_amount,
  i.taxes_amount,
  i.progressive_billing_credit_amount,
  i.coupons_amount,
  i.credit_notes_amount,
  i.sub_total_excluding_taxes_amount,
  i.sub_total_including_taxes_amount,
  i.total_amount,
  i.total_paid_amount,
  i.total_amount - i.total_paid_amount AS total_due_amount,
  i.prepaid_credit_amount,
  i.prepaid_granted_credit_amount,
  i.prepaid_purchased_credit_amount,
  i.voided_invoice_id,
  i.purchase_order_number,
  i.metadata,
  (
    SELECT json_agg(json_build_object(
      'id', ed.id,
      'error_code', ed.error_code,
      'details', ed.details
    ) ORDER BY ed.created_at, ed.id)
    FROM invoice_error_details AS ed
    WHERE ed.organization_id = i.organization_id AND ed.invoice_id = i.id
  ) AS error_details,
  i.created_at,
  i.updated_at,
  i.finalized_at,
  i.voided_at
FROM invoices AS i
WHERE i.status IN ('draft', 'finalized', 'voided', 'failed', 'deleted')
  OR (i.status = 'open' AND i.tax_status = 'pending');

-- Invoice subscriptions of invoices not deleted; their rows are never
-- updated.
CREATE VIEW exports_invoice_subscriptions WITH (security_invoker = true) AS
SELECT
  ins.organization_id,
  ins.id,
  ins.invoice_id,
  ins.regenerated_invoice_id,
  ins.subscription_id,
  ins.invoicing_reason,
  ins.recurring,
  ins.from_datetime,
  ins.to_datetime,
  NULLIF(ins.charges_from_datetime, '0001-01-01 00:00:00+00') AS charges_from_datetime,
  NULLIF(ins.charges_to_datetime, '0001-01-01 00:00:00+00') AS charges_to_datetime,
  NULLIF(ins.fixed_charges_from_datetime, '0001-01-01 00:00:00+00') AS fixed_charges_from_datetime,
  NULLIF(ins.fixed_charges_to_datetime, '0001-01-01 00:00:00+00') AS fixed_charges_to_datetime,
  ins.billing_timestamp,
  ins.created_at,
  ins.created_at AS updated_at
FROM invoice_subscriptions AS ins
JOIN invoices AS i ON i.organization_id = ins.organization_id AND i.id = ins.invoice_id
WHERE i.status <> 'deleted';

-- Applied taxes of invoices not deleted; their rows are never updated.
CREATE VIEW exports_invoice_applied_taxes WITH (security_invoker = true) AS
SELECT
  it.organization_id,
  it.id,
  it.invoice_id,
  it.tax_id,
  it.tax_code,
  it.tax_name,
  it.tax_description,
  it.tax_rate,
  it.fees_amount,
  it.taxable_base_amount,
  it.amount,
  it.precise_amount,
  it.currency,
  it.created_at,
  it.created_at AS updated_at
FROM invoice_applied_taxes AS it
JOIN invoices AS i ON i.organization_id = it.organization_id AND i.id = it.invoice_id
WHERE i.status <> 'deleted';

-- Settlements of invoices by payments and credit notes; their rows are
-- never updated.
CREATE VIEW exports_invoice_settlements WITH (security_invoker = true) AS
SELECT
  st.organization_id,
  st.id,
  i.billing_entity_id,
  st.invoice_id,
  st.settlement_type,
  st.payment_id,
  st.credit_note_id,
  st.amount,
  st.currency,
  st.created_at,
  st.created_at AS updated_at
FROM invoice_settlements AS st
JOIN invoices AS i ON i.organization_id = st.organization_id AND i.id = st.invoice_id;

-- Fees of invoices not deleted or of no invoice, with their
-- item (the billable metric, add-on, wallet transaction, product or
-- subscription billed), their billed period and their applied taxes. The
-- customer is the subscription's, else the invoice's. Deleted fees are
-- exported with deleted_at, hard-deleted ones by their deletion records.
CREATE VIEW exports_fees WITH (security_invoker = true) AS
SELECT
  f.organization_id,
  f.id,
  f.invoice_id,
  f.subscription_id,
  COALESCE(s.customer_id, i.customer_id) AS customer_id,
  f.fee_type,
  f.charge_id,
  f.charge_filter_id,
  f.fixed_charge_id,
  f.add_on_id,
  f.commitment_id,
  f.wallet_transaction_id,
  f.product_id,
  json_build_object(
    'type', f.fee_type,
    'item_type', CASE f.fee_type
      WHEN 'charge' THEN 'billable_metric'
      WHEN 'add_on' THEN 'add_on'
      WHEN 'fixed_charge' THEN 'add_on'
      WHEN 'credit' THEN 'wallet_transaction'
      WHEN 'product' THEN 'product'
      ELSE 'subscription'
    END,
    'item_id', CASE f.fee_type
      WHEN 'charge' THEN bm.id
      WHEN 'add_on' THEN ao.id
      WHEN 'fixed_charge' THEN fao.id
      WHEN 'credit' THEN f.wallet_transaction_id
      WHEN 'product' THEN pr.id
      ELSE f.subscription_id
    END,
    'code', CASE f.fee_type
      WHEN 'charge' THEN bm.code
      WHEN 'add_on' THEN ao.code
      WHEN 'fixed_charge' THEN fao.code
      WHEN 'credit' THEN 'credit'
      WHEN 'product' THEN pr.code
      ELSE p.code
    END,
    'name', CASE f.fee_type
      WHEN 'charge' THEN bm.name
      WHEN 'add_on' THEN ao.name
      WHEN 'fixed_charge' THEN fao.name
      WHEN 'credit' THEN 'credit'
      WHEN 'product' THEN pr.name
      ELSE p.name
    END,
    'description', CASE f.fee_type
      WHEN 'charge' THEN bm.description
      WHEN 'add_on' THEN ao.description
      WHEN 'fixed_charge' THEN fao.description
      WHEN 'credit' THEN 'credit'
      WHEN 'product' THEN pr.description
      ELSE p.description
    END,
    'invoice_display_name', COALESCE(f.invoice_display_name, CASE f.fee_type
      WHEN 'charge' THEN COALESCE(ch.invoice_display_name, bm.name)
      WHEN 'add_on' THEN COALESCE(ao.invoice_display_name, ao.name)
      WHEN 'fixed_charge' THEN COALESCE(fc.invoice_display_name, fao.invoice_display_name, fao.name)
      WHEN 'credit' THEN 'credit'
      WHEN 'product' THEN COALESCE(pr.invoice_display_name, pr.name)
      ELSE p.invoice_display_name
    END),
    'filters', (
      SELECT json_agg(json_build_object(
        'id', cf.id,
        'charge_id', cf.charge_id,
        'invoice_display_name', cf.invoice_display_name,
        'properties', cf.properties,
        'values', cf.filter_values
      ) ORDER BY cf.position)
      FROM charge_filters AS cf
      WHERE cf.organization_id = f.organization_id AND cf.charge_id = f.charge_id
    ),
    'grouped_by', f.grouped_by
  ) AS item,
  f.description,
  f.pay_in_advance,
  f.invoiceable,
  f.currency,
  f.units,
  f.total_aggregated_units,
  f.events_count,
  f.amount,
  f.precise_amount,
  f.unit_amount,
  f.precise_unit_amount,
  f.precise_coupons_amount,
  f.precise_credit_notes_amount,
  f.taxes_amount,
  f.taxes_precise_amount,
  f.taxes_rate,
  f.amount + f.taxes_amount AS total_amount,
  f.precise_amount + f.taxes_precise_amount AS precise_total_amount,
  f.amount_details,
  f.applied_taxes,
  f.payment_status,
  CASE f.fee_type
    WHEN 'charge' THEN NULLIF(f.charges_from_datetime, '0001-01-01 00:00:00+00')
    WHEN 'fixed_charge' THEN NULLIF(f.fixed_charges_from_datetime, '0001-01-01 00:00:00+00')
    ELSE NULLIF(f.from_datetime, '0001-01-01 00:00:00+00')
  END AS from_datetime,
  CASE f.fee_type
    WHEN 'charge' THEN NULLIF(f.charges_to_datetime, '0001-01-01 00:00:00+00')
    WHEN 'fixed_charge' THEN NULLIF(f.fixed_charges_to_datetime, '0001-01-01 00:00:00+00')
    ELSE NULLIF(f.to_datetime, '0001-01-01 00:00:00+00')
  END AS to_datetime,
  f.created_at,
  f.updated_at,
  f.succeeded_at,
  f.failed_at,
  f.refunded_at,
  f.deleted_at
FROM fees AS f
LEFT JOIN invoices AS i ON i.organization_id = f.organization_id AND i.id = f.invoice_id
LEFT JOIN subscriptions AS s ON s.organization_id = f.organization_id AND s.id = f.subscription_id
LEFT JOIN plans AS p ON p.organization_id = s.organization_id AND p.id = s.plan_id
LEFT JOIN charges AS ch ON ch.organization_id = f.organization_id AND ch.id = f.charge_id
LEFT JOIN billable_metrics AS bm ON bm.organization_id = ch.organization_id AND bm.id = ch.billable_metric_id
LEFT JOIN add_ons AS ao ON ao.organization_id = f.organization_id AND ao.id = f.add_on_id
LEFT JOIN fixed_charges AS fc ON fc.organization_id = f.organization_id AND fc.id = f.fixed_charge_id
LEFT JOIN add_ons AS fao ON fao.organization_id = fc.organization_id AND fao.id = fc.add_on_id
LEFT JOIN products AS pr ON pr.organization_id = f.organization_id AND pr.id = f.product_id
WHERE i.status IS DISTINCT FROM 'deleted';

-- Credit notes not deleted, with their items and error details.
CREATE VIEW exports_credit_notes WITH (security_invoker = true) AS
SELECT
  cn.organization_id,
  cn.id,
  cn.invoice_id,
  cn.customer_id,
  cn.sequential_id,
  cn.number,
  cn.issuing_date,
  cn.status,
  cn.credit_status,
  cn.refund_status,
  cn.reason,
  cn.description,
  cn.currency,
  cn.total_amount,
  cn.taxes_amount,
  cn.taxes_rate,
  cn.sub_total_excluding_taxes_amount,
  cn.balance_amount,
  cn.credit_amount,
  cn.refund_amount,
  cn.offset_amount,
  cn.coupons_adjustment_amount,
  (
    SELECT json_agg(json_build_object(
      'id', it.id,
      'fee_id', it.fee_id,
      'amount', it.amount,
      'precise_amount', it.precise_amount
    ) ORDER BY it.position)
    FROM credit_note_items AS it
    WHERE it.organization_id = cn.organization_id AND it.credit_note_id = cn.id
  ) AS items,
  cn.metadata,
  (
    SELECT json_agg(json_build_object(
      'id', ed.id,
      'error_code', ed.error_code,
      'details', ed.details
    ) ORDER BY ed.created_at, ed.id)
    FROM credit_note_error_details AS ed
    WHERE ed.organization_id = cn.organization_id AND ed.credit_note_id = cn.id
  ) AS error_details,
  cn.created_at,
  cn.updated_at,
  cn.refunded_at,
  cn.voided_at
FROM credit_notes AS cn
WHERE cn.status <> 'deleted';

-- Applied taxes of credit notes not deleted, stamped with their credit
-- note's times.
CREATE VIEW exports_credit_note_applied_taxes WITH (security_invoker = true) AS
SELECT
  t.organization_id,
  t.id,
  t.credit_note_id,
  t.tax_id,
  t.tax_code,
  t.tax_name,
  t.tax_description,
  t.tax_rate,
  t.base_amount,
  t.amount,
  t.precise_amount,
  cn.currency,
  cn.created_at,
  cn.updated_at
FROM credit_note_applied_taxes AS t
JOIN credit_notes AS cn ON cn.organization_id = t.organization_id AND cn.id = t.credit_note_id
WHERE cn.status <> 'deleted';

CREATE VIEW exports_wallets WITH (security_invoker = true) AS
SELECT
  w.organization_id,
  w.id,
  w.customer_id,
  w.billing_entity_id,
  w.code,
  w.name,
  w.status,
  w.currency,
  w.rate_amount,
  w.priority,
  w.expiration_at,
  w.balance_amount,
  w.credits_balance,
  w.ongoing_balance_amount,
  w.credits_ongoing_balance,
  w.ongoing_usage_balance_amount,
  w.credits_ongoing_usage_balance,
  w.consumed_amount,
  w.consumed_credits,
  w.paid_top_up_min_amount,
  w.paid_top_up_max_amount,
  w.invoice_requires_successful_payment,
  w.allowed_fee_types,
  w.traceable,
  w.purchase_order_number,
  w.metadata,
  w.last_balance_sync_at,
  w.last_consumed_credit_at,
  w.created_at,
  w.updated_at,
  w.terminated_at
FROM wallets AS w;

CREATE VIEW exports_wallet_transactions WITH (security_invoker = true) AS
SELECT
  wt.organization_id,
  wt.id,
  wt.wallet_id,
  wt.transaction_type,
  wt.transaction_status,
  wt.status,
  wt.source,
  wt.currency,
  wt.amount,
  wt.credit_amount,
  wt.remaining_amount,
  wt.priority,
  wt.name,
  wt.invoice_id,
  wt.credit_note_id,
  wt.voided_invoice_id,
  wt.billing_entity_id,
  wt.recurring_transaction_rule_id,
  wt.purchase_order_number,
  wt.invoice_requires_successful_payment,
  wt.metadata,
  wt.settled_at,
  wt.failed_at,
  wt.created_at,
  wt.updated_at
FROM wallet_transactions AS wt;

CREATE VIEW exports_wallet_transaction_consumptions WITH (security_invoker = true) AS
SELECT
  c.organization_id,
  c.id,
  c.inbound_transaction_id,
  c.outbound_transaction_id,
  c.consumed_amount,
  c.created_at,
  c.updated_at
FROM wallet_transaction_consumptions AS c;

-- Payment requests with their payments and invoices.
CREATE VIEW exports_payment_requests WITH (security_invoker = true) AS
SELECT
  pr.organization_id,
  pr.id,
  pr.customer_id,
  pr.amount,
  pr.currency,
  pr.email,
  pr.payment_status,
  pr.payment_attempts,
  pr.ready_for_payment_processing,
  to_json(ARRAY(
    SELECT p.id FROM payments AS p
    WHERE p.organization_id = pr.organization_id AND p.payment_request_id = pr.id
    ORDER BY p.created_at, p.id
  )) AS payment_ids,
  to_json(ARRAY(
    SELECT pri.invoice_id FROM payment_request_invoices AS pri
    WHERE pri.organization_id = pr.organization_id AND pri.payment_request_id = pr.id
    ORDER BY pri.position
  )) AS invoice_ids,
  pr.created_at,
  pr.updated_at
FROM payment_requests AS pr;

-- Payments with the invoices they pay: their invoice, or the invoices of
-- their payment request.
CREATE VIEW exports_payments WITH (security_invoker = true) AS
SELECT
  p.organization_id,
  p.id,
  p.customer_id,
  p.payable_type,
  p.payment_request_id,
  p.payment_kind,
  p.status,
  p.amount,
  p.currency,
  p.reference,
  p.provider_payment_id,
  p.paid_at,
  CASE p.payable_type
    WHEN 'invoice' THEN to_json(ARRAY[p.invoice_id])
    ELSE to_json(ARRAY(
      SELECT pri.invoice_id FROM payment_request_invoices AS pri
      WHERE pri.organization_id = p.organization_id AND pri.payment_request_id = p.payment_request_id
      ORDER BY pri.position
    ))
  END AS invoice_ids,
  p.created_at,
  p.updated_at
FROM payments AS p;

CREATE VIEW exports_daily_usages WITH (security_invoker = true) AS
SELECT
  du.organization_id,
  du.id,
  du.customer_id,
  du.subscription_id,
  du.external_subscription_id,
  du.usage_date,
  du.from_datetime,
  du.to_datetime,
  du.refreshed_at,
  du.usage,
  du.usage_diff,
  du.created_at,
  du.updated_at
FROM daily_usages AS du;

-- Alert thresholds, stamped with their alert's times.
CREATE VIEW exports_alert_thresholds WITH (security_invoker = true) AS
SELECT
  th.organization_id,
  th.id,
  th.alert_id,
  th.code,
  th.value,
  th.recurring,
  a.created_at,
  a.updated_at
FROM alert_thresholds AS th
JOIN alerts AS a ON a.organization_id = th.organization_id AND a.id = th.alert_id;

CREATE VIEW exports_triggered_alerts WITH (security_invoker = true) AS
SELECT
  ta.organization_id,
  ta.id,
  ta.alert_id,
  ta.subscription_id,
  ta.wallet_id,
  ta.current_value,
  ta.previous_value,
  ta.crossed_thresholds,
  ta.triggered_at,
  ta.created_at,
  ta.updated_at
FROM triggered_alerts AS ta;

-- The deletion records (parity §1.49) of the rows of the tracked tables:
-- table_name is the table, record_id the deleted row.
CREATE VIEW exports_record_deletions WITH (security_invoker = true) AS
SELECT
  rd.organization_id,
  rd.id,
  rd.record_table AS table_name,
  rd.record_id,
  rd.deleted_at,
  rd.created_at,
  rd.updated_at
FROM record_deletions AS rd;
