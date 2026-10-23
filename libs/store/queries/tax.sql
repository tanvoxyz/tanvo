-- Taxes.

-- name: CreateTax :execrows
INSERT INTO taxes (
  organization_id, id, code, name, rate, description, auto_generated, created_at, updated_at
) VALUES (
  @organization_id, @id, @code, @name, @rate, @description, @auto_generated, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateTax :execrows
UPDATE taxes SET
  organization_id = @organization_id, id = @id, code = @code, name = @name, rate = @rate,
  description = @description, auto_generated = @auto_generated, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteTax :execrows
DELETE FROM taxes WHERE organization_id = @organization_id AND id = @id;

-- name: GetTax :one
SELECT * FROM taxes WHERE organization_id = @organization_id AND id = @id;

-- name: GetTaxByCode :one
SELECT * FROM taxes WHERE organization_id = @organization_id AND code = @code;

-- name: ListTaxesByCodes :many
SELECT * FROM taxes
WHERE organization_id = @organization_id AND code = ANY(@codes::text[])
ORDER BY id;

-- name: ListTaxesByIDs :many
SELECT * FROM taxes
WHERE organization_id = @organization_id AND id = ANY(@ids::uuid[])
ORDER BY id;

-- name: ListTaxes :many
SELECT * FROM taxes
WHERE organization_id = @organization_id
  AND (sqlc.narg(auto_generated)::boolean IS NULL OR auto_generated = sqlc.narg(auto_generated))
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;
