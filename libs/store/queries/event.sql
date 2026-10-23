-- The event ingestion log.

-- name: AppendEvent :execrows
INSERT INTO events (
  organization_id, id, transaction_id, code, external_subscription_id, timestamp,
  properties, precise_total_amount, ingested_at, created_at, updated_at, metadata
) VALUES (
  @organization_id, @id, @transaction_id, @code, @external_subscription_id, @timestamp,
  @properties, @precise_total_amount, @ingested_at, @created_at, @updated_at, @metadata
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
-- and creation instants and the metadata of the request that ingested it.
UPDATE events SET
  code = @code, timestamp = @timestamp,
  properties = @properties, precise_total_amount = @precise_total_amount,
  updated_at = @updated_at
WHERE organization_id = @organization_id AND external_subscription_id = @external_subscription_id AND transaction_id = @transaction_id;

-- name: CreateCachedAggregation :execrows
INSERT INTO cached_aggregations (
  organization_id, id, external_subscription_id, charge_id, charge_filter_id, timestamp, event_transaction_id,
  grouped_by, current_aggregation, max_aggregation, max_aggregation_with_proration, presentation_breakdowns, created_at
) VALUES (
  @organization_id, @id, @external_subscription_id, @charge_id, @charge_filter_id, @timestamp, @event_transaction_id,
  @grouped_by, @current_aggregation, @max_aggregation, @max_aggregation_with_proration, @presentation_breakdowns, @created_at
)
ON CONFLICT DO NOTHING;

-- name: CachedAggregationExists :one
-- Reports whether a state of the charge, filter (NULL matching states
-- without one), groups and timestamp exists.
SELECT EXISTS (
  SELECT 1 FROM cached_aggregations
  WHERE organization_id = @organization_id AND external_subscription_id = @external_subscription_id
    AND charge_id = @charge_id AND charge_filter_id IS NOT DISTINCT FROM sqlc.narg(charge_filter_id)::uuid
    AND timestamp = @timestamp AND grouped_by = @grouped_by
);

-- name: LatestCachedAggregationInPeriod :one
-- A NULL charge filter selects the states without a filter of a default
-- bucket, else every filter of the charge; an excluded transaction also
-- drops states without one (SQL inequality).
SELECT * FROM cached_aggregations
WHERE organization_id = @organization_id AND external_subscription_id = @external_subscription_id
  AND charge_id = @charge_id
  AND (charge_filter_id IS NOT DISTINCT FROM sqlc.narg(charge_filter_id)::uuid
    OR (sqlc.narg(charge_filter_id)::uuid IS NULL AND NOT @default_bucket::boolean))
  AND timestamp >= @from_datetime AND timestamp <= @to_datetime
  AND grouped_by = @grouped_by
  AND (@exclude_transaction_id::text = '' OR event_transaction_id <> @exclude_transaction_id::text)
ORDER BY timestamp DESC, created_at DESC, id DESC
LIMIT 1;

-- name: ListCachedAggregationsBefore :many
-- A NULL charge filter selects the states without a filter of a default
-- bucket, else every filter of the charge.
SELECT * FROM cached_aggregations
WHERE organization_id = @organization_id AND external_subscription_id = @external_subscription_id
  AND charge_id = @charge_id
  AND (charge_filter_id IS NOT DISTINCT FROM sqlc.narg(charge_filter_id)::uuid
    OR (sqlc.narg(charge_filter_id)::uuid IS NULL AND NOT @default_bucket::boolean))
  AND timestamp < @before
ORDER BY timestamp DESC, created_at DESC, id DESC;
