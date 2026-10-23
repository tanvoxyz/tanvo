-- Drop index "job_outbox_unique_key" from table: "job_outbox"
DROP INDEX "job_outbox_unique_key";
-- Create index "job_outbox_unique_key" to table: "job_outbox"
CREATE UNIQUE INDEX "job_outbox_unique_key" ON "job_outbox" ("organization_id", "kind", "unique_key") WHERE (unique_key IS NOT NULL);
