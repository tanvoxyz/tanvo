-- Idempotency keys (idempotency.Repository).

-- name: ClaimIdempotencyKey :execrows
-- Inserts a pending record, or replaces one expired at @created_at.
INSERT INTO idempotency_keys (organization_id, key, procedure, request_hash, response, created_at, expires_at)
VALUES (@organization_id, @key, @procedure, @request_hash, NULL, @created_at, @expires_at)
ON CONFLICT (organization_id, key) DO UPDATE SET
  procedure = EXCLUDED.procedure, request_hash = EXCLUDED.request_hash, response = NULL,
  created_at = EXCLUDED.created_at, expires_at = EXCLUDED.expires_at
WHERE idempotency_keys.expires_at <= EXCLUDED.created_at;

-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys WHERE organization_id = @organization_id AND key = @key;

-- name: CompleteIdempotencyKey :execrows
UPDATE idempotency_keys SET response = @response, expires_at = @expires_at
WHERE organization_id = @organization_id AND key = @key;

-- name: ReleaseIdempotencyKey :exec
DELETE FROM idempotency_keys
WHERE organization_id = @organization_id AND key = @key AND response IS NULL;

-- name: PurgeIdempotencyKeys :execrows
DELETE FROM idempotency_keys d
WHERE d.organization_id = @organization_id AND d.key = ANY(ARRAY(
  SELECT k.key FROM idempotency_keys k
  WHERE k.organization_id = @organization_id AND k.expires_at <= @now
  ORDER BY k.key
  LIMIT @lim
));

-- name: ListIdempotencyKeyOrganizations :many
SELECT DISTINCT organization_id FROM idempotency_keys WHERE expires_at <= @now ORDER BY organization_id;
