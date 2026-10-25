-- Audit logs. Lists are most recent first: logged_at descending, then id
-- ascending; the cursor is (@after_at, @after).

-- name: CreateSecurityLog :exec
INSERT INTO security_logs (organization_id, id, user_id, api_key_id, log_type, log_event, resources, device_info, logged_at, created_at)
VALUES (@organization_id, @id, sqlc.narg(user_id), sqlc.narg(api_key_id), @log_type, @log_event, sqlc.narg(resources), sqlc.narg(device_info), @logged_at, @created_at);

-- name: GetSecurityLog :one
SELECT * FROM security_logs WHERE organization_id = @organization_id AND id = @id;

-- name: ListSecurityLogs :many
SELECT * FROM security_logs
WHERE organization_id = @organization_id
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR logged_at >= sqlc.narg(from_at))
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR logged_at <= sqlc.narg(to_at))
  AND (cardinality(@user_ids::uuid[]) = 0 OR user_id = ANY(@user_ids::uuid[]))
  AND (cardinality(@api_key_ids::uuid[]) = 0 OR api_key_id = ANY(@api_key_ids::uuid[]))
  AND (cardinality(@log_types::text[]) = 0 OR log_type = ANY(@log_types::text[]))
  AND (cardinality(@log_events::text[]) = 0 OR log_event = ANY(@log_events::text[]))
  AND (sqlc.narg(after)::uuid IS NULL
       OR logged_at < sqlc.narg(after_at)::timestamptz
       OR (logged_at = sqlc.narg(after_at)::timestamptz AND id > sqlc.narg(after)))
ORDER BY logged_at DESC, id
LIMIT @lim;

-- name: CreateApiLog :exec
INSERT INTO api_logs (organization_id, id, api_key_id, client, http_method, http_status, request_origin, request_path, request_body, request_response, api_version, logged_at, created_at)
VALUES (@organization_id, @id, @api_key_id, @client, @http_method, @http_status, @request_origin, @request_path, sqlc.narg(request_body), sqlc.narg(request_response), @api_version, @logged_at, @created_at)
ON CONFLICT DO NOTHING;

-- name: GetApiLog :one
SELECT * FROM api_logs WHERE organization_id = @organization_id AND id = @id;

-- name: ListApiLogs :many
-- @succeeded keeps statuses up to 399 and @failed from 400 (lago
-- api_logs_query.rb with_http_statuses); they take precedence over
-- @http_statuses. @request_paths are LIKE patterns.
SELECT * FROM api_logs
WHERE organization_id = @organization_id
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR logged_at >= sqlc.narg(from_at))
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR logged_at <= sqlc.narg(to_at))
  AND (cardinality(@ids::uuid[]) = 0 OR id = ANY(@ids::uuid[]))
  AND (cardinality(@api_key_ids::uuid[]) = 0 OR api_key_id = ANY(@api_key_ids::uuid[]))
  AND (cardinality(@http_methods::text[]) = 0 OR http_method = ANY(@http_methods::text[]))
  AND (CASE WHEN @succeeded::boolean OR @failed::boolean
            THEN (NOT @succeeded::boolean OR http_status <= 399) AND (NOT @failed::boolean OR http_status > 399)
            ELSE cardinality(@http_statuses::integer[]) = 0 OR http_status = ANY(@http_statuses::integer[]) END)
  AND (sqlc.narg(api_version)::text IS NULL OR api_version = sqlc.narg(api_version))
  AND (cardinality(@request_paths::text[]) = 0 OR request_path LIKE ANY(@request_paths::text[]))
  AND (cardinality(@clients::text[]) = 0 OR client = ANY(@clients::text[]))
  AND (sqlc.narg(after)::uuid IS NULL
       OR logged_at < sqlc.narg(after_at)::timestamptz
       OR (logged_at = sqlc.narg(after_at)::timestamptz AND id > sqlc.narg(after)))
ORDER BY logged_at DESC, id
LIMIT @lim;

-- name: CreateActivityLog :exec
INSERT INTO activity_logs (organization_id, id, user_id, api_key_id, activity_type, activity_source, activity_object, activity_object_changes, resource_id, resource_type, external_customer_id, external_subscription_id, logged_at, created_at)
VALUES (@organization_id, @id, sqlc.narg(user_id), sqlc.narg(api_key_id), @activity_type, @activity_source, sqlc.narg(activity_object), sqlc.narg(activity_object_changes), @resource_id, @resource_type, sqlc.narg(external_customer_id), sqlc.narg(external_subscription_id), @logged_at, @created_at);

-- name: GetActivityLog :one
SELECT * FROM activity_logs WHERE organization_id = @organization_id AND id = @id;

-- name: ListActivityLogs :many
SELECT * FROM activity_logs
WHERE organization_id = @organization_id
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR logged_at >= sqlc.narg(from_at))
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR logged_at <= sqlc.narg(to_at))
  AND (cardinality(@ids::uuid[]) = 0 OR id = ANY(@ids::uuid[]))
  AND (cardinality(@activity_types::text[]) = 0 OR activity_type = ANY(@activity_types::text[]))
  AND (cardinality(@activity_sources::text[]) = 0 OR activity_source = ANY(@activity_sources::text[]))
  AND (cardinality(@user_ids::uuid[]) = 0 OR user_id = ANY(@user_ids::uuid[]))
  AND (cardinality(@api_key_ids::uuid[]) = 0 OR api_key_id = ANY(@api_key_ids::uuid[]))
  AND (cardinality(@resource_ids::uuid[]) = 0 OR resource_id = ANY(@resource_ids::uuid[]))
  AND (cardinality(@resource_types::text[]) = 0 OR resource_type = ANY(@resource_types::text[]))
  AND (sqlc.narg(external_customer_id)::text IS NULL OR external_customer_id = sqlc.narg(external_customer_id))
  AND (sqlc.narg(external_subscription_id)::text IS NULL OR external_subscription_id = sqlc.narg(external_subscription_id))
  AND (sqlc.narg(after)::uuid IS NULL
       OR logged_at < sqlc.narg(after_at)::timestamptz
       OR (logged_at = sqlc.narg(after_at)::timestamptz AND id > sqlc.narg(after)))
ORDER BY logged_at DESC, id
LIMIT @lim;

-- Moving the logs to a log archive (auditlog.Archive): the oldest
-- entries of every organization not claimed by another mover, then their
-- deletion once archived, by "organization_id/id" keys.

-- name: ClaimSecurityLogs :many
SELECT * FROM security_logs ORDER BY created_at LIMIT @lim FOR UPDATE SKIP LOCKED;

-- name: DeleteSecurityLogs :exec
DELETE FROM security_logs
WHERE organization_id::text || '/' || id::text = ANY(@keys::text[]);

-- name: ClaimApiLogs :many
SELECT * FROM api_logs ORDER BY created_at LIMIT @lim FOR UPDATE SKIP LOCKED;

-- name: DeleteApiLogs :exec
DELETE FROM api_logs
WHERE organization_id::text || '/' || id::text = ANY(@keys::text[]);

-- name: ClaimActivityLogs :many
SELECT * FROM activity_logs ORDER BY created_at LIMIT @lim FOR UPDATE SKIP LOCKED;

-- name: DeleteActivityLogs :exec
DELETE FROM activity_logs
WHERE organization_id::text || '/' || id::text = ANY(@keys::text[]);
