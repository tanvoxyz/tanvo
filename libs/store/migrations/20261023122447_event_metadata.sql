-- Modify "events" table
ALTER TABLE "events" ADD CONSTRAINT "events_metadata_check" CHECK (jsonb_typeof(metadata) = 'object'::text), DROP COLUMN "attribution_labels", ADD COLUMN "metadata" jsonb NOT NULL DEFAULT '{}';
