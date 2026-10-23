-- The event ingestion log.

-- name: AppendEvent :execrows
INSERT INTO events (
  organization_id, id, transaction_id, code, external_subscription_id, timestamp,
  properties, precise_total_amount, attribution_labels, ingested_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @transaction_id, @code, @external_subscription_id, @timestamp,
  @properties, @precise_total_amount, @attribution_labels, @ingested_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: ListEventKeys :many
-- The stored deduplication keys (subscription, transaction ID) of the
-- transaction IDs.
SELECT external_subscription_id, transaction_id FROM events
WHERE organization_id = @organization_id AND transaction_id = ANY(@transaction_ids::text[]);

-- name: GetEvent :one
SELECT * FROM events
WHERE organization_id = @organization_id AND external_subscription_id = @external_subscription_id AND transaction_id = @transaction_id;

-- name: GetFirstEvent :one
-- The earliest ingested event with that transaction ID, of any subscription.
SELECT * FROM events WHERE organization_id = @organization_id AND transaction_id = @transaction_id
ORDER BY ingested_at, id LIMIT 1;

-- name: ReplaceEvent :execrows
-- Replaces the event of the deduplication key, keeping its id, ingestion
-- and creation instants.
UPDATE events SET
  code = @code, timestamp = @timestamp,
  properties = @properties, precise_total_amount = @precise_total_amount,
  attribution_labels = @attribution_labels, updated_at = @updated_at
WHERE organization_id = @organization_id AND external_subscription_id = @external_subscription_id AND transaction_id = @transaction_id;
