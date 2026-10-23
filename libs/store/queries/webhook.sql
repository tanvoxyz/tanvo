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
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;

-- name: ListDueWebhookDeliveries :many
-- Across organizations: the dispatcher sweep. A NULL limit reads every due
-- delivery.
SELECT * FROM webhook_deliveries
WHERE status IN ('pending', 'retrying') AND next_attempt_at IS NOT NULL AND next_attempt_at <= @now
ORDER BY next_attempt_at, id
LIMIT sqlc.narg(lim)::integer;

-- name: PurgeWebhookDeliveries :execrows
-- Across organizations: the retention purge.
DELETE FROM webhook_deliveries WHERE created_at < @cutoff;

-- name: RecordDomainEvent :execrows
INSERT INTO domain_events (
  organization_id, id, event_type, object_type, object_id, payload, occurred_at
) VALUES (
  @organization_id, @id, @event_type, @object_type, @object_id, @payload, @occurred_at
)
ON CONFLICT DO NOTHING;

-- name: GetDomainEvent :one
SELECT * FROM domain_events WHERE organization_id = @organization_id AND id = @id;

-- name: ListDomainEventsByObject :many
SELECT * FROM domain_events
WHERE organization_id = @organization_id AND object_type = @object_type AND object_id = @object_id
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;
