-- Modify "add_ons" table
ALTER TABLE "add_ons" DROP CONSTRAINT "add_ons_code_key", ADD COLUMN "deleted_at" timestamptz NULL;
-- Create index "add_ons_code_key" to table: "add_ons"
CREATE UNIQUE INDEX "add_ons_code_key" ON "add_ons" ("organization_id", "code") WHERE (deleted_at IS NULL);
-- Create index "events_created_at_idx" to table: "events"
CREATE INDEX "events_created_at_idx" ON "events" ("organization_id", "created_at");
-- Create index "events_timestamp_idx" to table: "events"
CREATE INDEX "events_timestamp_idx" ON "events" ("organization_id", "timestamp" DESC, "id" DESC);
-- Modify "billable_metric_filters" table
ALTER TABLE "billable_metric_filters" DROP CONSTRAINT "billable_metric_filters_key_key", ADD COLUMN "deleted_at" timestamptz NULL;
-- Create index "billable_metric_filters_key_idx" to table: "billable_metric_filters"
CREATE UNIQUE INDEX "billable_metric_filters_key_idx" ON "billable_metric_filters" ("organization_id", "billable_metric_id", "key") WHERE (deleted_at IS NULL);
-- Drop index "usage_thresholds_plan_amount_key" from table: "usage_thresholds"
DROP INDEX "usage_thresholds_plan_amount_key";
-- Drop index "usage_thresholds_plan_idx" from table: "usage_thresholds"
DROP INDEX "usage_thresholds_plan_idx";
-- Drop index "usage_thresholds_plan_recurring_key" from table: "usage_thresholds"
DROP INDEX "usage_thresholds_plan_recurring_key";
-- Drop index "usage_thresholds_subscription_amount_key" from table: "usage_thresholds"
DROP INDEX "usage_thresholds_subscription_amount_key";
-- Drop index "usage_thresholds_subscription_idx" from table: "usage_thresholds"
DROP INDEX "usage_thresholds_subscription_idx";
-- Drop index "usage_thresholds_subscription_recurring_key" from table: "usage_thresholds"
DROP INDEX "usage_thresholds_subscription_recurring_key";
-- Modify "usage_thresholds" table
ALTER TABLE "usage_thresholds" ADD COLUMN "deleted_at" timestamptz NULL;
-- Create index "usage_thresholds_plan_amount_key" to table: "usage_thresholds"
CREATE UNIQUE INDEX "usage_thresholds_plan_amount_key" ON "usage_thresholds" ("organization_id", "plan_id", "amount") WHERE ((plan_id IS NOT NULL) AND (NOT recurring) AND (deleted_at IS NULL));
-- Create index "usage_thresholds_plan_idx" to table: "usage_thresholds"
CREATE INDEX "usage_thresholds_plan_idx" ON "usage_thresholds" ("organization_id", "plan_id") WHERE ((plan_id IS NOT NULL) AND (deleted_at IS NULL));
-- Create index "usage_thresholds_plan_recurring_key" to table: "usage_thresholds"
CREATE UNIQUE INDEX "usage_thresholds_plan_recurring_key" ON "usage_thresholds" ("organization_id", "plan_id") WHERE ((plan_id IS NOT NULL) AND recurring AND (deleted_at IS NULL));
-- Create index "usage_thresholds_subscription_amount_key" to table: "usage_thresholds"
CREATE UNIQUE INDEX "usage_thresholds_subscription_amount_key" ON "usage_thresholds" ("organization_id", "subscription_id", "amount") WHERE ((subscription_id IS NOT NULL) AND (NOT recurring) AND (deleted_at IS NULL));
-- Create index "usage_thresholds_subscription_idx" to table: "usage_thresholds"
CREATE INDEX "usage_thresholds_subscription_idx" ON "usage_thresholds" ("organization_id", "subscription_id") WHERE ((subscription_id IS NOT NULL) AND (deleted_at IS NULL));
-- Create index "usage_thresholds_subscription_recurring_key" to table: "usage_thresholds"
CREATE UNIQUE INDEX "usage_thresholds_subscription_recurring_key" ON "usage_thresholds" ("organization_id", "subscription_id") WHERE ((subscription_id IS NOT NULL) AND recurring AND (deleted_at IS NULL));
-- Drop index "charge_filters_charge_idx" from table: "charge_filters"
DROP INDEX "charge_filters_charge_idx";
-- Drop index "charge_filters_code_key" from table: "charge_filters"
DROP INDEX "charge_filters_code_key";
-- Modify "charge_filters" table
ALTER TABLE "charge_filters" ADD COLUMN "deleted_at" timestamptz NULL;
-- Create index "charge_filters_charge_idx" to table: "charge_filters"
CREATE INDEX "charge_filters_charge_idx" ON "charge_filters" ("organization_id", "charge_id", "position") WHERE (deleted_at IS NULL);
-- Create index "charge_filters_code_key" to table: "charge_filters"
CREATE UNIQUE INDEX "charge_filters_code_key" ON "charge_filters" ("organization_id", "charge_id", "code") WHERE ((code <> ''::text) AND (deleted_at IS NULL));
-- Drop index "charges_billable_metric_idx" from table: "charges"
DROP INDEX "charges_billable_metric_idx";
-- Drop index "charges_code_key" from table: "charges"
DROP INDEX "charges_code_key";
-- Drop index "charges_parent_idx" from table: "charges"
DROP INDEX "charges_parent_idx";
-- Drop index "charges_plan_idx" from table: "charges"
DROP INDEX "charges_plan_idx";
-- Modify "charges" table
ALTER TABLE "charges" ADD COLUMN "deleted_at" timestamptz NULL;
-- Create index "charges_billable_metric_idx" to table: "charges"
CREATE INDEX "charges_billable_metric_idx" ON "charges" ("organization_id", "billable_metric_id") WHERE (deleted_at IS NULL);
-- Create index "charges_code_key" to table: "charges"
CREATE UNIQUE INDEX "charges_code_key" ON "charges" ("organization_id", "plan_id", "code") WHERE ((parent_id IS NULL) AND (deleted_at IS NULL));
-- Create index "charges_parent_idx" to table: "charges"
CREATE INDEX "charges_parent_idx" ON "charges" ("organization_id", "parent_id") WHERE ((parent_id IS NOT NULL) AND (deleted_at IS NULL));
-- Create index "charges_plan_idx" to table: "charges"
CREATE INDEX "charges_plan_idx" ON "charges" ("organization_id", "plan_id") WHERE (deleted_at IS NULL);
-- Drop index "coupons_code_active_key" from table: "coupons"
DROP INDEX "coupons_code_active_key";
-- Drop index "coupons_expiration_at_idx" from table: "coupons"
DROP INDEX "coupons_expiration_at_idx";
-- Modify "coupons" table
ALTER TABLE "coupons" ADD COLUMN "deleted_at" timestamptz NULL;
-- Create index "coupons_expiration_at_idx" to table: "coupons"
CREATE INDEX "coupons_expiration_at_idx" ON "coupons" ("expiration_at") WHERE ((status = 'active'::text) AND (expiration = 'time_limit'::text) AND (deleted_at IS NULL));
-- Create index "coupons_code_kept_key" to table: "coupons"
CREATE UNIQUE INDEX "coupons_code_kept_key" ON "coupons" ("organization_id", "code") WHERE (deleted_at IS NULL);
-- Modify "invoice_custom_sections" table
ALTER TABLE "invoice_custom_sections" DROP CONSTRAINT "invoice_custom_sections_code_key", ADD COLUMN "description" text NULL, ADD COLUMN "deleted_at" timestamptz NULL;
-- Create index "invoice_custom_sections_code_key" to table: "invoice_custom_sections"
CREATE UNIQUE INDEX "invoice_custom_sections_code_key" ON "invoice_custom_sections" ("organization_id", "code") WHERE (deleted_at IS NULL);
-- Drop index "integrations_default_key" from table: "integrations"
DROP INDEX "integrations_default_key";
-- Modify "integrations" table
ALTER TABLE "integrations" DROP CONSTRAINT "integrations_code_key", ADD COLUMN "deleted_at" timestamptz NULL;
-- Create index "integrations_default_key" to table: "integrations"
CREATE UNIQUE INDEX "integrations_default_key" ON "integrations" ("organization_id", "category") WHERE (is_default AND (deleted_at IS NULL));
-- Create index "integrations_other_code_key" to table: "integrations"
CREATE UNIQUE INDEX "integrations_other_code_key" ON "integrations" ("organization_id", "code") WHERE (category <> 'payment'::text);
-- Create index "integrations_payment_code_key" to table: "integrations"
CREATE UNIQUE INDEX "integrations_payment_code_key" ON "integrations" ("organization_id", "code") WHERE ((category = 'payment'::text) AND (deleted_at IS NULL));
-- Drop index "fees_unbilled_in_advance_idx" from table: "fees"
DROP INDEX "fees_unbilled_in_advance_idx";
-- Modify "fees" table
ALTER TABLE "fees" ADD COLUMN "original_fee_id" uuid NULL, ADD COLUMN "deleted_at" timestamptz NULL, ADD CONSTRAINT "fees_original_fee_fkey" FOREIGN KEY ("organization_id", "original_fee_id") REFERENCES "fees" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- Create index "fees_unbilled_in_advance_idx" to table: "fees"
CREATE INDEX "fees_unbilled_in_advance_idx" ON "fees" ("organization_id", "created_at") WHERE ((invoice_id IS NULL) AND pay_in_advance AND invoiceable AND (payment_status = 'succeeded'::text) AND (deleted_at IS NULL));
-- Create index "fees_original_fee_idx" to table: "fees"
CREATE INDEX "fees_original_fee_idx" ON "fees" ("organization_id", "original_fee_id") WHERE (original_fee_id IS NOT NULL);
-- Drop index "alerts_billable_metric_idx" from table: "alerts"
DROP INDEX "alerts_billable_metric_idx";
-- Drop index "alerts_subscription_code_key" from table: "alerts"
DROP INDEX "alerts_subscription_code_key";
-- Drop index "alerts_wallet_alert_type_key" from table: "alerts"
DROP INDEX "alerts_wallet_alert_type_key";
-- Drop index "alerts_wallet_code_key" from table: "alerts"
DROP INDEX "alerts_wallet_code_key";
-- Modify "alerts" table
ALTER TABLE "alerts" ADD COLUMN "deleted_at" timestamptz NULL;
-- Create index "alerts_billable_metric_idx" to table: "alerts"
CREATE INDEX "alerts_billable_metric_idx" ON "alerts" ("organization_id", "billable_metric_id") WHERE ((billable_metric_id IS NOT NULL) AND (deleted_at IS NULL));
-- Create index "alerts_subscription_code_key" to table: "alerts"
CREATE UNIQUE INDEX "alerts_subscription_code_key" ON "alerts" ("organization_id", "subscription_external_id", "code") WHERE ((wallet_id IS NULL) AND (deleted_at IS NULL));
-- Create index "alerts_wallet_alert_type_key" to table: "alerts"
CREATE UNIQUE INDEX "alerts_wallet_alert_type_key" ON "alerts" ("organization_id", "wallet_id", "alert_type") WHERE ((wallet_id IS NOT NULL) AND (deleted_at IS NULL));
-- Create index "alerts_wallet_code_key" to table: "alerts"
CREATE UNIQUE INDEX "alerts_wallet_code_key" ON "alerts" ("organization_id", "wallet_id", "code") WHERE ((wallet_id IS NOT NULL) AND (deleted_at IS NULL));
-- Drop index "quotes_created_at_idx" from table: "quotes"
DROP INDEX "quotes_created_at_idx";
-- Modify "quotes" table
ALTER TABLE "quotes" ADD COLUMN "images" text[] NOT NULL DEFAULT '{}', ADD CONSTRAINT "quotes_sequential_id_key" UNIQUE ("organization_id", "sequential_id");
-- Create index "quotes_created_at_idx" to table: "quotes"
CREATE INDEX "quotes_created_at_idx" ON "quotes" ("organization_id", "created_at", "id");
-- Modify "quote_versions" table
ALTER TABLE "quote_versions" ALTER COLUMN "content" TYPE text, ADD COLUMN "billing_items" json NULL, ADD COLUMN "mention_variables" json NULL;
-- Modify "organizations" table
ALTER TABLE "organizations" ADD CONSTRAINT "organizations_document_number_prefix_check" CHECK ((char_length(document_number_prefix) >= 1) AND (char_length(document_number_prefix) <= 10)), ADD COLUMN "document_number_prefix" text NOT NULL;
-- Drop index "orders_created_at_idx" from table: "orders"
DROP INDEX "orders_created_at_idx";
-- Modify "orders" table
ALTER TABLE "orders" ADD CONSTRAINT "orders_sequential_id_key" UNIQUE ("organization_id", "sequential_id");
-- Create index "orders_created_at_idx" to table: "orders"
CREATE INDEX "orders_created_at_idx" ON "orders" ("organization_id", "created_at", "id");
-- Drop index "order_forms_created_at_idx" from table: "order_forms"
DROP INDEX "order_forms_created_at_idx";
-- Modify "order_forms" table
ALTER TABLE "order_forms" DROP COLUMN "signed_by", ADD CONSTRAINT "order_forms_sequential_id_key" UNIQUE ("organization_id", "sequential_id");
-- Create index "order_forms_created_at_idx" to table: "order_forms"
CREATE INDEX "order_forms_created_at_idx" ON "order_forms" ("organization_id", "created_at", "id");
-- Create "activity_logs" table
CREATE TABLE "activity_logs" ("organization_id" uuid NOT NULL, "id" uuid NOT NULL, "user_id" uuid NULL, "api_key_id" uuid NULL, "activity_type" text NOT NULL, "activity_source" text NOT NULL, "activity_object" jsonb NULL, "activity_object_changes" jsonb NULL, "resource_id" uuid NOT NULL, "resource_type" text NOT NULL, "external_customer_id" text NULL, "external_subscription_id" text NULL, "logged_at" timestamptz NOT NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "activity_logs_organization_fkey" FOREIGN KEY ("organization_id") REFERENCES "organizations" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "activity_logs_activity_source_check" CHECK (activity_source = ANY (ARRAY['api'::text, 'front'::text, 'system'::text])));
-- Create index "activity_logs_logged_at_idx" to table: "activity_logs"
CREATE INDEX "activity_logs_logged_at_idx" ON "activity_logs" ("organization_id", "logged_at" DESC, "id");
-- Create "api_logs" table
CREATE TABLE "api_logs" ("organization_id" uuid NOT NULL, "id" uuid NOT NULL, "api_key_id" uuid NOT NULL, "client" text NOT NULL, "http_method" text NOT NULL, "http_status" integer NOT NULL, "request_origin" text NOT NULL, "request_path" text NOT NULL, "request_body" jsonb NULL, "request_response" jsonb NULL, "api_version" text NOT NULL, "logged_at" timestamptz NOT NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "api_logs_organization_fkey" FOREIGN KEY ("organization_id") REFERENCES "organizations" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "api_logs_http_method_check" CHECK (http_method = ANY (ARRAY['get'::text, 'post'::text, 'put'::text, 'delete'::text, 'patch'::text])));
-- Create index "api_logs_logged_at_idx" to table: "api_logs"
CREATE INDEX "api_logs_logged_at_idx" ON "api_logs" ("organization_id", "logged_at" DESC, "id");
-- Create index "invoices_generating_idx" to table: "invoices"
CREATE INDEX "invoices_generating_idx" ON "invoices" ("organization_id", "created_at") WHERE (status = 'generating'::text);
-- Create "applied_invoice_custom_sections" table
CREATE TABLE "applied_invoice_custom_sections" ("organization_id" uuid NOT NULL, "id" uuid NOT NULL, "invoice_id" uuid NOT NULL, "code" text NOT NULL, "name" text NOT NULL, "display_name" text NULL, "details" text NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "applied_invoice_custom_sections_invoice_fkey" FOREIGN KEY ("organization_id", "invoice_id") REFERENCES "invoices" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "applied_invoice_custom_sections_organization_fkey" FOREIGN KEY ("organization_id") REFERENCES "organizations" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "applied_invoice_custom_sections_invoice_idx" to table: "applied_invoice_custom_sections"
CREATE INDEX "applied_invoice_custom_sections_invoice_idx" ON "applied_invoice_custom_sections" ("organization_id", "invoice_id");
-- Modify "customers" table
ALTER TABLE "customers" ADD COLUMN "awaiting_wallet_refresh" boolean NOT NULL DEFAULT false, ADD COLUMN "wallet_refresh_generation" bigint NOT NULL DEFAULT 0, ADD COLUMN "wallet_refresh_claimed" bigint NOT NULL DEFAULT 0;
-- Create index "customers_awaiting_wallet_refresh_idx" to table: "customers"
CREATE INDEX "customers_awaiting_wallet_refresh_idx" ON "customers" ("organization_id", "id") WHERE awaiting_wallet_refresh;
-- Modify "subscriptions" table
ALTER TABLE "subscriptions" ADD COLUMN "skip_daily_usage" boolean NOT NULL DEFAULT false, ADD COLUMN "last_received_event_on" date NULL;
-- Create "daily_usages" table
CREATE TABLE "daily_usages" ("organization_id" uuid NOT NULL, "id" uuid NOT NULL, "customer_id" uuid NOT NULL, "subscription_id" uuid NOT NULL, "external_subscription_id" text NOT NULL, "usage" jsonb NOT NULL, "usage_diff" jsonb NOT NULL, "from_datetime" timestamptz NOT NULL, "to_datetime" timestamptz NOT NULL, "refreshed_at" timestamptz NOT NULL, "usage_date" date NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "daily_usages_customer_fkey" FOREIGN KEY ("organization_id", "customer_id") REFERENCES "customers" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "daily_usages_organization_fkey" FOREIGN KEY ("organization_id") REFERENCES "organizations" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "daily_usages_subscription_fkey" FOREIGN KEY ("organization_id", "subscription_id") REFERENCES "subscriptions" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "daily_usages_customer_idx" to table: "daily_usages"
CREATE INDEX "daily_usages_customer_idx" ON "daily_usages" ("organization_id", "customer_id");
-- Create index "daily_usages_external_subscription_id_idx" to table: "daily_usages"
CREATE INDEX "daily_usages_external_subscription_id_idx" ON "daily_usages" ("organization_id", "external_subscription_id");
-- Create index "daily_usages_subscription_usage_date_idx" to table: "daily_usages"
CREATE INDEX "daily_usages_subscription_usage_date_idx" ON "daily_usages" ("organization_id", "subscription_id", "usage_date");
-- Create index "daily_usages_usage_date_idx" to table: "daily_usages"
CREATE INDEX "daily_usages_usage_date_idx" ON "daily_usages" ("organization_id", "usage_date");
-- Drop index "fixed_charges_add_on_idx" from table: "fixed_charges"
DROP INDEX "fixed_charges_add_on_idx";
-- Drop index "fixed_charges_code_key" from table: "fixed_charges"
DROP INDEX "fixed_charges_code_key";
-- Drop index "fixed_charges_parent_idx" from table: "fixed_charges"
DROP INDEX "fixed_charges_parent_idx";
-- Drop index "fixed_charges_plan_idx" from table: "fixed_charges"
DROP INDEX "fixed_charges_plan_idx";
-- Modify "fixed_charges" table
ALTER TABLE "fixed_charges" ADD COLUMN "deleted_at" timestamptz NULL;
-- Create index "fixed_charges_add_on_idx" to table: "fixed_charges"
CREATE INDEX "fixed_charges_add_on_idx" ON "fixed_charges" ("organization_id", "add_on_id") WHERE (deleted_at IS NULL);
-- Create index "fixed_charges_code_key" to table: "fixed_charges"
CREATE UNIQUE INDEX "fixed_charges_code_key" ON "fixed_charges" ("organization_id", "plan_id", "code") WHERE ((parent_id IS NULL) AND (deleted_at IS NULL));
-- Create index "fixed_charges_parent_idx" to table: "fixed_charges"
CREATE INDEX "fixed_charges_parent_idx" ON "fixed_charges" ("organization_id", "parent_id") WHERE ((parent_id IS NOT NULL) AND (deleted_at IS NULL));
-- Create index "fixed_charges_plan_idx" to table: "fixed_charges"
CREATE INDEX "fixed_charges_plan_idx" ON "fixed_charges" ("organization_id", "plan_id") WHERE (deleted_at IS NULL);
-- Create "fixed_charge_units_overrides" table
CREATE TABLE "fixed_charge_units_overrides" ("organization_id" uuid NOT NULL, "id" uuid NOT NULL, "subscription_id" uuid NOT NULL, "fixed_charge_id" uuid NOT NULL, "units" numeric NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "deleted_at" timestamptz NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "fixed_charge_units_overrides_fixed_charge_fkey" FOREIGN KEY ("organization_id", "fixed_charge_id") REFERENCES "fixed_charges" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "fixed_charge_units_overrides_subscription_fkey" FOREIGN KEY ("organization_id", "subscription_id") REFERENCES "subscriptions" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "fixed_charge_units_overrides_units_check" CHECK (units >= (0)::numeric));
-- Create index "fixed_charge_units_overrides_fixed_charge_idx" to table: "fixed_charge_units_overrides"
CREATE INDEX "fixed_charge_units_overrides_fixed_charge_idx" ON "fixed_charge_units_overrides" ("organization_id", "fixed_charge_id") WHERE (deleted_at IS NULL);
-- Create index "fixed_charge_units_overrides_kept_key" to table: "fixed_charge_units_overrides"
CREATE UNIQUE INDEX "fixed_charge_units_overrides_kept_key" ON "fixed_charge_units_overrides" ("organization_id", "subscription_id", "fixed_charge_id") WHERE (deleted_at IS NULL);
-- Create "idempotency_keys" table
CREATE TABLE "idempotency_keys" ("organization_id" uuid NOT NULL, "key" text NOT NULL, "procedure" text NOT NULL, "request_hash" bytea NOT NULL, "response" bytea NULL, "created_at" timestamptz NOT NULL, "expires_at" timestamptz NOT NULL, PRIMARY KEY ("organization_id", "key"), CONSTRAINT "idempotency_keys_organization_fkey" FOREIGN KEY ("organization_id") REFERENCES "organizations" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "idempotency_keys_key_check" CHECK ((octet_length(key) >= 1) AND (octet_length(key) <= 255)));
-- Create index "idempotency_keys_expires_at_idx" to table: "idempotency_keys"
CREATE INDEX "idempotency_keys_expires_at_idx" ON "idempotency_keys" ("expires_at");
-- Drop index "invoice_subscriptions_charges_key" from table: "invoice_subscriptions"
DROP INDEX "invoice_subscriptions_charges_key";
-- Drop index "invoice_subscriptions_fixed_charges_key" from table: "invoice_subscriptions"
DROP INDEX "invoice_subscriptions_fixed_charges_key";
-- Drop index "invoice_subscriptions_reason_key" from table: "invoice_subscriptions"
DROP INDEX "invoice_subscriptions_reason_key";
-- Modify "invoice_subscriptions" table
ALTER TABLE "invoice_subscriptions" ADD COLUMN "regenerated_invoice_id" uuid NULL, ADD CONSTRAINT "invoice_subscriptions_regenerated_invoice_fkey" FOREIGN KEY ("organization_id", "regenerated_invoice_id") REFERENCES "invoices" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- Create index "invoice_subscriptions_charges_key" to table: "invoice_subscriptions"
CREATE UNIQUE INDEX "invoice_subscriptions_charges_key" ON "invoice_subscriptions" ("organization_id", "subscription_id", "charges_from_datetime", "charges_to_datetime") WHERE (recurring AND (charges_from_datetime <> '0001-01-01 00:00:00+00'::timestamp with time zone) AND (regenerated_invoice_id IS NULL));
-- Create index "invoice_subscriptions_fixed_charges_key" to table: "invoice_subscriptions"
CREATE UNIQUE INDEX "invoice_subscriptions_fixed_charges_key" ON "invoice_subscriptions" ("organization_id", "subscription_id", "fixed_charges_from_datetime", "fixed_charges_to_datetime") WHERE (recurring AND (fixed_charges_from_datetime <> '0001-01-01 00:00:00+00'::timestamp with time zone) AND (regenerated_invoice_id IS NULL));
-- Create index "invoice_subscriptions_reason_key" to table: "invoice_subscriptions"
CREATE UNIQUE INDEX "invoice_subscriptions_reason_key" ON "invoice_subscriptions" ("organization_id", "subscription_id", "invoicing_reason") WHERE ((invoicing_reason = ANY (ARRAY['subscription_starting'::text, 'subscription_terminating'::text])) AND (regenerated_invoice_id IS NULL));
-- Create index "invoice_subscriptions_regenerated_invoice_idx" to table: "invoice_subscriptions"
CREATE INDEX "invoice_subscriptions_regenerated_invoice_idx" ON "invoice_subscriptions" ("organization_id", "regenerated_invoice_id") WHERE (regenerated_invoice_id IS NOT NULL);
-- Create "pending_vies_checks" table
CREATE TABLE "pending_vies_checks" ("organization_id" uuid NOT NULL, "customer_id" uuid NOT NULL, "billing_entity_id" uuid NOT NULL, "tax_identification_number" text NULL, "attempts_count" integer NOT NULL DEFAULT 0, "last_attempt_at" timestamptz NULL, "last_error_type" text NULL, "last_error_message" text NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("organization_id", "customer_id"), CONSTRAINT "pending_vies_checks_billing_entity_fkey" FOREIGN KEY ("organization_id", "billing_entity_id") REFERENCES "billing_entities" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "pending_vies_checks_customer_fkey" FOREIGN KEY ("organization_id", "customer_id") REFERENCES "customers" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "pending_vies_checks_attempts_count_check" CHECK (attempts_count >= 0), CONSTRAINT "pending_vies_checks_last_error_type_check" CHECK (last_error_type = ANY (ARRAY['rate_limit'::text, 'timeout'::text, 'blocked'::text, 'invalid_requester'::text, 'service_unavailable'::text, 'member_state_unavailable'::text, 'unknown'::text])));
-- Create index "pending_vies_checks_billing_entity_idx" to table: "pending_vies_checks"
CREATE INDEX "pending_vies_checks_billing_entity_idx" ON "pending_vies_checks" ("organization_id", "billing_entity_id");
-- Modify "billable_metrics" table
ALTER TABLE "billable_metrics" DROP CONSTRAINT "billable_metrics_code_key", ADD COLUMN "deleted_at" timestamptz NULL;
-- Create index "billable_metrics_code_key" to table: "billable_metrics"
CREATE UNIQUE INDEX "billable_metrics_code_key" ON "billable_metrics" ("organization_id", "code") WHERE (deleted_at IS NULL);
-- Create "product_categories" table
CREATE TABLE "product_categories" ("organization_id" uuid NOT NULL, "id" uuid NOT NULL, "code" text NOT NULL, "name" text NOT NULL, "description" text NULL, "invoice_display_name" text NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "deleted_at" timestamptz NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "product_categories_organization_fkey" FOREIGN KEY ("organization_id") REFERENCES "organizations" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "product_categories_code_idx" to table: "product_categories"
CREATE UNIQUE INDEX "product_categories_code_idx" ON "product_categories" ("organization_id", "code") WHERE (deleted_at IS NULL);
-- Create "products" table
CREATE TABLE "products" ("organization_id" uuid NOT NULL, "id" uuid NOT NULL, "product_category_id" uuid NULL, "billable_metric_id" uuid NULL, "product_type" text NOT NULL, "code" text NOT NULL, "name" text NOT NULL, "description" text NULL, "invoice_display_name" text NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "deleted_at" timestamptz NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "products_billable_metric_fkey" FOREIGN KEY ("organization_id", "billable_metric_id") REFERENCES "billable_metrics" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "products_organization_fkey" FOREIGN KEY ("organization_id") REFERENCES "organizations" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "products_product_category_fkey" FOREIGN KEY ("organization_id", "product_category_id") REFERENCES "product_categories" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "products_billable_metric_check" CHECK ((product_type = 'metered'::text) = (billable_metric_id IS NOT NULL)), CONSTRAINT "products_product_type_check" CHECK (product_type = ANY (ARRAY['metered'::text, 'fixed'::text])));
-- Create index "products_billable_metric_idx" to table: "products"
CREATE INDEX "products_billable_metric_idx" ON "products" ("organization_id", "billable_metric_id");
-- Create index "products_code_idx" to table: "products"
CREATE UNIQUE INDEX "products_code_idx" ON "products" ("organization_id", "code") WHERE (deleted_at IS NULL);
-- Create index "products_product_category_idx" to table: "products"
CREATE INDEX "products_product_category_idx" ON "products" ("organization_id", "product_category_id");
-- Create "product_filters" table
CREATE TABLE "product_filters" ("organization_id" uuid NOT NULL, "id" uuid NOT NULL, "product_id" uuid NOT NULL, "code" text NOT NULL, "name" text NOT NULL, "description" text NULL, "invoice_display_name" text NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "deleted_at" timestamptz NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "product_filters_product_fkey" FOREIGN KEY ("organization_id", "product_id") REFERENCES "products" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE CASCADE);
-- Create index "product_filters_code_idx" to table: "product_filters"
CREATE UNIQUE INDEX "product_filters_code_idx" ON "product_filters" ("organization_id", "product_id", "code") WHERE (deleted_at IS NULL);
-- Create "rate_cards" table
CREATE TABLE "rate_cards" ("organization_id" uuid NOT NULL, "id" uuid NOT NULL, "product_id" uuid NOT NULL, "product_filter_id" uuid NULL, "code" text NOT NULL, "name" text NOT NULL, "description" text NULL, "currency" text NOT NULL, "billing_timing" text NOT NULL, "proration" boolean NOT NULL, "display_on_invoice" boolean NOT NULL, "regroup_paid_fees" text NULL, "pricing_unit_id" uuid NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "deleted_at" timestamptz NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "rate_cards_pricing_unit_fkey" FOREIGN KEY ("organization_id", "pricing_unit_id") REFERENCES "pricing_units" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "rate_cards_product_filter_fkey" FOREIGN KEY ("organization_id", "product_filter_id") REFERENCES "product_filters" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "rate_cards_product_fkey" FOREIGN KEY ("organization_id", "product_id") REFERENCES "products" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "rate_cards_billing_timing_check" CHECK (billing_timing = ANY (ARRAY['arrears'::text, 'advance'::text])), CONSTRAINT "rate_cards_regroup_paid_fees_check" CHECK (regroup_paid_fees = 'invoice'::text));
-- Create index "rate_cards_code_idx" to table: "rate_cards"
CREATE UNIQUE INDEX "rate_cards_code_idx" ON "rate_cards" ("organization_id", "code") WHERE (deleted_at IS NULL);
-- Create index "rate_cards_product_filter_idx" to table: "rate_cards"
CREATE INDEX "rate_cards_product_filter_idx" ON "rate_cards" ("organization_id", "product_filter_id");
-- Create index "rate_cards_product_idx" to table: "rate_cards"
CREATE INDEX "rate_cards_product_idx" ON "rate_cards" ("organization_id", "product_id");
-- Create "plan_rate_cards" table
CREATE TABLE "plan_rate_cards" ("organization_id" uuid NOT NULL, "id" uuid NOT NULL, "plan_id" uuid NOT NULL, "rate_card_id" uuid NOT NULL, "units" numeric NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "deleted_at" timestamptz NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "plan_rate_cards_plan_fkey" FOREIGN KEY ("organization_id", "plan_id") REFERENCES "plans" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "plan_rate_cards_rate_card_fkey" FOREIGN KEY ("organization_id", "rate_card_id") REFERENCES "rate_cards" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "plan_rate_cards_units_check" CHECK (units >= (0)::numeric));
-- Create index "plan_rate_cards_rate_card_idx" to table: "plan_rate_cards"
CREATE INDEX "plan_rate_cards_rate_card_idx" ON "plan_rate_cards" ("organization_id", "rate_card_id");
-- Create index "plan_rate_cards_rate_card_key_idx" to table: "plan_rate_cards"
CREATE UNIQUE INDEX "plan_rate_cards_rate_card_key_idx" ON "plan_rate_cards" ("organization_id", "plan_id", "rate_card_id") WHERE (deleted_at IS NULL);
-- Create "product_filter_values" table
CREATE TABLE "product_filter_values" ("organization_id" uuid NOT NULL, "id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "product_filter_id" uuid NOT NULL, "position" integer NOT NULL, "billable_metric_filter_id" uuid NOT NULL, "value" text NULL, "deleted_at" timestamptz NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "product_filter_values_product_filter_fkey" FOREIGN KEY ("organization_id", "product_filter_id") REFERENCES "product_filters" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE CASCADE);
-- Create index "product_filter_values_billable_metric_filter_idx" to table: "product_filter_values"
CREATE INDEX "product_filter_values_billable_metric_filter_idx" ON "product_filter_values" ("organization_id", "billable_metric_filter_id");
-- Create index "product_filter_values_position_idx" to table: "product_filter_values"
CREATE UNIQUE INDEX "product_filter_values_position_idx" ON "product_filter_values" ("organization_id", "product_filter_id", "position") WHERE (deleted_at IS NULL);
-- Create "rate_card_rates" table
CREATE TABLE "rate_card_rates" ("organization_id" uuid NOT NULL, "id" uuid NOT NULL, "rate_card_id" uuid NOT NULL, "code" text NOT NULL, "effective_from" timestamptz NOT NULL, "rate_model" text NOT NULL, "properties" json NOT NULL, "min_amount" bigint NOT NULL, "billing_interval_count" integer NOT NULL, "billing_interval_unit" text NOT NULL, "pricing_unit_conversion_rate" numeric NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "deleted_at" timestamptz NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "rate_card_rates_rate_card_fkey" FOREIGN KEY ("organization_id", "rate_card_id") REFERENCES "rate_cards" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "rate_card_rates_billing_interval_count_check" CHECK (billing_interval_count >= 1), CONSTRAINT "rate_card_rates_billing_interval_unit_check" CHECK (billing_interval_unit = ANY (ARRAY['day'::text, 'week'::text, 'month'::text, 'year'::text])), CONSTRAINT "rate_card_rates_min_amount_check" CHECK (min_amount >= 0), CONSTRAINT "rate_card_rates_pricing_unit_conversion_rate_check" CHECK (pricing_unit_conversion_rate > (0)::numeric), CONSTRAINT "rate_card_rates_rate_model_check" CHECK (rate_model = ANY (ARRAY['standard'::text, 'graduated'::text, 'graduated_percentage'::text, 'package'::text, 'percentage'::text, 'volume'::text, 'dynamic'::text])));
-- Create index "rate_card_rates_code_idx" to table: "rate_card_rates"
CREATE UNIQUE INDEX "rate_card_rates_code_idx" ON "rate_card_rates" ("organization_id", "rate_card_id", "code") WHERE (deleted_at IS NULL);
-- Create index "rate_card_rates_effective_from_idx" to table: "rate_card_rates"
CREATE UNIQUE INDEX "rate_card_rates_effective_from_idx" ON "rate_card_rates" ("organization_id", "rate_card_id", "effective_from") WHERE (deleted_at IS NULL);
-- Modify "taxes" table
ALTER TABLE "taxes" DROP CONSTRAINT "taxes_code_key", ADD COLUMN "deleted_at" timestamptz NULL;
-- Create index "taxes_code_key" to table: "taxes"
CREATE UNIQUE INDEX "taxes_code_key" ON "taxes" ("organization_id", "code") WHERE (deleted_at IS NULL);
-- Create "rate_card_taxes" table
CREATE TABLE "rate_card_taxes" ("organization_id" uuid NOT NULL, "rate_card_id" uuid NOT NULL, "tax_id" uuid NOT NULL, "position" integer NOT NULL, PRIMARY KEY ("organization_id", "rate_card_id", "tax_id"), CONSTRAINT "rate_card_taxes_rate_card_fkey" FOREIGN KEY ("organization_id", "rate_card_id") REFERENCES "rate_cards" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "rate_card_taxes_tax_fkey" FOREIGN KEY ("organization_id", "tax_id") REFERENCES "taxes" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE CASCADE);
-- Create index "rate_card_taxes_tax_idx" to table: "rate_card_taxes"
CREATE INDEX "rate_card_taxes_tax_idx" ON "rate_card_taxes" ("organization_id", "tax_id");
-- Create "rate_phases" table
CREATE TABLE "rate_phases" ("organization_id" uuid NOT NULL, "id" uuid NOT NULL, "plan_rate_card_id" uuid NOT NULL, "code" text NOT NULL, "position" integer NOT NULL, "name" text NULL, "billing_interval_cycle_count" integer NULL, "override_rate_model" text NULL, "override_properties" json NULL, "override_min_amount" bigint NULL, "override_billing_interval_count" integer NULL, "override_billing_interval_unit" text NULL, "override_pricing_unit_conversion_rate" numeric NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "deleted_at" timestamptz NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "rate_phases_plan_rate_card_fkey" FOREIGN KEY ("organization_id", "plan_rate_card_id") REFERENCES "plan_rate_cards" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "rate_phases_billing_interval_cycle_count_check" CHECK (billing_interval_cycle_count > 0), CONSTRAINT "rate_phases_override_billing_interval_unit_check" CHECK (override_billing_interval_unit = ANY (ARRAY['day'::text, 'week'::text, 'month'::text, 'year'::text])), CONSTRAINT "rate_phases_override_check" CHECK ((override_rate_model IS NULL) = (override_properties IS NULL)), CONSTRAINT "rate_phases_override_rate_model_check" CHECK (override_rate_model = ANY (ARRAY['standard'::text, 'graduated'::text, 'graduated_percentage'::text, 'package'::text, 'percentage'::text, 'volume'::text, 'dynamic'::text])), CONSTRAINT "rate_phases_position_check" CHECK ("position" > 0));
-- Create index "rate_phases_code_idx" to table: "rate_phases"
CREATE UNIQUE INDEX "rate_phases_code_idx" ON "rate_phases" ("organization_id", "plan_rate_card_id", "code") WHERE (deleted_at IS NULL);
-- Create index "rate_phases_position_idx" to table: "rate_phases"
CREATE UNIQUE INDEX "rate_phases_position_idx" ON "rate_phases" ("organization_id", "plan_rate_card_id", "position") WHERE (deleted_at IS NULL);
-- Create "record_deletions" table
CREATE TABLE "record_deletions" ("organization_id" uuid NOT NULL, "id" uuid NOT NULL, "record_table" text NOT NULL, "record_id" uuid NOT NULL, "deleted_at" timestamptz NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "record_deletions_organization_fkey" FOREIGN KEY ("organization_id") REFERENCES "organizations" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "record_deletions_record_table_check" CHECK (record_table = ANY (ARRAY['fees'::text, 'invoice_subscriptions'::text, 'invoice_applied_taxes'::text, 'credit_note_applied_taxes'::text])));
-- Create index "record_deletions_deleted_at_idx" to table: "record_deletions"
CREATE INDEX "record_deletions_deleted_at_idx" ON "record_deletions" ("organization_id", "deleted_at");
-- Create index "record_deletions_expiry_idx" to table: "record_deletions"
CREATE INDEX "record_deletions_expiry_idx" ON "record_deletions" ("deleted_at");
-- Create index "record_deletions_updated_at_idx" to table: "record_deletions"
CREATE INDEX "record_deletions_updated_at_idx" ON "record_deletions" ("organization_id", "updated_at");
-- Create "security_logs" table
CREATE TABLE "security_logs" ("organization_id" uuid NOT NULL, "id" uuid NOT NULL, "user_id" uuid NULL, "api_key_id" uuid NULL, "log_type" text NOT NULL, "log_event" text NOT NULL, "resources" jsonb NULL, "device_info" jsonb NULL, "logged_at" timestamptz NOT NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "security_logs_organization_fkey" FOREIGN KEY ("organization_id") REFERENCES "organizations" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "security_logs_logged_at_idx" to table: "security_logs"
CREATE INDEX "security_logs_logged_at_idx" ON "security_logs" ("organization_id", "logged_at" DESC, "id");
-- Create "usage_attribution_types" table
CREATE TABLE "usage_attribution_types" ("organization_id" uuid NOT NULL, "id" uuid NOT NULL, "code" text NOT NULL, "name" text NULL, "description" text NULL, "attribution_keys" text[] NOT NULL, "role" text NOT NULL, "parent_id" uuid NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "deleted_at" timestamptz NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "usage_attribution_types_organization_fkey" FOREIGN KEY ("organization_id") REFERENCES "organizations" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "usage_attribution_types_parent_fkey" FOREIGN KEY ("organization_id", "parent_id") REFERENCES "usage_attribution_types" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "usage_attribution_types_attribution_keys_check" CHECK ((cardinality(attribution_keys) >= 1) AND (cardinality(attribution_keys) <= 4)), CONSTRAINT "usage_attribution_types_role_check" CHECK (role = ANY (ARRAY['hierarchical'::text, 'flat'::text])));
-- Create index "usage_attribution_types_code_key" to table: "usage_attribution_types"
CREATE UNIQUE INDEX "usage_attribution_types_code_key" ON "usage_attribution_types" ("organization_id", "code") WHERE (deleted_at IS NULL);
-- Create index "usage_attribution_types_parent_idx" to table: "usage_attribution_types"
CREATE INDEX "usage_attribution_types_parent_idx" ON "usage_attribution_types" ("organization_id", "parent_id") WHERE (parent_id IS NOT NULL);
-- Create "usage_attribution_values" table
CREATE TABLE "usage_attribution_values" ("organization_id" uuid NOT NULL, "id" uuid NOT NULL, "usage_attribution_type_id" uuid NOT NULL, "customer_id" uuid NOT NULL, "parent_id" uuid NULL, "value" text NOT NULL, "last_seen_at" timestamptz NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "deleted_at" timestamptz NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "usage_attribution_values_value_key" UNIQUE ("organization_id", "customer_id", "usage_attribution_type_id", "value"), CONSTRAINT "usage_attribution_values_customer_fkey" FOREIGN KEY ("organization_id", "customer_id") REFERENCES "customers" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "usage_attribution_values_organization_fkey" FOREIGN KEY ("organization_id") REFERENCES "organizations" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "usage_attribution_values_parent_fkey" FOREIGN KEY ("organization_id", "parent_id") REFERENCES "usage_attribution_values" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "usage_attribution_values_type_fkey" FOREIGN KEY ("organization_id", "usage_attribution_type_id") REFERENCES "usage_attribution_types" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "usage_attribution_values_customer_seen_idx" to table: "usage_attribution_values"
CREATE INDEX "usage_attribution_values_customer_seen_idx" ON "usage_attribution_values" ("organization_id", "customer_id", "last_seen_at");
-- Create index "usage_attribution_values_type_idx" to table: "usage_attribution_values"
CREATE INDEX "usage_attribution_values_type_idx" ON "usage_attribution_values" ("organization_id", "usage_attribution_type_id");
-- Enable row-level security on table "activity_logs"
ALTER TABLE activity_logs ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "api_logs"
ALTER TABLE api_logs ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "applied_invoice_custom_sections"
ALTER TABLE applied_invoice_custom_sections ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "daily_usages"
ALTER TABLE daily_usages ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "fixed_charge_units_overrides"
ALTER TABLE fixed_charge_units_overrides ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "idempotency_keys"
ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "pending_vies_checks"
ALTER TABLE pending_vies_checks ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "plan_rate_cards"
ALTER TABLE plan_rate_cards ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "product_categories"
ALTER TABLE product_categories ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "product_filter_values"
ALTER TABLE product_filter_values ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "product_filters"
ALTER TABLE product_filters ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "products"
ALTER TABLE products ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "rate_card_rates"
ALTER TABLE rate_card_rates ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "rate_card_taxes"
ALTER TABLE rate_card_taxes ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "rate_cards"
ALTER TABLE rate_cards ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "rate_phases"
ALTER TABLE rate_phases ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "record_deletions"
ALTER TABLE record_deletions ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "security_logs"
ALTER TABLE security_logs ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "usage_attribution_types"
ALTER TABLE usage_attribution_types ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "usage_attribution_values"
ALTER TABLE usage_attribution_values ENABLE ROW LEVEL SECURITY;
-- Create policy "tenant_isolation" on table "activity_logs"
CREATE POLICY tenant_isolation ON activity_logs AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "api_logs"
CREATE POLICY tenant_isolation ON api_logs AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "applied_invoice_custom_sections"
CREATE POLICY tenant_isolation ON applied_invoice_custom_sections AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "daily_usages"
CREATE POLICY tenant_isolation ON daily_usages AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "fixed_charge_units_overrides"
CREATE POLICY tenant_isolation ON fixed_charge_units_overrides AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "idempotency_keys"
CREATE POLICY tenant_isolation ON idempotency_keys AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "pending_vies_checks"
CREATE POLICY tenant_isolation ON pending_vies_checks AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "plan_rate_cards"
CREATE POLICY tenant_isolation ON plan_rate_cards AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "product_categories"
CREATE POLICY tenant_isolation ON product_categories AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "product_filter_values"
CREATE POLICY tenant_isolation ON product_filter_values AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "product_filters"
CREATE POLICY tenant_isolation ON product_filters AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "products"
CREATE POLICY tenant_isolation ON products AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "rate_card_rates"
CREATE POLICY tenant_isolation ON rate_card_rates AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "rate_card_taxes"
CREATE POLICY tenant_isolation ON rate_card_taxes AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "rate_cards"
CREATE POLICY tenant_isolation ON rate_cards AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "rate_phases"
CREATE POLICY tenant_isolation ON rate_phases AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "record_deletions"
CREATE POLICY tenant_isolation ON record_deletions AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "security_logs"
CREATE POLICY tenant_isolation ON security_logs AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "usage_attribution_types"
CREATE POLICY tenant_isolation ON usage_attribution_types AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "usage_attribution_values"
CREATE POLICY tenant_isolation ON usage_attribution_values AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Drop constraint "credit_notes_status_check" of table "credit_notes"
ALTER TABLE credit_notes DROP CONSTRAINT credit_notes_status_check;
-- Drop constraint "fees_original_fee_fkey" of table "fees"
ALTER TABLE fees DROP CONSTRAINT fees_original_fee_fkey;
-- Drop constraint "invoice_subscriptions_regenerated_invoice_fkey" of table "invoice_subscriptions"
ALTER TABLE invoice_subscriptions DROP CONSTRAINT invoice_subscriptions_regenerated_invoice_fkey;
-- Drop constraint "invoices_status_check" of table "invoices"
ALTER TABLE invoices DROP CONSTRAINT invoices_status_check;
-- Drop constraint "job_outbox_queue_check" of table "job_outbox"
ALTER TABLE job_outbox DROP CONSTRAINT job_outbox_queue_check;
-- Create constraint "credit_notes_status_check" on table "credit_notes"
ALTER TABLE credit_notes ADD CONSTRAINT credit_notes_status_check CHECK ((status = ANY (ARRAY['draft'::text, 'finalized'::text, 'deleted'::text])));
-- Create constraint "fees_original_fee_fkey" on table "fees"
ALTER TABLE fees ADD CONSTRAINT fees_original_fee_fkey FOREIGN KEY (organization_id, original_fee_id) REFERENCES fees(organization_id, id) ON DELETE SET NULL (original_fee_id);
-- Create constraint "invoice_subscriptions_regenerated_invoice_fkey" on table "invoice_subscriptions"
ALTER TABLE invoice_subscriptions ADD CONSTRAINT invoice_subscriptions_regenerated_invoice_fkey FOREIGN KEY (organization_id, regenerated_invoice_id) REFERENCES invoices(organization_id, id) ON DELETE SET NULL (regenerated_invoice_id);
-- Create constraint "invoices_status_check" on table "invoices"
ALTER TABLE invoices ADD CONSTRAINT invoices_status_check CHECK ((status = ANY (ARRAY['generating'::text, 'draft'::text, 'open'::text, 'finalized'::text, 'closed'::text, 'voided'::text, 'failed'::text, 'deleted'::text])));
-- Create constraint "job_outbox_queue_check" on table "job_outbox"
ALTER TABLE job_outbox ADD CONSTRAINT job_outbox_queue_check CHECK ((queue = ANY (ARRAY['billing'::text, 'payments'::text, 'webhooks'::text, 'documents'::text, 'integrations'::text, 'exports'::text, 'alerts'::text, 'alerts_high_priority'::text, 'wallets'::text, 'default'::text])));
