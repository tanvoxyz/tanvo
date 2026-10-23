-- Modify "credit_note_items" table
ALTER TABLE "credit_note_items" ALTER COLUMN "fee_id" DROP NOT NULL;
-- Modify "features" table
ALTER TABLE "features" DROP CONSTRAINT "features_code_key", ADD COLUMN "deleted_at" timestamptz NULL;
-- Create index "features_code_key" to table: "features"
CREATE UNIQUE INDEX "features_code_key" ON "features" ("organization_id", "code") WHERE (deleted_at IS NULL);
-- Modify "plans" table
ALTER TABLE "plans" DROP CONSTRAINT "plans_status_check", ADD CONSTRAINT "plans_status_check" CHECK (status = ANY (ARRAY['active'::text, 'pending_deletion'::text, 'deleted'::text]));
-- Drop index "plans_code_key" from table: "plans"
DROP INDEX "plans_code_key";
-- Create index "plans_code_key" to table: "plans"
CREATE UNIQUE INDEX "plans_code_key" ON "plans" ("organization_id", "code") WHERE ((parent_id IS NULL) AND (status <> 'deleted'::text));
-- Modify "entitlement_removals" table
ALTER TABLE "entitlement_removals" DROP CONSTRAINT "entitlement_removals_feature_fkey", ADD CONSTRAINT "entitlement_removals_feature_fkey" FOREIGN KEY ("organization_id", "feature_id") REFERENCES "features" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE NO ACTION;
-- Drop index "entitlements_plan_feature_key" from table: "entitlements"
DROP INDEX "entitlements_plan_feature_key";
-- Drop index "entitlements_subscription_feature_key" from table: "entitlements"
DROP INDEX "entitlements_subscription_feature_key";
-- Modify "entitlements" table
ALTER TABLE "entitlements" DROP CONSTRAINT "entitlements_feature_fkey", ADD COLUMN "deleted_at" timestamptz NULL, ADD CONSTRAINT "entitlements_feature_fkey" FOREIGN KEY ("organization_id", "feature_id") REFERENCES "features" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE NO ACTION;
-- Create index "entitlements_plan_feature_key" to table: "entitlements"
CREATE UNIQUE INDEX "entitlements_plan_feature_key" ON "entitlements" ("organization_id", "plan_id", "feature_id") WHERE ((plan_id IS NOT NULL) AND (deleted_at IS NULL));
-- Create index "entitlements_subscription_feature_key" to table: "entitlements"
CREATE UNIQUE INDEX "entitlements_subscription_feature_key" ON "entitlements" ("organization_id", "subscription_id", "feature_id") WHERE ((subscription_id IS NOT NULL) AND (deleted_at IS NULL));
-- Modify "feature_privileges" table
ALTER TABLE "feature_privileges" DROP CONSTRAINT "feature_privileges_code_key", DROP CONSTRAINT "feature_privileges_feature_fkey", ADD COLUMN "deleted_at" timestamptz NULL, ADD CONSTRAINT "feature_privileges_feature_fkey" FOREIGN KEY ("organization_id", "feature_id") REFERENCES "features" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE NO ACTION;
-- Create index "feature_privileges_code_key" to table: "feature_privileges"
CREATE UNIQUE INDEX "feature_privileges_code_key" ON "feature_privileges" ("organization_id", "feature_id", "code") WHERE (deleted_at IS NULL);
