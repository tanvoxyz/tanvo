-- Commitments, usage thresholds, features with their privileges,
-- entitlements and entitlement removals.

-- name: CreateCommitment :execrows
INSERT INTO commitments (
  organization_id, id, plan_id, commitment_type, amount, invoice_display_name, created_at, updated_at
) VALUES (
  @organization_id, @id, @plan_id, @commitment_type, @amount, @invoice_display_name, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateCommitment :execrows
UPDATE commitments SET
  organization_id = @organization_id, id = @id, plan_id = @plan_id, commitment_type = @commitment_type,
  amount = @amount, invoice_display_name = @invoice_display_name, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteCommitment :execrows
DELETE FROM commitments WHERE organization_id = @organization_id AND id = @id;

-- name: GetCommitment :one
SELECT sqlc.embed(c),
  ARRAY(SELECT t.tax_id FROM commitment_taxes t
        WHERE t.organization_id = c.organization_id AND t.commitment_id = c.id ORDER BY t.position)::uuid[] AS tax_ids
FROM commitments c
WHERE c.organization_id = @organization_id AND c.id = @id;

-- name: GetCommitmentByPlan :one
SELECT sqlc.embed(c),
  ARRAY(SELECT t.tax_id FROM commitment_taxes t
        WHERE t.organization_id = c.organization_id AND t.commitment_id = c.id ORDER BY t.position)::uuid[] AS tax_ids
FROM commitments c
WHERE c.organization_id = @organization_id AND c.plan_id = @plan_id AND c.commitment_type = @commitment_type;

-- name: ListCommitmentsByPlan :many
SELECT sqlc.embed(c),
  ARRAY(SELECT t.tax_id FROM commitment_taxes t
        WHERE t.organization_id = c.organization_id AND t.commitment_id = c.id ORDER BY t.position)::uuid[] AS tax_ids
FROM commitments c
WHERE c.organization_id = @organization_id AND c.plan_id = @plan_id
ORDER BY c.id;

-- name: DeleteCommitmentTaxes :exec
DELETE FROM commitment_taxes WHERE organization_id = @organization_id AND commitment_id = @commitment_id;

-- name: InsertCommitmentTaxes :exec
INSERT INTO commitment_taxes (organization_id, commitment_id, tax_id, position)
SELECT @organization_id, @commitment_id, t.id, t.ord
FROM unnest(@tax_ids::uuid[]) WITH ORDINALITY AS t(id, ord);

-- name: CreateUsageThreshold :execrows
INSERT INTO usage_thresholds (
  organization_id, id, plan_id, subscription_id, amount, recurring, threshold_display_name, created_at, updated_at
) VALUES (
  @organization_id, @id, @plan_id, @subscription_id, @amount, @recurring, @threshold_display_name, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateUsageThreshold :execrows
UPDATE usage_thresholds SET
  organization_id = @organization_id, id = @id, plan_id = @plan_id, subscription_id = @subscription_id,
  amount = @amount, recurring = @recurring, threshold_display_name = @threshold_display_name,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteUsageThreshold :execrows
DELETE FROM usage_thresholds WHERE organization_id = @organization_id AND id = @id;

-- name: GetUsageThreshold :one
SELECT * FROM usage_thresholds WHERE organization_id = @organization_id AND id = @id;

-- name: ListUsageThresholdsByPlan :many
SELECT * FROM usage_thresholds
WHERE organization_id = @organization_id AND plan_id = @plan_id
ORDER BY recurring, amount, id;

-- name: ListUsageThresholdsBySubscription :many
SELECT * FROM usage_thresholds
WHERE organization_id = @organization_id AND subscription_id = @subscription_id
ORDER BY recurring, amount, id;

-- name: CreateFeature :execrows
INSERT INTO features (
  organization_id, id, code, name, description, created_at, updated_at
) VALUES (
  @organization_id, @id, @code, @name, @description, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateFeature :execrows
UPDATE features SET
  organization_id = @organization_id, id = @id, code = @code, name = @name, description = @description,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteFeature :execrows
DELETE FROM features WHERE organization_id = @organization_id AND id = @id;

-- name: GetFeature :one
SELECT * FROM features WHERE organization_id = @organization_id AND id = @id;

-- name: GetFeatureByCode :one
SELECT * FROM features WHERE organization_id = @organization_id AND code = @code;

-- name: ListFeaturesByIDs :many
SELECT * FROM features
WHERE organization_id = @organization_id AND id = ANY(@ids::uuid[])
ORDER BY id;

-- name: ListFeatures :many
SELECT * FROM features
WHERE organization_id = @organization_id
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;

-- name: ListFeaturePrivileges :many
SELECT * FROM feature_privileges
WHERE organization_id = @organization_id AND feature_id = ANY(@feature_ids::uuid[])
ORDER BY feature_id, position;

-- name: DeleteFeaturePrivileges :exec
DELETE FROM feature_privileges WHERE organization_id = @organization_id AND feature_id = @feature_id;

-- name: InsertFeaturePrivilege :exec
INSERT INTO feature_privileges (organization_id, id, feature_id, position, code, name, value_type, select_options)
VALUES (@organization_id, @id, @feature_id, @position, @code, @name, @value_type, @select_options);

-- name: CreateEntitlement :execrows
INSERT INTO entitlements (
  organization_id, id, feature_id, plan_id, subscription_id, privilege_values, created_at, updated_at
) VALUES (
  @organization_id, @id, @feature_id, @plan_id, @subscription_id, @privilege_values, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateEntitlement :execrows
UPDATE entitlements SET
  organization_id = @organization_id, id = @id, feature_id = @feature_id, plan_id = @plan_id,
  subscription_id = @subscription_id, privilege_values = @privilege_values,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteEntitlement :execrows
DELETE FROM entitlements WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteEntitlementsByFeature :execrows
DELETE FROM entitlements WHERE organization_id = @organization_id AND feature_id = @feature_id;

-- name: GetEntitlement :one
SELECT * FROM entitlements WHERE organization_id = @organization_id AND id = @id;

-- name: ListEntitlementsByPlan :many
SELECT * FROM entitlements
WHERE organization_id = @organization_id AND plan_id = @plan_id
ORDER BY id;

-- name: ListEntitlementsBySubscription :many
SELECT * FROM entitlements
WHERE organization_id = @organization_id AND subscription_id = @subscription_id
ORDER BY id;

-- name: CreateEntitlementRemoval :execrows
INSERT INTO entitlement_removals (
  organization_id, id, subscription_id, feature_id, privilege_code, created_at
) VALUES (
  @organization_id, @id, @subscription_id, @feature_id, @privilege_code, @created_at
)
ON CONFLICT DO NOTHING;

-- name: DeleteEntitlementRemoval :execrows
DELETE FROM entitlement_removals WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteEntitlementRemovalsByFeature :execrows
DELETE FROM entitlement_removals WHERE organization_id = @organization_id AND feature_id = @feature_id;

-- name: ListEntitlementRemovalsBySubscription :many
SELECT * FROM entitlement_removals
WHERE organization_id = @organization_id AND subscription_id = @subscription_id
ORDER BY id;
