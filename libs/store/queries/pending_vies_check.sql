-- name: GetPendingViesCheck :one
SELECT * FROM pending_vies_checks
WHERE organization_id = @organization_id AND customer_id = @customer_id;

-- name: UpsertPendingViesCheck :exec
INSERT INTO pending_vies_checks (
  organization_id, customer_id, billing_entity_id, tax_identification_number, attempts_count,
  last_attempt_at, last_error_type, last_error_message, created_at, updated_at
) VALUES (
  @organization_id, @customer_id, @billing_entity_id, sqlc.narg(tax_identification_number), @attempts_count,
  sqlc.narg(last_attempt_at), sqlc.narg(last_error_type), sqlc.narg(last_error_message), @created_at, @updated_at
)
ON CONFLICT (organization_id, customer_id) DO UPDATE SET
  billing_entity_id = EXCLUDED.billing_entity_id,
  tax_identification_number = EXCLUDED.tax_identification_number,
  attempts_count = EXCLUDED.attempts_count,
  last_attempt_at = EXCLUDED.last_attempt_at,
  last_error_type = EXCLUDED.last_error_type,
  last_error_message = EXCLUDED.last_error_message,
  updated_at = EXCLUDED.updated_at;

-- name: DeletePendingViesCheck :execrows
DELETE FROM pending_vies_checks
WHERE organization_id = @organization_id AND customer_id = @customer_id;
