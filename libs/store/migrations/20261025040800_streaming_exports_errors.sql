-- Modify "recurring_transaction_rules" table
ALTER TABLE "recurring_transaction_rules" ADD COLUMN "skip_invoice_custom_sections" boolean NOT NULL DEFAULT false, ADD COLUMN "invoice_custom_section_ids" uuid[] NOT NULL DEFAULT '{}';
-- Modify "wallets" table
ALTER TABLE "wallets" ADD CONSTRAINT "wallets_metadata_check" CHECK (jsonb_typeof(metadata) = 'array'::text), ADD COLUMN "metadata" jsonb NOT NULL DEFAULT '[]', ADD COLUMN "skip_invoice_custom_sections" boolean NOT NULL DEFAULT false, ADD COLUMN "invoice_custom_section_ids" uuid[] NOT NULL DEFAULT '{}';
-- Create "customer_error_details" table
CREATE TABLE "customer_error_details" ("organization_id" uuid NOT NULL, "id" uuid NOT NULL, "customer_id" uuid NOT NULL, "error_code" text NOT NULL, "details" jsonb NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "customer_error_details_customer_fkey" FOREIGN KEY ("organization_id", "customer_id") REFERENCES "customers" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "customer_error_details_organization_fkey" FOREIGN KEY ("organization_id") REFERENCES "organizations" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "customer_error_details_error_code_check" CHECK (error_code = 'tax_error'::text));
-- Create index "customer_error_details_customer_idx" to table: "customer_error_details"
CREATE INDEX "customer_error_details_customer_idx" ON "customer_error_details" ("organization_id", "customer_id", "error_code");
-- Create "streaming_delivery_locks" table
CREATE TABLE "streaming_delivery_locks" ("organization_id" uuid NOT NULL, "customer_id" uuid NOT NULL, "event_type" text NOT NULL, "enqueued_until" timestamptz NULL, "running_until" timestamptz NULL, PRIMARY KEY ("organization_id", "customer_id", "event_type"), CONSTRAINT "streaming_delivery_locks_customer_fkey" FOREIGN KEY ("organization_id", "customer_id") REFERENCES "customers" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE CASCADE);
-- Create "streaming_destinations" table
CREATE TABLE "streaming_destinations" ("organization_id" uuid NOT NULL, "id" uuid NOT NULL, "type" text NOT NULL, "active" boolean NOT NULL DEFAULT false, "event_types" text[] NOT NULL, "customer_full_usage_excluded_plan_codes" text[] NOT NULL DEFAULT '{}', "stream_arn" text NOT NULL DEFAULT '', "region" text NOT NULL DEFAULT '', "role_arn" text NOT NULL DEFAULT '', "external_id" text NOT NULL DEFAULT '', "partition_key" text NOT NULL DEFAULT 'customer_external_id', "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "streaming_destinations_organization_fkey" FOREIGN KEY ("organization_id") REFERENCES "organizations" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "streaming_destinations_event_types_check" CHECK ((cardinality(event_types) > 0) AND (event_types <@ ARRAY['customer_usage.refreshed.v1'::text, 'customer_full_usage.refreshed.v1'::text])), CONSTRAINT "streaming_destinations_kinesis_check" CHECK ((type <> 'kinesis'::text) OR ((stream_arn <> ''::text) AND (region <> ''::text) AND (role_arn <> ''::text))), CONSTRAINT "streaming_destinations_partition_key_check" CHECK (partition_key = 'customer_external_id'::text), CONSTRAINT "streaming_destinations_type_check" CHECK (type = 'kinesis'::text));
-- Create index "streaming_destinations_customer_full_usage_key" to table: "streaming_destinations"
CREATE UNIQUE INDEX "streaming_destinations_customer_full_usage_key" ON "streaming_destinations" ("organization_id") WHERE ('customer_full_usage.refreshed.v1'::text = ANY (event_types));
-- Create index "streaming_destinations_customer_usage_key" to table: "streaming_destinations"
CREATE UNIQUE INDEX "streaming_destinations_customer_usage_key" ON "streaming_destinations" ("organization_id") WHERE ('customer_usage.refreshed.v1'::text = ANY (event_types));
-- Modify "wallet_transactions" table
ALTER TABLE "wallet_transactions" ADD COLUMN "voided_invoice_id" uuid NULL, ADD COLUMN "billing_entity_id" uuid NULL, ADD COLUMN "skip_invoice_custom_sections" boolean NOT NULL DEFAULT false, ADD COLUMN "invoice_custom_section_ids" uuid[] NOT NULL DEFAULT '{}', ADD CONSTRAINT "wallet_transactions_billing_entity_fkey" FOREIGN KEY ("organization_id", "billing_entity_id") REFERENCES "billing_entities" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE NO ACTION, ADD CONSTRAINT "wallet_transactions_voided_invoice_fkey" FOREIGN KEY ("organization_id", "voided_invoice_id") REFERENCES "invoices" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE NO ACTION;
-- Create index "wallet_transactions_billing_entity_idx" to table: "wallet_transactions"
CREATE INDEX "wallet_transactions_billing_entity_idx" ON "wallet_transactions" ("organization_id", "billing_entity_id") WHERE (billing_entity_id IS NOT NULL);
-- Create index "wallet_transactions_voided_invoice_idx" to table: "wallet_transactions"
CREATE INDEX "wallet_transactions_voided_invoice_idx" ON "wallet_transactions" ("organization_id", "voided_invoice_id") WHERE (voided_invoice_id IS NOT NULL);
-- Enable row-level security on table "customer_error_details"
ALTER TABLE customer_error_details ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "streaming_delivery_locks"
ALTER TABLE streaming_delivery_locks ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "streaming_destinations"
ALTER TABLE streaming_destinations ENABLE ROW LEVEL SECURITY;
-- Create policy "tenant_isolation" on table "customer_error_details"
CREATE POLICY tenant_isolation ON customer_error_details AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "streaming_delivery_locks"
CREATE POLICY tenant_isolation ON streaming_delivery_locks AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "streaming_destinations"
CREATE POLICY tenant_isolation ON streaming_destinations AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create view "exports_alert_thresholds"
CREATE VIEW exports_alert_thresholds WITH (security_invoker=true) AS
SELECT th.organization_id,
    th.id,
    th.alert_id,
    th.code,
    th.value,
    th.recurring,
    a.created_at,
    a.updated_at
   FROM (alert_thresholds th
     JOIN alerts a ON (((a.organization_id = th.organization_id) AND (a.id = th.alert_id))));
-- Create view "exports_applied_coupons"
CREATE VIEW exports_applied_coupons WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    coupon_id,
    customer_id,
    status,
    amount,
    remaining_amount,
    amount_currency,
    percentage_rate,
    frequency,
    frequency_duration,
    frequency_duration_remaining,
    ( SELECT json_agg(json_build_object('id', cr.id, 'invoice_id', cr.invoice_id, 'amount', cr.amount, 'currency', cr.currency, 'before_taxes', cr.before_taxes) ORDER BY cr.created_at, cr.id) AS json_agg
           FROM invoice_credits cr
          WHERE ((cr.organization_id = ac.organization_id) AND (cr.applied_coupon_id = ac.id))) AS credits,
    created_at,
    updated_at,
    terminated_at
   FROM applied_coupons ac;
-- Create view "exports_billable_metrics"
CREATE VIEW exports_billable_metrics WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    code,
    name,
    description,
    aggregation_type,
    field_name,
    weighted_interval,
    recurring,
    expression,
    rounding_function,
    rounding_precision,
    COALESCE(( SELECT json_agg(json_build_object('key', f.key, 'values', f.filter_values) ORDER BY f."position") AS json_agg
           FROM billable_metric_filters f
          WHERE ((f.organization_id = bm.organization_id) AND (f.billable_metric_id = bm.id) AND (f.deleted_at IS NULL))), '[]'::json) AS filters,
    created_at,
    updated_at
   FROM billable_metrics bm
  WHERE (deleted_at IS NULL);
-- Create view "exports_billing_entities"
CREATE VIEW exports_billing_entities WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    code,
    name,
    legal_name,
    legal_number,
    tax_id,
    email,
    address_line1,
    address_line2,
    address_city,
    address_state,
    address_zipcode,
    address_country,
    timezone,
    default_currency,
    is_default,
    created_at,
    updated_at,
    archived_at
   FROM billing_entities be;
-- Create view "exports_charges"
CREATE VIEW exports_charges WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    plan_id,
    parent_id,
    billable_metric_id,
    code,
    invoice_display_name,
    charge_model,
    properties,
    pay_in_advance,
    invoiceable,
    regroup_paid_fees,
    prorated,
    min_amount,
    applied_pricing_unit_id,
    conversion_rate,
    ( SELECT json_agg(json_build_object('id', cf.id, 'code', cf.code, 'invoice_display_name', cf.invoice_display_name, 'properties', cf.properties, 'values', cf.filter_values, 'deleted_at', cf.deleted_at) ORDER BY cf."position") AS json_agg
           FROM charge_filters cf
          WHERE ((cf.organization_id = c.organization_id) AND (cf.charge_id = c.id))) AS filters,
    created_at,
    updated_at,
    deleted_at
   FROM charges c;
-- Create view "exports_coupons"
CREATE VIEW exports_coupons WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    code,
    name,
    description,
    coupon_type,
    amount,
    amount_currency,
    percentage_rate,
    frequency,
    frequency_duration,
    reusable,
    (EXISTS ( SELECT 1
           FROM coupon_plans t
          WHERE ((t.organization_id = cp.organization_id) AND (t.coupon_id = cp.id)))) AS limited_plans,
    (EXISTS ( SELECT 1
           FROM coupon_billable_metrics t
          WHERE ((t.organization_id = cp.organization_id) AND (t.coupon_id = cp.id)))) AS limited_billable_metrics,
    to_json(ARRAY( SELECT t.plan_id
           FROM coupon_plans t
          WHERE ((t.organization_id = cp.organization_id) AND (t.coupon_id = cp.id))
          ORDER BY t."position")) AS plan_ids,
    to_json(ARRAY( SELECT t.billable_metric_id
           FROM coupon_billable_metrics t
          WHERE ((t.organization_id = cp.organization_id) AND (t.coupon_id = cp.id))
          ORDER BY t."position")) AS billable_metric_ids,
    expiration,
    expiration_at,
    status,
    created_at,
    updated_at,
    terminated_at,
    deleted_at
   FROM coupons cp;
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
-- Create view "exports_customers"
CREATE VIEW exports_customers WITH (security_invoker=true) AS
SELECT c.organization_id,
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
    COALESCE(c.timezone, be.timezone, 'UTC'::text) AS applicable_timezone,
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
    to_json(ARRAY( SELECT ct.tax_id
           FROM customer_taxes ct
          WHERE ((ct.organization_id = c.organization_id) AND (ct.customer_id = c.id))
          ORDER BY ct."position")) AS tax_ids,
    c.created_at,
    c.updated_at,
    c.deleted_at
   FROM (((customers c
     JOIN billing_entities be ON (((be.organization_id = c.organization_id) AND (be.id = c.billing_entity_id))))
     LEFT JOIN payment_provider_customers ppc ON (((ppc.organization_id = c.organization_id) AND (ppc.customer_id = c.id) AND ppc.is_default AND (ppc.deleted_at IS NULL))))
     LEFT JOIN integrations i ON (((i.organization_id = ppc.organization_id) AND (i.id = ppc.integration_id))));
-- Create view "exports_daily_usages"
CREATE VIEW exports_daily_usages WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    customer_id,
    subscription_id,
    external_subscription_id,
    usage_date,
    from_datetime,
    to_datetime,
    refreshed_at,
    usage,
    usage_diff,
    created_at,
    updated_at
   FROM daily_usages du;
-- Create view "exports_entitlements"
CREATE VIEW exports_entitlements WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    feature_id,
    plan_id,
    subscription_id,
    privilege_values,
    created_at,
    updated_at,
    deleted_at
   FROM entitlements e;
-- Create view "exports_features"
CREATE VIEW exports_features WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    code,
    name,
    description,
    COALESCE(( SELECT json_agg(json_build_object('id', fp.id, 'code', fp.code, 'name', fp.name, 'value_type', fp.value_type, 'select_options', fp.select_options, 'deleted_at', fp.deleted_at) ORDER BY fp."position") AS json_agg
           FROM feature_privileges fp
          WHERE ((fp.organization_id = f.organization_id) AND (fp.feature_id = f.id))), '[]'::json) AS privileges,
    created_at,
    updated_at,
    deleted_at
   FROM features f;
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
-- Create view "exports_integration_customers"
CREATE VIEW exports_integration_customers WITH (security_invoker=true) AS
SELECT ic.organization_id,
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
   FROM (integration_customers ic
     JOIN integrations i ON (((i.organization_id = ic.organization_id) AND (i.id = ic.integration_id))));
-- Create view "exports_invoice_applied_taxes"
CREATE VIEW exports_invoice_applied_taxes WITH (security_invoker=true) AS
SELECT it.organization_id,
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
   FROM (invoice_applied_taxes it
     JOIN invoices i ON (((i.organization_id = it.organization_id) AND (i.id = it.invoice_id))))
  WHERE (i.status <> 'deleted'::text);
-- Create view "exports_invoice_settlements"
CREATE VIEW exports_invoice_settlements WITH (security_invoker=true) AS
SELECT st.organization_id,
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
   FROM (invoice_settlements st
     JOIN invoices i ON (((i.organization_id = st.organization_id) AND (i.id = st.invoice_id))));
-- Create view "exports_invoice_subscriptions"
CREATE VIEW exports_invoice_subscriptions WITH (security_invoker=true) AS
SELECT ins.organization_id,
    ins.id,
    ins.invoice_id,
    ins.regenerated_invoice_id,
    ins.subscription_id,
    ins.invoicing_reason,
    ins.recurring,
    ins.from_datetime,
    ins.to_datetime,
    NULLIF(ins.charges_from_datetime, '0001-01-01 00:00:00+00'::timestamp with time zone) AS charges_from_datetime,
    NULLIF(ins.charges_to_datetime, '0001-01-01 00:00:00+00'::timestamp with time zone) AS charges_to_datetime,
    NULLIF(ins.fixed_charges_from_datetime, '0001-01-01 00:00:00+00'::timestamp with time zone) AS fixed_charges_from_datetime,
    NULLIF(ins.fixed_charges_to_datetime, '0001-01-01 00:00:00+00'::timestamp with time zone) AS fixed_charges_to_datetime,
    ins.billing_timestamp,
    ins.created_at,
    ins.created_at AS updated_at
   FROM (invoice_subscriptions ins
     JOIN invoices i ON (((i.organization_id = ins.organization_id) AND (i.id = ins.invoice_id))))
  WHERE (i.status <> 'deleted'::text);
-- Create view "exports_invoices"
CREATE VIEW exports_invoices WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    customer_id,
    billing_entity_id,
    sequential_id,
    billing_entity_sequential_id,
    number,
    invoice_type,
    status,
    payment_status,
    tax_status,
    issuing_date,
    payment_due_date,
    net_payment_term_days,
    payment_overdue,
    payment_dispute_lost_at,
    currency,
    fees_amount,
    taxes_amount,
    progressive_billing_credit_amount,
    coupons_amount,
    credit_notes_amount,
    sub_total_excluding_taxes_amount,
    sub_total_including_taxes_amount,
    total_amount,
    total_paid_amount,
    (total_amount - total_paid_amount) AS total_due_amount,
    prepaid_credit_amount,
    prepaid_granted_credit_amount,
    prepaid_purchased_credit_amount,
    voided_invoice_id,
    purchase_order_number,
    metadata,
    ( SELECT json_agg(json_build_object('id', ed.id, 'error_code', ed.error_code, 'details', ed.details) ORDER BY ed.created_at, ed.id) AS json_agg
           FROM invoice_error_details ed
          WHERE ((ed.organization_id = i.organization_id) AND (ed.invoice_id = i.id))) AS error_details,
    created_at,
    updated_at,
    finalized_at,
    voided_at
   FROM invoices i
  WHERE ((status = ANY (ARRAY['draft'::text, 'finalized'::text, 'voided'::text, 'failed'::text, 'deleted'::text])) OR ((status = 'open'::text) AND (tax_status = 'pending'::text)));
-- Create view "exports_payment_requests"
CREATE VIEW exports_payment_requests WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    customer_id,
    amount,
    currency,
    email,
    payment_status,
    payment_attempts,
    ready_for_payment_processing,
    to_json(ARRAY( SELECT p.id
           FROM payments p
          WHERE ((p.organization_id = pr.organization_id) AND (p.payment_request_id = pr.id))
          ORDER BY p.created_at, p.id)) AS payment_ids,
    to_json(ARRAY( SELECT pri.invoice_id
           FROM payment_request_invoices pri
          WHERE ((pri.organization_id = pr.organization_id) AND (pri.payment_request_id = pr.id))
          ORDER BY pri."position")) AS invoice_ids,
    created_at,
    updated_at
   FROM payment_requests pr;
-- Create view "exports_payments"
CREATE VIEW exports_payments WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    customer_id,
    payable_type,
    payment_request_id,
    payment_kind,
    status,
    amount,
    currency,
    reference,
    provider_payment_id,
    paid_at,
        CASE payable_type
            WHEN 'invoice'::text THEN to_json(ARRAY[invoice_id])
            ELSE to_json(ARRAY( SELECT pri.invoice_id
               FROM payment_request_invoices pri
              WHERE ((pri.organization_id = p.organization_id) AND (pri.payment_request_id = p.payment_request_id))
              ORDER BY pri."position"))
        END AS invoice_ids,
    created_at,
    updated_at
   FROM payments p;
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
-- Create view "exports_record_deletions"
CREATE VIEW exports_record_deletions WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    record_table AS table_name,
    record_id,
    deleted_at,
    created_at,
    updated_at
   FROM record_deletions rd;
-- Create view "exports_subscriptions"
CREATE VIEW exports_subscriptions WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    external_id,
    customer_id,
    plan_id,
    billing_entity_id,
    name,
    status,
    billing_time,
    subscription_at,
    started_at,
    trial_ended_at,
    ending_at,
    terminated_at,
    canceled_at,
    cancellation_reason,
    purchase_order_number,
    previous_subscription_id,
    to_json(ARRAY( SELECT ns.id
           FROM subscriptions ns
          WHERE ((ns.organization_id = s.organization_id) AND (ns.previous_subscription_id = s.id))
          ORDER BY ns.created_at, ns.id)) AS next_subscription_ids,
    created_at,
    updated_at
   FROM subscriptions s;
-- Create view "exports_taxes"
CREATE VIEW exports_taxes WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    code,
    name,
    rate,
    description,
    to_json(ARRAY( SELECT bt.billing_entity_id
           FROM billing_entity_taxes bt
          WHERE ((bt.organization_id = tx.organization_id) AND (bt.tax_id = tx.id))
          ORDER BY bt.billing_entity_id)) AS billing_entity_ids,
    created_at,
    updated_at,
    deleted_at
   FROM taxes tx;
-- Create view "exports_triggered_alerts"
CREATE VIEW exports_triggered_alerts WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    alert_id,
    subscription_id,
    wallet_id,
    current_value,
    previous_value,
    crossed_thresholds,
    triggered_at,
    created_at,
    updated_at
   FROM triggered_alerts ta;
-- Create view "exports_usage_thresholds"
CREATE VIEW exports_usage_thresholds WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    plan_id,
    subscription_id,
    amount,
    recurring,
    threshold_display_name,
    created_at,
    updated_at,
    deleted_at
   FROM usage_thresholds ut;
-- Create view "exports_wallet_transaction_consumptions"
CREATE VIEW exports_wallet_transaction_consumptions WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    inbound_transaction_id,
    outbound_transaction_id,
    consumed_amount,
    created_at,
    updated_at
   FROM wallet_transaction_consumptions c;
-- Create view "exports_wallet_transactions"
CREATE VIEW exports_wallet_transactions WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    wallet_id,
    transaction_type,
    transaction_status,
    status,
    source,
    currency,
    amount,
    credit_amount,
    remaining_amount,
    priority,
    name,
    invoice_id,
    credit_note_id,
    voided_invoice_id,
    billing_entity_id,
    recurring_transaction_rule_id,
    purchase_order_number,
    invoice_requires_successful_payment,
    metadata,
    settled_at,
    failed_at,
    created_at,
    updated_at
   FROM wallet_transactions wt;
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
