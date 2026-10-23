-- Usage attribution types and values (schema/24_usage_attribution.sql).

-- name: CreateUsageAttributionType :execrows
INSERT INTO usage_attribution_types (
  organization_id, id, code, name, description, attribution_keys, role, parent_id, created_at, updated_at, deleted_at
) VALUES (
  @organization_id, @id, @code, @name, @description, @attribution_keys, @role, @parent_id, @created_at, @updated_at, @deleted_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateUsageAttributionType :execrows
UPDATE usage_attribution_types SET
  code = @code, name = @name, description = @description, attribution_keys = @attribution_keys, role = @role,
  parent_id = @parent_id, created_at = @created_at, updated_at = @updated_at, deleted_at = @deleted_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetUsageAttributionType :one
SELECT * FROM usage_attribution_types
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: GetUsageAttributionTypeByCode :one
SELECT * FROM usage_attribution_types
WHERE organization_id = @organization_id AND code = @code AND deleted_at IS NULL;

-- name: ListUsageAttributionTypesByIDs :many
-- Deleted types included: a child still names its deleted parent.
SELECT * FROM usage_attribution_types
WHERE organization_id = @organization_id AND id = ANY(@ids::uuid[])
ORDER BY id;

-- name: ListUsageAttributionTypes :many
-- By created_at, newest first, then by id (base_query.rb:111-112); the
-- cursor is (@after_at, @after). @root keeps the types without a kept
-- parent (usage_attribution_types_query.rb:49-54), NOT @root the others.
SELECT t.* FROM usage_attribution_types t
WHERE t.organization_id = @organization_id AND t.deleted_at IS NULL
  AND (sqlc.narg(role)::text IS NULL OR t.role = sqlc.narg(role))
  AND (sqlc.narg(root)::boolean IS NULL OR sqlc.narg(root)::boolean = NOT EXISTS (
    SELECT 1 FROM usage_attribution_types p
    WHERE p.organization_id = t.organization_id AND p.id = t.parent_id AND p.deleted_at IS NULL
  ))
  AND (sqlc.narg(search)::text IS NULL OR t.name ILIKE sqlc.narg(search) OR t.code ILIKE sqlc.narg(search))
  AND (sqlc.narg(after)::uuid IS NULL
       OR t.created_at < sqlc.narg(after_at)::timestamptz
       OR (t.created_at = sqlc.narg(after_at)::timestamptz AND t.id > sqlc.narg(after)))
ORDER BY t.created_at DESC, t.id
LIMIT @lim;

-- name: UsageAttributionKeysTaken :one
SELECT EXISTS (
  SELECT 1 FROM usage_attribution_types
  WHERE organization_id = @organization_id AND deleted_at IS NULL AND id <> @except_id
    AND attribution_keys && @keys::text[]
) AS taken;

-- name: UsageAttributionTypeHasChildren :one
SELECT EXISTS (
  SELECT 1 FROM usage_attribution_types
  WHERE organization_id = @organization_id AND deleted_at IS NULL AND parent_id = @id
) AS has_children;

-- name: DeleteUsageAttributionType :execrows
UPDATE usage_attribution_types SET deleted_at = @deleted_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: CreateUsageAttributionValue :execrows
INSERT INTO usage_attribution_values (
  organization_id, id, usage_attribution_type_id, customer_id, parent_id, value, last_seen_at, created_at, updated_at, deleted_at
) VALUES (
  @organization_id, @id, @usage_attribution_type_id, @customer_id, @parent_id, @value, @last_seen_at, @created_at, @updated_at, @deleted_at
)
ON CONFLICT DO NOTHING;

-- name: GetUsageAttributionValue :one
SELECT * FROM usage_attribution_values WHERE organization_id = @organization_id AND id = @id;

-- name: UsageAttributionValuesExist :one
-- Deleted values included (update_service.rb:66).
SELECT EXISTS (
  SELECT 1 FROM usage_attribution_values
  WHERE organization_id = @organization_id AND usage_attribution_type_id = @usage_attribution_type_id
) AS exist;

-- name: DeleteUsageAttributionValues :execrows
UPDATE usage_attribution_values SET deleted_at = @deleted_at
WHERE organization_id = @organization_id AND usage_attribution_type_id = @usage_attribution_type_id AND deleted_at IS NULL;
