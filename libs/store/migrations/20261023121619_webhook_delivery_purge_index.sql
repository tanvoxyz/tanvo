-- Drop index "webhook_deliveries_created_at_idx" from table: "webhook_deliveries"
DROP INDEX "webhook_deliveries_created_at_idx";
-- Create index "webhook_deliveries_updated_at_idx" to table: "webhook_deliveries"
CREATE INDEX "webhook_deliveries_updated_at_idx" ON "webhook_deliveries" ("organization_id", "updated_at") INCLUDE ("id");
