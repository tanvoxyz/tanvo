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
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- Discarded payment providers (deleted_at) are hidden from every read
-- (base_provider.rb:11, default_scope kept).

-- name: GetIntegration :one
SELECT * FROM integrations WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: GetIntegrationByCode :one
SELECT * FROM integrations WHERE organization_id = @organization_id AND code = @code AND deleted_at IS NULL;

-- name: GetDefaultIntegration :one
SELECT * FROM integrations
WHERE organization_id = @organization_id AND category = @category AND is_default AND deleted_at IS NULL;

-- name: ListIntegrationsByProvider :many
SELECT * FROM integrations
WHERE organization_id = @organization_id AND provider = @provider AND deleted_at IS NULL
ORDER BY id;

-- ListIntegrationsByDomain is cross-organization (single sign-on routing):
-- the auth integrations of a provider claiming an email domain, as the
-- primary domain or one of the additional domains (stored lower-case).
-- name: ListIntegrationsByDomain :many
SELECT * FROM integrations
WHERE category = 'auth' AND provider = @provider AND deleted_at IS NULL
  AND (lower(settings->>'domain') = lower(@domain::text)
    OR settings->'additional_domains' @> jsonb_build_array(lower(@domain::text)))
ORDER BY id;

-- name: ListIntegrations :many
SELECT * FROM integrations
WHERE organization_id = @organization_id AND deleted_at IS NULL
  AND (sqlc.narg(category)::text IS NULL OR category = sqlc.narg(category))
  AND (sqlc.narg(provider)::text IS NULL OR provider = sqlc.narg(provider))
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;

-- name: DeleteIntegration :execrows
DELETE FROM integrations WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DiscardIntegration :one
-- Discards a payment provider with its live provider customers
-- (payment_providers/destroy_service.rb:19-22).
WITH discarded AS (
  UPDATE integrations SET deleted_at = sqlc.arg(now)::timestamptz, updated_at = sqlc.arg(now)::timestamptz
  WHERE integrations.organization_id = @organization_id AND integrations.id = @id AND integrations.deleted_at IS NULL
  RETURNING integrations.organization_id, integrations.id
), customers AS (
  UPDATE payment_provider_customers c SET deleted_at = sqlc.arg(now)::timestamptz, updated_at = sqlc.arg(now)::timestamptz
  FROM discarded d
  WHERE c.organization_id = d.organization_id AND c.integration_id = d.id AND c.deleted_at IS NULL
  RETURNING c.id
)
SELECT count(*)::bigint AS discarded FROM discarded;

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
  AND (sqlc.narg(search)::text IS NULL OR external_id ILIKE sqlc.narg(search) OR external_name ILIKE sqlc.narg(search)
       OR external_account_code ILIKE sqlc.narg(search))
  -- By external_name (byte order, NULLs last), then by created_at, newest
  -- first, then by id (integration_items_query.rb:10, base_query.rb:111);
  -- the cursor is (@after_name, @after_at, @after), @after_name NULL for
  -- an item without a name.
  AND (sqlc.narg(after)::uuid IS NULL
       OR (sqlc.narg(after_name)::text IS NOT NULL
           AND (external_name IS NULL OR external_name COLLATE "C" > sqlc.narg(after_name)::text COLLATE "C"))
       OR (external_name IS NOT DISTINCT FROM sqlc.narg(after_name)::text
           AND (created_at < sqlc.narg(after_at)::timestamptz
                OR (created_at = sqlc.narg(after_at)::timestamptz AND id > sqlc.narg(after)))))
ORDER BY external_name COLLATE "C" NULLS LAST, created_at DESC, id
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

-- name: IntegrationResourceExistsForSyncable :one
SELECT EXISTS (
  SELECT 1 FROM integration_resources WHERE organization_id = @organization_id AND syncable_id = @syncable_id
);

-- name: DeleteIntegrationResource :execrows
DELETE FROM integration_resources WHERE organization_id = @organization_id AND id = @id;
