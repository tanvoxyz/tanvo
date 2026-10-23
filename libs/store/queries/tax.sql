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
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DiscardTax :execrows
UPDATE taxes SET deleted_at = @deleted_at, updated_at = @deleted_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DetachTax :exec
-- Deletes the join rows of the tax's owners (lago
-- Taxes::DestroyService).
WITH be AS (
  DELETE FROM billing_entity_taxes x1 WHERE x1.organization_id = @organization_id AND x1.tax_id = @tax_id
), cu AS (
  DELETE FROM customer_taxes x2 WHERE x2.organization_id = @organization_id AND x2.tax_id = @tax_id
), pl AS (
  DELETE FROM plan_taxes x3 WHERE x3.organization_id = @organization_id AND x3.tax_id = @tax_id
), ch AS (
  DELETE FROM charge_taxes x4 WHERE x4.organization_id = @organization_id AND x4.tax_id = @tax_id
), fc AS (
  DELETE FROM fixed_charge_taxes x5 WHERE x5.organization_id = @organization_id AND x5.tax_id = @tax_id
), ao AS (
  DELETE FROM add_on_taxes x6 WHERE x6.organization_id = @organization_id AND x6.tax_id = @tax_id
), rc AS (
  DELETE FROM rate_card_taxes x8 WHERE x8.organization_id = @organization_id AND x8.tax_id = @tax_id
)
DELETE FROM commitment_taxes x7 WHERE x7.organization_id = @organization_id AND x7.tax_id = @tax_id;

-- name: GetTax :one
-- Kept or discarded.
SELECT * FROM taxes WHERE organization_id = @organization_id AND id = @id;

-- name: GetTaxByCode :one
SELECT * FROM taxes WHERE organization_id = @organization_id AND code = @code AND deleted_at IS NULL;

-- name: ListTaxesByCodes :many
SELECT * FROM taxes
WHERE organization_id = @organization_id AND code = ANY(@codes::text[]) AND deleted_at IS NULL
ORDER BY id;

-- name: ListTaxesByIDs :many
SELECT * FROM taxes
WHERE organization_id = @organization_id AND id = ANY(@ids::uuid[]) AND deleted_at IS NULL
ORDER BY id;

-- name: ListTaxes :many
-- @by_name lists by name (byte order), then by created_at, newest first,
-- then by id (taxes_query.rb, base_query.rb:111-112); its cursor is
-- (@after_name, @after_at, @after).
SELECT * FROM taxes
WHERE taxes.organization_id = @organization_id AND taxes.deleted_at IS NULL
  AND (sqlc.narg(auto_generated)::boolean IS NULL OR auto_generated = sqlc.narg(auto_generated))
  AND (sqlc.narg(applied_to_organization)::boolean IS NULL
       OR (taxes.id IN (SELECT bt.tax_id FROM billing_entity_taxes bt
                  WHERE bt.organization_id = @organization_id
                    AND bt.billing_entity_id IN (SELECT b.id FROM billing_entities b
                                                 WHERE b.organization_id = @organization_id AND b.is_default)))
          = sqlc.narg(applied_to_organization))
  AND (sqlc.narg(search)::text IS NULL OR name ILIKE sqlc.narg(search) OR code ILIKE sqlc.narg(search))
  AND (sqlc.narg(after)::uuid IS NULL
       OR (@by_name::boolean AND (name COLLATE "C" > sqlc.narg(after_name)::text COLLATE "C"
                                  OR (name = sqlc.narg(after_name)::text
                                      AND (created_at < sqlc.narg(after_at)::timestamptz
                                           OR (created_at = sqlc.narg(after_at)::timestamptz AND id > sqlc.narg(after))))))
       OR (NOT @by_name::boolean AND id > sqlc.narg(after)))
ORDER BY CASE WHEN @by_name::boolean THEN name COLLATE "C" END,
  CASE WHEN @by_name::boolean THEN created_at END DESC, id
LIMIT @lim;
