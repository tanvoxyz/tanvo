-- Modify "invoices" table
ALTER TABLE "invoices" ADD COLUMN "purchase_order_number" text NULL, ADD COLUMN "x402_connection_id" uuid NULL, ADD COLUMN "x402_payment_token" text NULL;
-- Create index "invoices_purchase_order_number_idx" to table: "invoices"
CREATE INDEX "invoices_purchase_order_number_idx" ON "invoices" ("organization_id", (lower(purchase_order_number))) WHERE (purchase_order_number IS NOT NULL);
-- Create index "invoices_x402_payment_token_key" to table: "invoices"
CREATE UNIQUE INDEX "invoices_x402_payment_token_key" ON "invoices" ("x402_payment_token") WHERE (x402_payment_token IS NOT NULL);
-- Modify "recurring_transaction_rules" table
ALTER TABLE "recurring_transaction_rules" ADD COLUMN "purchase_order_number" text NULL;
-- Modify "wallet_transactions" table
ALTER TABLE "wallet_transactions" ADD COLUMN "purchase_order_number" text NULL;
-- Modify "wallets" table
ALTER TABLE "wallets" ADD COLUMN "purchase_order_number" text NULL, ADD COLUMN "x402_enabled" boolean NOT NULL DEFAULT false;
-- Create index "wallets_x402_enabled_idx" to table: "wallets"
CREATE INDEX "wallets_x402_enabled_idx" ON "wallets" ("organization_id", "customer_id") WHERE x402_enabled;
