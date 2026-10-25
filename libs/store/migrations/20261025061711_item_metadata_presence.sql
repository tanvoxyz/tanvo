-- Drop view "exports_wallets"
DROP VIEW exports_wallets;
-- Drop view "exports_plans"
DROP VIEW exports_plans;
-- Drop view "exports_fees"
DROP VIEW exports_fees;
-- Drop view "exports_credit_notes"
DROP VIEW exports_credit_notes;
-- Drop view "exports_credit_note_applied_taxes"
DROP VIEW exports_credit_note_applied_taxes;
-- Modify "credit_notes" table
ALTER TABLE "credit_notes" ALTER COLUMN "metadata" DROP NOT NULL;
-- Modify "plans" table
ALTER TABLE "plans" ALTER COLUMN "metadata" DROP NOT NULL, ALTER COLUMN "metadata" DROP DEFAULT;
-- Modify "wallets" table
ALTER TABLE "wallets" ALTER COLUMN "metadata" DROP NOT NULL, ALTER COLUMN "metadata" DROP DEFAULT;
-- Before this migration an empty list was no metadata: it becomes NULL, so
-- that an empty list now means an emptied metadata item.
UPDATE "credit_notes" SET "metadata" = NULL WHERE "metadata" = '[]';
UPDATE "plans" SET "metadata" = NULL WHERE "metadata" = '[]';
UPDATE "wallets" SET "metadata" = NULL WHERE "metadata" = '[]';
-- Create view "exports_credit_note_applied_taxes"
CREATE VIEW exports_credit_note_applied_taxes WITH (security_invoker=true) AS
SELECT t.organization_id,
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
   FROM (credit_note_applied_taxes t
     JOIN credit_notes cn ON (((cn.organization_id = t.organization_id) AND (cn.id = t.credit_note_id))))
  WHERE (cn.status <> 'deleted'::text);
-- Create view "exports_credit_notes"
CREATE VIEW exports_credit_notes WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    invoice_id,
    customer_id,
    sequential_id,
    number,
    issuing_date,
    status,
    credit_status,
    refund_status,
    reason,
    description,
    currency,
    total_amount,
    taxes_amount,
    taxes_rate,
    sub_total_excluding_taxes_amount,
    balance_amount,
    credit_amount,
    refund_amount,
    offset_amount,
    coupons_adjustment_amount,
    ( SELECT json_agg(json_build_object('id', it.id, 'fee_id', it.fee_id, 'amount', it.amount, 'precise_amount', it.precise_amount) ORDER BY it."position") AS json_agg
           FROM credit_note_items it
          WHERE ((it.organization_id = cn.organization_id) AND (it.credit_note_id = cn.id))) AS items,
    metadata,
    ( SELECT json_agg(json_build_object('id', ed.id, 'error_code', ed.error_code, 'details', ed.details) ORDER BY ed.created_at, ed.id) AS json_agg
           FROM credit_note_error_details ed
          WHERE ((ed.organization_id = cn.organization_id) AND (ed.credit_note_id = cn.id))) AS error_details,
    created_at,
    updated_at,
    refunded_at,
    voided_at
   FROM credit_notes cn
  WHERE (status <> 'deleted'::text);
-- Create view "exports_fees"
CREATE VIEW exports_fees WITH (security_invoker=true) AS
SELECT f.organization_id,
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
    json_build_object('type', f.fee_type, 'item_type',
        CASE f.fee_type
            WHEN 'charge'::text THEN 'billable_metric'::text
            WHEN 'add_on'::text THEN 'add_on'::text
            WHEN 'fixed_charge'::text THEN 'add_on'::text
            WHEN 'credit'::text THEN 'wallet_transaction'::text
            WHEN 'product'::text THEN 'product'::text
            ELSE 'subscription'::text
        END, 'item_id',
        CASE f.fee_type
            WHEN 'charge'::text THEN bm.id
            WHEN 'add_on'::text THEN ao.id
            WHEN 'fixed_charge'::text THEN fao.id
            WHEN 'credit'::text THEN f.wallet_transaction_id
            WHEN 'product'::text THEN pr.id
            ELSE f.subscription_id
        END, 'code',
        CASE f.fee_type
            WHEN 'charge'::text THEN bm.code
            WHEN 'add_on'::text THEN ao.code
            WHEN 'fixed_charge'::text THEN fao.code
            WHEN 'credit'::text THEN 'credit'::text
            WHEN 'product'::text THEN pr.code
            ELSE p.code
        END, 'name',
        CASE f.fee_type
            WHEN 'charge'::text THEN bm.name
            WHEN 'add_on'::text THEN ao.name
            WHEN 'fixed_charge'::text THEN fao.name
            WHEN 'credit'::text THEN 'credit'::text
            WHEN 'product'::text THEN pr.name
            ELSE p.name
        END, 'description',
        CASE f.fee_type
            WHEN 'charge'::text THEN bm.description
            WHEN 'add_on'::text THEN ao.description
            WHEN 'fixed_charge'::text THEN fao.description
            WHEN 'credit'::text THEN 'credit'::text
            WHEN 'product'::text THEN pr.description
            ELSE p.description
        END, 'invoice_display_name', COALESCE(f.invoice_display_name,
        CASE f.fee_type
            WHEN 'charge'::text THEN COALESCE(ch.invoice_display_name, bm.name)
            WHEN 'add_on'::text THEN COALESCE(ao.invoice_display_name, ao.name)
            WHEN 'fixed_charge'::text THEN COALESCE(fc.invoice_display_name, fao.invoice_display_name, fao.name)
            WHEN 'credit'::text THEN 'credit'::text
            WHEN 'product'::text THEN COALESCE(pr.invoice_display_name, pr.name)
            ELSE p.invoice_display_name
        END), 'filters', ( SELECT json_agg(json_build_object('id', cf.id, 'charge_id', cf.charge_id, 'invoice_display_name', cf.invoice_display_name, 'properties', cf.properties, 'values', cf.filter_values) ORDER BY cf."position") AS json_agg
           FROM charge_filters cf
          WHERE ((cf.organization_id = f.organization_id) AND (cf.charge_id = f.charge_id))), 'grouped_by', f.grouped_by) AS item,
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
    (f.amount + f.taxes_amount) AS total_amount,
    (f.precise_amount + f.taxes_precise_amount) AS precise_total_amount,
    f.amount_details,
    f.applied_taxes,
    f.payment_status,
        CASE f.fee_type
            WHEN 'charge'::text THEN NULLIF(f.charges_from_datetime, '0001-01-01 00:00:00+00'::timestamp with time zone)
            WHEN 'fixed_charge'::text THEN NULLIF(f.fixed_charges_from_datetime, '0001-01-01 00:00:00+00'::timestamp with time zone)
            ELSE NULLIF(f.from_datetime, '0001-01-01 00:00:00+00'::timestamp with time zone)
        END AS from_datetime,
        CASE f.fee_type
            WHEN 'charge'::text THEN NULLIF(f.charges_to_datetime, '0001-01-01 00:00:00+00'::timestamp with time zone)
            WHEN 'fixed_charge'::text THEN NULLIF(f.fixed_charges_to_datetime, '0001-01-01 00:00:00+00'::timestamp with time zone)
            ELSE NULLIF(f.to_datetime, '0001-01-01 00:00:00+00'::timestamp with time zone)
        END AS to_datetime,
    f.created_at,
    f.updated_at,
    f.succeeded_at,
    f.failed_at,
    f.refunded_at,
    f.deleted_at
   FROM (((((((((fees f
     LEFT JOIN invoices i ON (((i.organization_id = f.organization_id) AND (i.id = f.invoice_id))))
     LEFT JOIN subscriptions s ON (((s.organization_id = f.organization_id) AND (s.id = f.subscription_id))))
     LEFT JOIN plans p ON (((p.organization_id = s.organization_id) AND (p.id = s.plan_id))))
     LEFT JOIN charges ch ON (((ch.organization_id = f.organization_id) AND (ch.id = f.charge_id))))
     LEFT JOIN billable_metrics bm ON (((bm.organization_id = ch.organization_id) AND (bm.id = ch.billable_metric_id))))
     LEFT JOIN add_ons ao ON (((ao.organization_id = f.organization_id) AND (ao.id = f.add_on_id))))
     LEFT JOIN fixed_charges fc ON (((fc.organization_id = f.organization_id) AND (fc.id = f.fixed_charge_id))))
     LEFT JOIN add_ons fao ON (((fao.organization_id = fc.organization_id) AND (fao.id = fc.add_on_id))))
     LEFT JOIN products pr ON (((pr.organization_id = f.organization_id) AND (pr.id = f.product_id))))
  WHERE (i.status IS DISTINCT FROM 'deleted'::text);
-- Create view "exports_plans"
CREATE VIEW exports_plans WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    code,
    name,
    invoice_display_name,
    description,
    billing_interval,
    amount,
    currency,
    trial_period_days,
    pay_in_advance,
    bill_charges_monthly,
    bill_fixed_charges_monthly,
    parent_id,
    status,
    metadata,
    to_json(ARRAY( SELECT pt.tax_id
           FROM plan_taxes pt
          WHERE ((pt.organization_id = p.organization_id) AND (pt.plan_id = p.id))
          ORDER BY pt."position")) AS tax_ids,
    created_at,
    updated_at
   FROM plans p
  WHERE (status <> 'deleted'::text);
-- Create view "exports_wallets"
CREATE VIEW exports_wallets WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    customer_id,
    billing_entity_id,
    code,
    name,
    status,
    currency,
    rate_amount,
    priority,
    expiration_at,
    balance_amount,
    credits_balance,
    ongoing_balance_amount,
    credits_ongoing_balance,
    ongoing_usage_balance_amount,
    credits_ongoing_usage_balance,
    consumed_amount,
    consumed_credits,
    paid_top_up_min_amount,
    paid_top_up_max_amount,
    invoice_requires_successful_payment,
    allowed_fee_types,
    traceable,
    purchase_order_number,
    metadata,
    last_balance_sync_at,
    last_consumed_credit_at,
    created_at,
    updated_at,
    terminated_at
   FROM wallets w;
