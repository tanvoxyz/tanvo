-- Integrations, integration customers, mappings, items and resources.

-- name: CreateIntegration :execrows
INSERT INTO integrations (
  organization_id, id, code, name, category, provider, is_default, settings, secrets,
  sync_invoices, sync_credit_notes, sync_payments, sync_subscriptions, created_at, updated_at
) VALUES (
  @organization_id, @id, @code, @name, @category, @provider, @is_default, @settings, @secrets,
  @sync_invoices, @sync_credit_notes, @sync_payments, @sync_subscriptions, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateIntegration :execrows
UPDATE integrations SET
  organization_id = @organization_id, id = @id, code = @code, name = @name, category = @category, provider = @provider,
  is_default = @is_default, settings = @settings, secrets = @secrets,
  sync_invoices = @sync_invoices, sync_credit_notes = @sync_credit_notes, sync_payments = @sync_payments,
  sync_subscriptions = @sync_subscriptions, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetIntegration :one
SELECT * FROM integrations WHERE organization_id = @organization_id AND id = @id;

-- name: GetIntegrationByCode :one
SELECT * FROM integrations WHERE organization_id = @organization_id AND code = @code;

-- name: GetDefaultIntegration :one
SELECT * FROM integrations WHERE organization_id = @organization_id AND category = @category AND is_default;

-- name: ListIntegrationsByProvider :many
SELECT * FROM integrations
WHERE organization_id = @organization_id AND provider = @provider
ORDER BY id;

-- ListIntegrationsByDomain is cross-organization (single sign-on routing):
-- the auth integrations of a provider claiming an email domain, as the
-- primary domain or one of the additional domains (stored lower-case).
-- name: ListIntegrationsByDomain :many
SELECT * FROM integrations
WHERE category = 'auth' AND provider = @provider
  AND (lower(settings->>'domain') = lower(@domain::text)
    OR settings->'additional_domains' @> jsonb_build_array(lower(@domain::text)))
ORDER BY id;

-- name: ListIntegrations :many
SELECT * FROM integrations
WHERE organization_id = @organization_id
  AND (sqlc.narg(category)::text IS NULL OR category = sqlc.narg(category))
  AND (sqlc.narg(provider)::text IS NULL OR provider = sqlc.narg(provider))
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;

-- name: DeleteIntegration :execrows
DELETE FROM integrations WHERE organization_id = @organization_id AND id = @id;

-- name: CreateIntegrationCustomer :execrows
INSERT INTO integration_customers (
  organization_id, id, integration_id, customer_id, category, external_customer_id, subsidiary_id,
  sync_with_provider, is_default, settings, created_at, updated_at
) VALUES (
  @organization_id, @id, @integration_id, @customer_id, @category, @external_customer_id, @subsidiary_id,
  @sync_with_provider, @is_default, @settings, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateIntegrationCustomer :execrows
UPDATE integration_customers SET
  organization_id = @organization_id, id = @id, integration_id = @integration_id, customer_id = @customer_id,
  category = @category, external_customer_id = @external_customer_id, subsidiary_id = @subsidiary_id,
  sync_with_provider = @sync_with_provider, is_default = @is_default, settings = @settings,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetIntegrationCustomer :one
SELECT * FROM integration_customers WHERE organization_id = @organization_id AND id = @id;

-- name: ListIntegrationCustomersByCustomer :many
SELECT * FROM integration_customers
WHERE organization_id = @organization_id AND customer_id = @customer_id
  AND (sqlc.narg(category)::text IS NULL OR category = sqlc.narg(category))
ORDER BY id;

-- name: GetIntegrationCustomerByExternalID :one
SELECT * FROM integration_customers
WHERE organization_id = @organization_id AND integration_id = @integration_id AND external_customer_id = @external_customer_id
ORDER BY id
LIMIT 1;

-- name: DeleteIntegrationCustomer :execrows
DELETE FROM integration_customers WHERE organization_id = @organization_id AND id = @id;

-- name: CreateIntegrationMapping :execrows
INSERT INTO integration_mappings (
  organization_id, id, integration_id, mappable_type, mappable_id, billing_entity_id,
  external_id, external_account_code, external_name, created_at, updated_at
) VALUES (
  @organization_id, @id, @integration_id, @mappable_type, @mappable_id, @billing_entity_id,
  @external_id, @external_account_code, @external_name, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateIntegrationMapping :execrows
UPDATE integration_mappings SET
  organization_id = @organization_id, id = @id, integration_id = @integration_id, mappable_type = @mappable_type,
  mappable_id = @mappable_id, billing_entity_id = @billing_entity_id, external_id = @external_id,
  external_account_code = @external_account_code, external_name = @external_name,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetIntegrationMapping :one
SELECT * FROM integration_mappings WHERE organization_id = @organization_id AND id = @id;

-- name: FindIntegrationMapping :one
SELECT * FROM integration_mappings
WHERE organization_id = @organization_id AND integration_id = @integration_id
  AND mappable_type = @mappable_type AND mappable_id = @mappable_id
  AND billing_entity_id IS NOT DISTINCT FROM sqlc.narg(billing_entity_id)::uuid;

-- name: ListIntegrationMappings :many
SELECT * FROM integration_mappings
WHERE organization_id = @organization_id AND integration_id = @integration_id
ORDER BY id;

-- name: DeleteIntegrationMapping :execrows
DELETE FROM integration_mappings WHERE organization_id = @organization_id AND id = @id;

-- name: CreateIntegrationCollectionMapping :execrows
INSERT INTO integration_collection_mappings (
  organization_id, id, integration_id, mapping_type, billing_entity_id, external_id, external_account_code,
  external_name, tax_nexus, tax_type, tax_code, currencies, created_at, updated_at
) VALUES (
  @organization_id, @id, @integration_id, @mapping_type, @billing_entity_id, @external_id, @external_account_code,
  @external_name, @tax_nexus, @tax_type, @tax_code, @currencies, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateIntegrationCollectionMapping :execrows
UPDATE integration_collection_mappings SET
  organization_id = @organization_id, id = @id, integration_id = @integration_id, mapping_type = @mapping_type,
  billing_entity_id = @billing_entity_id, external_id = @external_id, external_account_code = @external_account_code,
  external_name = @external_name, tax_nexus = @tax_nexus, tax_type = @tax_type, tax_code = @tax_code,
  currencies = @currencies, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetIntegrationCollectionMapping :one
SELECT * FROM integration_collection_mappings WHERE organization_id = @organization_id AND id = @id;

-- name: ListIntegrationCollectionMappings :many
SELECT * FROM integration_collection_mappings
WHERE organization_id = @organization_id AND integration_id = @integration_id
  AND (sqlc.narg(mapping_type)::text IS NULL OR mapping_type = sqlc.narg(mapping_type))
ORDER BY id;

-- name: DeleteIntegrationCollectionMapping :execrows
DELETE FROM integration_collection_mappings WHERE organization_id = @organization_id AND id = @id;

-- name: UpsertIntegrationItem :execrows
-- An item already cached under the same (integration, item type, external
-- id) keeps its id and creation instant.
INSERT INTO integration_items (
  organization_id, id, integration_id, item_type, external_id, external_account_code, external_name,
  created_at, updated_at
) VALUES (
  @organization_id, @id, @integration_id, @item_type, @external_id, @external_account_code, @external_name,
  @created_at, @updated_at
)
ON CONFLICT (organization_id, integration_id, item_type, external_id) DO UPDATE SET
  external_account_code = EXCLUDED.external_account_code,
  external_name = EXCLUDED.external_name,
  updated_at = EXCLUDED.updated_at;

-- name: ListIntegrationItems :many
SELECT * FROM integration_items
WHERE organization_id = @organization_id AND integration_id = @integration_id
  AND (sqlc.narg(item_type)::text IS NULL OR item_type = sqlc.narg(item_type))
  AND (sqlc.narg(search)::text IS NULL OR external_id ILIKE sqlc.narg(search) OR external_name ILIKE sqlc.narg(search))
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;

-- name: DeleteIntegrationItemsByType :execrows
DELETE FROM integration_items
WHERE organization_id = @organization_id AND integration_id = @integration_id AND item_type = @item_type;

-- name: CreateIntegrationResource :execrows
INSERT INTO integration_resources (
  organization_id, id, integration_id, resource_type, syncable_id, external_id, created_at, updated_at
) VALUES (
  @organization_id, @id, @integration_id, @resource_type, @syncable_id, @external_id, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: FindIntegrationResource :one
SELECT * FROM integration_resources
WHERE organization_id = @organization_id AND integration_id = @integration_id
  AND resource_type = @resource_type AND syncable_id = @syncable_id;

-- name: DeleteIntegrationResource :execrows
DELETE FROM integration_resources WHERE organization_id = @organization_id AND id = @id;
