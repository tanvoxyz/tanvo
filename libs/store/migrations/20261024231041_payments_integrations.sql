-- Modify "billing_entities" table
ALTER TABLE "billing_entities" ADD COLUMN "skip_credit_invoice_payment_delay" boolean NOT NULL DEFAULT false;
-- Drop index "integration_customers_external_customer_id_key" from table: "integration_customers"
DROP INDEX "integration_customers_external_customer_id_key";
-- Create index "integration_customers_external_customer_idx" to table: "integration_customers"
CREATE INDEX "integration_customers_external_customer_idx" ON "integration_customers" ("organization_id", "integration_id", "external_customer_id") WHERE (external_customer_id <> ''::text);
-- Modify "invoices" table
ALTER TABLE "invoices" ADD CONSTRAINT "invoices_payment_attempts_check" CHECK (payment_attempts >= 0), ADD COLUMN "payment_attempts" integer NOT NULL DEFAULT 0;
-- Modify "payments" table
ALTER TABLE "payments" ADD COLUMN "error_code" text NULL;
-- Create "credit_note_error_details" table
CREATE TABLE "credit_note_error_details" ("organization_id" uuid NOT NULL, "id" uuid NOT NULL, "credit_note_id" uuid NOT NULL, "error_code" text NOT NULL, "details" jsonb NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "credit_note_error_details_credit_note_fkey" FOREIGN KEY ("organization_id", "credit_note_id") REFERENCES "credit_notes" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "credit_note_error_details_organization_fkey" FOREIGN KEY ("organization_id") REFERENCES "organizations" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "credit_note_error_details_error_code_check" CHECK (error_code = 'tax_error'::text));
-- Create index "credit_note_error_details_credit_note_idx" to table: "credit_note_error_details"
CREATE INDEX "credit_note_error_details_credit_note_idx" ON "credit_note_error_details" ("organization_id", "credit_note_id");
-- Enable row-level security on table "credit_note_error_details"
ALTER TABLE credit_note_error_details ENABLE ROW LEVEL SECURITY;
-- Create policy "tenant_isolation" on table "credit_note_error_details"
CREATE POLICY tenant_isolation ON credit_note_error_details AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
