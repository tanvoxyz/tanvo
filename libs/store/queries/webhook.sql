-- Webhook endpoints, deliveries and domain events.

-- name: CreateWebhookEndpoint :execrows
INSERT INTO webhook_endpoints (
  organization_id, id, url, name, event_types, secret, created_at, updated_at
) VALUES (
  @organization_id, @id, @url, @name, @event_types, @secret, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateWebhookEndpoint :execrows
UPDATE webhook_endpoints SET
  organization_id = @organization_id, id = @id, url = @url, name = @name, event_types = @event_types,
  secret = @secret, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetWebhookEndpoint :one
SELECT * FROM webhook_endpoints WHERE organization_id = @organization_id AND id = @id;

-- name: ListWebhookEndpoints :many
SELECT * FROM webhook_endpoints
WHERE organization_id = @organization_id
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;

-- name: ListSubscribedWebhookEndpoints :many
SELECT * FROM webhook_endpoints
WHERE organization_id = @organization_id
  AND (event_types IS NULL OR @event_type::text = ANY(event_types))
ORDER BY id;

-- name: CountWebhookEndpoints :one
SELECT count(*) FROM webhook_endpoints WHERE organization_id = @organization_id;

-- name: DeleteWebhookEndpoint :execrows
DELETE FROM webhook_endpoints WHERE organization_id = @organization_id AND id = @id;

-- name: CreateWebhookDelivery :execrows
INSERT INTO webhook_deliveries (
  organization_id, id, webhook_endpoint_id, event_id, event_type, object_type, object_id, payload,
  status, attempts, last_attempt_at, next_attempt_at, http_status, response, created_at, updated_at
) VALUES (
  @organization_id, @id, @webhook_endpoint_id, @event_id, @event_type, @object_type, @object_id, @payload,
  @status, @attempts, @last_attempt_at, @next_attempt_at, @http_status, @response, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateWebhookDelivery :execrows
UPDATE webhook_deliveries SET
  organization_id = @organization_id, id = @id, webhook_endpoint_id = @webhook_endpoint_id, event_id = @event_id,
  event_type = @event_type, object_type = @object_type, object_id = @object_id, payload = @payload,
  status = @status, attempts = @attempts, last_attempt_at = @last_attempt_at, next_attempt_at = @next_attempt_at,
  http_status = @http_status, response = @response, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetWebhookDelivery :one
SELECT * FROM webhook_deliveries WHERE organization_id = @organization_id AND id = @id;

-- name: ListWebhookDeliveriesByEndpoint :many
SELECT * FROM webhook_deliveries
WHERE organization_id = @organization_id AND webhook_endpoint_id = @webhook_endpoint_id
  AND (cardinality(@statuses::text[]) = 0 OR status = ANY(@statuses::text[]))
  AND (cardinality(@event_types::text[]) = 0 OR event_type = ANY(@event_types::text[]))
  AND (sqlc.narg(search)::text IS NULL OR id::text ILIKE sqlc.narg(search) OR object_id::text ILIKE sqlc.narg(search))
  -- The HTTP status filter (webhooks_query.rb:62-78): a code in one of the
  -- ranges (@http_mins[i]..@http_maxs[i]), or, with @http_timeout, no
  -- response on a failed delivery (webhooks_query.rb:70-72).
  AND ((cardinality(@http_mins::int[]) = 0 AND NOT @http_timeout::boolean)
       OR EXISTS (SELECT 1 FROM generate_subscripts(@http_mins::int[], 1) AS k
                  WHERE http_status BETWEEN (@http_mins::int[])[k] AND (@http_maxs::int[])[k])
       OR (@http_timeout::boolean AND http_status IS NULL AND status = 'failed'))
  AND (sqlc.narg(updated_from)::timestamptz IS NULL OR updated_at >= sqlc.narg(updated_from))
  AND (sqlc.narg(updated_to)::timestamptz IS NULL OR updated_at <= sqlc.narg(updated_to))
  -- @recent lists the last updated first, then by id ascending
  -- (webhooks_query.rb:10, base_query.rb:111-112); its cursor is
  -- (@after_at, @after).
  AND (sqlc.narg(after)::uuid IS NULL
       OR (@recent::boolean AND (updated_at < sqlc.narg(after_at)::timestamptz
                                 OR (updated_at = sqlc.narg(after_at)::timestamptz AND id > sqlc.narg(after))))
       OR (NOT @recent::boolean AND id > sqlc.narg(after)))
ORDER BY CASE WHEN @recent::boolean THEN updated_at END DESC, id
LIMIT @lim;

-- name: ListDueWebhookDeliveries :many
-- Across organizations: the dispatcher sweep. A NULL limit reads every due
-- delivery.
SELECT * FROM webhook_deliveries
WHERE status IN ('pending', 'retrying') AND next_attempt_at IS NOT NULL AND next_attempt_at <= @now
ORDER BY next_attempt_at, id
LIMIT sqlc.narg(lim)::integer;

-- name: PurgeWebhookDeliveries :execrows
-- The retention purge of one organization, a batch at a time, on the last
-- change (an index-only scan of webhook_deliveries_updated_at_idx).
DELETE FROM webhook_deliveries w
WHERE w.organization_id = sqlc.arg(organization_id) AND w.id IN (
  SELECT d.id FROM webhook_deliveries d
  WHERE d.organization_id = sqlc.arg(organization_id) AND d.updated_at < sqlc.arg(cutoff)
  LIMIT sqlc.arg(lim)
);

-- name: ListWebhookEndpointOrganizations :many
-- Across organizations: the organizations with endpoints, which hold every
-- delivery (deliveries are deleted with their endpoint), for the retention
-- purge's dispatch.
SELECT DISTINCT organization_id FROM webhook_endpoints ORDER BY organization_id;

-- name: RecordDomainEvent :execrows
INSERT INTO domain_events (
  organization_id, id, event_type, object_type, object_id, payload, occurred_at
) VALUES (
  @organization_id, @id, @event_type, @object_type, @object_id, @payload, @occurred_at
)
ON CONFLICT DO NOTHING;

-- name: GetDomainEvent :one
SELECT * FROM domain_events WHERE organization_id = @organization_id AND id = @id;

-- name: ListObjectsWithDomainEventSince :many
SELECT DISTINCT object_id FROM domain_events
WHERE organization_id = @organization_id AND event_type = @event_type AND object_type = @object_type
  AND object_id = ANY(@object_ids::uuid[]) AND occurred_at >= @since
ORDER BY object_id;

-- name: ListDomainEventsByObject :many
SELECT * FROM domain_events
WHERE organization_id = @organization_id AND object_type = @object_type AND object_id = @object_id
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;
