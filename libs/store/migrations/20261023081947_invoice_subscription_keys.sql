-- Drop index "invoice_subscriptions_idempotency_key" from table: "invoice_subscriptions"
DROP INDEX "invoice_subscriptions_idempotency_key";
-- Create index "invoice_subscriptions_charges_key" to table: "invoice_subscriptions"
CREATE UNIQUE INDEX "invoice_subscriptions_charges_key" ON "invoice_subscriptions" ("organization_id", "subscription_id", "charges_from_datetime", "charges_to_datetime") WHERE (recurring AND (charges_from_datetime <> '0001-01-01 00:00:00+00'::timestamp with time zone));
-- Create index "invoice_subscriptions_fixed_charges_key" to table: "invoice_subscriptions"
CREATE UNIQUE INDEX "invoice_subscriptions_fixed_charges_key" ON "invoice_subscriptions" ("organization_id", "subscription_id", "fixed_charges_from_datetime", "fixed_charges_to_datetime") WHERE (recurring AND (fixed_charges_from_datetime <> '0001-01-01 00:00:00+00'::timestamp with time zone));
-- Create index "invoice_subscriptions_reason_key" to table: "invoice_subscriptions"
CREATE UNIQUE INDEX "invoice_subscriptions_reason_key" ON "invoice_subscriptions" ("organization_id", "subscription_id", "invoicing_reason") WHERE (invoicing_reason = ANY (ARRAY['subscription_starting'::text, 'subscription_terminating'::text]));
