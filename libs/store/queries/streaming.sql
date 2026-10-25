-- Streaming destinations and their delivery locks (parity §1.40).

-- name: CreateStreamingDestination :execrows
INSERT INTO streaming_destinations (
  organization_id, id, type, active, event_types, customer_full_usage_excluded_plan_codes,
  stream_arn, region, role_arn, external_id, partition_key, created_at, updated_at
) VALUES (
  @organization_id, @id, @type, @active, @event_types, @customer_full_usage_excluded_plan_codes,
  @stream_arn, @region, @role_arn, @external_id, @partition_key, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateStreamingDestination :execrows
UPDATE streaming_destinations SET
  -- The key columns are assigned to themselves first so that the parameters
  -- follow the order of CreateStreamingDestination.
  organization_id = @organization_id, id = @id, type = @type, active = @active, event_types = @event_types,
  customer_full_usage_excluded_plan_codes = @customer_full_usage_excluded_plan_codes,
  stream_arn = @stream_arn, region = @region, role_arn = @role_arn, external_id = @external_id,
  partition_key = @partition_key, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetStreamingDestination :one
SELECT * FROM streaming_destinations WHERE organization_id = @organization_id AND id = @id;

-- name: ListStreamingDestinationsForEvent :many
SELECT * FROM streaming_destinations
WHERE organization_id = @organization_id AND active AND @event_type::text = ANY (event_types)
ORDER BY created_at, id;

-- name: ListActiveStreamingDestinations :many
SELECT * FROM streaming_destinations
WHERE organization_id = @organization_id AND active
ORDER BY created_at, id;

-- name: ClaimStreamingEnqueueLock :execrows
INSERT INTO streaming_delivery_locks AS l (organization_id, customer_id, event_type, enqueued_until)
VALUES (@organization_id, @customer_id, @event_type, @until)
ON CONFLICT (organization_id, customer_id, event_type) DO UPDATE SET enqueued_until = EXCLUDED.enqueued_until
WHERE l.enqueued_until IS NULL OR l.enqueued_until <= @now;

-- name: ClaimStreamingRunLock :one
-- The enqueue lock is released whatever happens to the runtime lock; the
-- runtime lock is taken only when free, so the returned instant is @until
-- exactly when it was taken.
INSERT INTO streaming_delivery_locks AS l (organization_id, customer_id, event_type, running_until)
VALUES (@organization_id, @customer_id, @event_type, @until)
ON CONFLICT (organization_id, customer_id, event_type) DO UPDATE SET
  enqueued_until = NULL,
  running_until = CASE WHEN l.running_until IS NULL OR l.running_until <= @now THEN EXCLUDED.running_until ELSE l.running_until END
RETURNING (l.running_until = @until::timestamptz)::boolean AS taken;

-- name: ReleaseStreamingRunLock :exec
UPDATE streaming_delivery_locks SET running_until = NULL
WHERE organization_id = @organization_id AND customer_id = @customer_id AND event_type = @event_type;
