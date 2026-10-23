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

-- name: ListEventTransactionIDs :many
SELECT transaction_id FROM events
WHERE organization_id = @organization_id AND transaction_id = ANY(@transaction_ids::text[]);

-- name: GetEvent :one
SELECT * FROM events WHERE organization_id = @organization_id AND transaction_id = @transaction_id;

-- name: ReplaceEvent :execrows
-- Replaces the event with that transaction ID, keeping its id, ingestion
-- and creation instants.
UPDATE events SET
  code = @code, external_subscription_id = @external_subscription_id, timestamp = @timestamp,
  properties = @properties, precise_total_amount = @precise_total_amount,
  attribution_labels = @attribution_labels, updated_at = @updated_at
WHERE organization_id = @organization_id AND transaction_id = @transaction_id;
