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

-- name: ListEvents :many
-- Newest first: by timestamp descending, then id descending
-- (events_query.rb:22); the cursor is (@after_at, @after).
SELECT * FROM events
WHERE organization_id = @organization_id
  AND (sqlc.narg(code)::text IS NULL OR code = sqlc.narg(code))
  AND (sqlc.narg(external_subscription_id)::text IS NULL OR external_subscription_id = sqlc.narg(external_subscription_id))
  AND (sqlc.narg(timestamp_from)::timestamptz IS NULL OR timestamp >= sqlc.narg(timestamp_from))
  AND (sqlc.narg(timestamp_to)::timestamptz IS NULL OR timestamp <= sqlc.narg(timestamp_to))
  AND (sqlc.narg(after)::uuid IS NULL
       OR timestamp < sqlc.narg(after_at)::timestamptz
       OR (timestamp = sqlc.narg(after_at)::timestamptz AND id < sqlc.narg(after)))
ORDER BY timestamp DESC, id DESC
LIMIT @lim;

-- name: ListEventValidationErrors :many
-- The post-validation errors of the events created in [@from_at, @to_at)
-- (event.ValidationErrors, lago db/views/last_hour_events_mv_v04.sql): the
-- metrics are joined on the code alone, discarded ones included, and an
-- event whose metrics are all discarded is not checked.
WITH matched AS (
  SELECT e.transaction_id, e.properties, m.id AS metric_id, m.aggregation_type, m.field_name, m.deleted_at
  FROM events e
  LEFT JOIN billable_metrics m ON m.organization_id = e.organization_id AND m.code = e.code
  WHERE e.organization_id = @organization_id
    AND e.created_at >= @from_at::timestamptz AND e.created_at < @to_at::timestamptz
)
SELECT 'invalid_code'::text AS kind, transaction_id FROM matched WHERE metric_id IS NULL
UNION
SELECT 'missing_aggregation_property'::text, transaction_id FROM matched
WHERE metric_id IS NOT NULL AND deleted_at IS NULL AND aggregation_type <> 'count'
  AND (properties ->> field_name IS NULL
       OR (aggregation_type IN ('sum', 'max', 'weighted_sum', 'latest')
           AND NOT (properties ->> field_name ~ '^-?\d+(\.\d+)?$')))
UNION
SELECT 'invalid_filter_values'::text, mt.transaction_id FROM matched mt
JOIN billable_metric_filters f ON f.organization_id = @organization_id AND f.billable_metric_id = mt.metric_id
WHERE mt.deleted_at IS NULL AND (mt.properties -> f.key) IS NOT NULL
  AND NOT ((mt.properties ->> f.key) = ANY (f.filter_values))
ORDER BY kind, transaction_id;

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
