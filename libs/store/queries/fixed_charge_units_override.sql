-- name: CreateFixedChargeUnitsOverride :execrows
INSERT INTO fixed_charge_units_overrides (
  organization_id, id, subscription_id, fixed_charge_id, units, created_at, updated_at, deleted_at
) VALUES (
  @organization_id, @id, @subscription_id, @fixed_charge_id, @units, @created_at, @updated_at, @deleted_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateFixedChargeUnitsOverride :execrows
UPDATE fixed_charge_units_overrides SET
  subscription_id = @subscription_id, fixed_charge_id = @fixed_charge_id, units = @units,
  created_at = @created_at, updated_at = @updated_at, deleted_at = @deleted_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DiscardFixedChargeUnitsOverride :execrows
UPDATE fixed_charge_units_overrides SET deleted_at = @deleted_at, updated_at = @deleted_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: ListFixedChargeUnitsOverridesBySubscription :many
SELECT * FROM fixed_charge_units_overrides
WHERE organization_id = @organization_id AND subscription_id = @subscription_id AND deleted_at IS NULL
ORDER BY id;

-- name: ListFixedChargeUnitsOverridesByFixedCharge :many
SELECT * FROM fixed_charge_units_overrides
WHERE organization_id = @organization_id AND fixed_charge_id = @fixed_charge_id AND deleted_at IS NULL
ORDER BY id;
