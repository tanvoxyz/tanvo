-- Modify "events" table
ALTER TABLE "events" DROP CONSTRAINT "events_transaction_id_key", ADD CONSTRAINT "events_subscription_transaction_id_key" UNIQUE ("organization_id", "external_subscription_id", "transaction_id");
-- Create index "events_transaction_id_idx" to table: "events"
CREATE INDEX "events_transaction_id_idx" ON "events" ("organization_id", "transaction_id");
