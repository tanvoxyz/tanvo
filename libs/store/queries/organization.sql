-- Organizations, billing entities and invoice custom sections.

-- name: CreateOrganization :execrows
INSERT INTO organizations (
  id, name, slug, email, logo,
  address_line1, address_line2, address_city, address_state, address_zipcode, address_country,
  authentication_methods, from_email_enabled, audit_logs_retention_days, max_wallets_per_customer, document_number_prefix, created_at, updated_at
) VALUES (
  @id, @name, @slug, @email, @logo,
  @address_line1, @address_line2, @address_city, @address_state, @address_zipcode, @address_country,
  @authentication_methods, @from_email_enabled, @audit_logs_retention_days, @max_wallets_per_customer, @document_number_prefix, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateOrganization :execrows
UPDATE organizations SET
  -- The key columns are assigned to themselves first so that the parameters
  -- follow the order of CreateOrganization (see repo_organization.go).
  id = @id, name = @name, slug = @slug, email = @email, logo = @logo,
  address_line1 = @address_line1, address_line2 = @address_line2, address_city = @address_city,
  address_state = @address_state, address_zipcode = @address_zipcode, address_country = @address_country,
  authentication_methods = @authentication_methods, from_email_enabled = @from_email_enabled,
  audit_logs_retention_days = @audit_logs_retention_days,
  max_wallets_per_customer = @max_wallets_per_customer, document_number_prefix = @document_number_prefix,
  created_at = @created_at, updated_at = @updated_at
WHERE id = @id;

-- name: GetOrganization :one
SELECT * FROM organizations WHERE id = @id;

-- name: GetOrganizationBySlug :one
SELECT * FROM organizations WHERE slug = @slug;

-- name: CreateBillingEntity :execrows
INSERT INTO billing_entities (
  organization_id, id, code, name, legal_name, legal_number, tax_id, email, phone,
  address_line1, address_line2, address_city, address_state, address_zipcode, address_country,
  logo, timezone, default_currency, document_locale, document_numbering, document_number_prefix,
  invoice_footer, invoice_grace_period_days, net_payment_term_days, finalize_zero_amount_invoice,
  issuing_date_anchor, issuing_date_adjustment, email_settings, eu_tax_management, einvoicing,
  is_default, applied_dunning_campaign_id, archived_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @code, @name, @legal_name, @legal_number, @tax_id, @email, @phone,
  @address_line1, @address_line2, @address_city, @address_state, @address_zipcode, @address_country,
  @logo, @timezone, @default_currency, @document_locale, @document_numbering, @document_number_prefix,
  @invoice_footer, @invoice_grace_period_days, @net_payment_term_days, @finalize_zero_amount_invoice,
  @issuing_date_anchor, @issuing_date_adjustment, @email_settings, @eu_tax_management, @einvoicing,
  @is_default, @applied_dunning_campaign_id, @archived_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateBillingEntity :execrows
UPDATE billing_entities SET
  organization_id = @organization_id, id = @id, code = @code, name = @name, legal_name = @legal_name, legal_number = @legal_number, tax_id = @tax_id,
  email = @email, phone = @phone,
  address_line1 = @address_line1, address_line2 = @address_line2, address_city = @address_city,
  address_state = @address_state, address_zipcode = @address_zipcode, address_country = @address_country,
  logo = @logo, timezone = @timezone, default_currency = @default_currency, document_locale = @document_locale,
  document_numbering = @document_numbering, document_number_prefix = @document_number_prefix,
  invoice_footer = @invoice_footer, invoice_grace_period_days = @invoice_grace_period_days,
  net_payment_term_days = @net_payment_term_days, finalize_zero_amount_invoice = @finalize_zero_amount_invoice,
  issuing_date_anchor = @issuing_date_anchor, issuing_date_adjustment = @issuing_date_adjustment,
  email_settings = @email_settings, eu_tax_management = @eu_tax_management, einvoicing = @einvoicing,
  is_default = @is_default, applied_dunning_campaign_id = @applied_dunning_campaign_id,
  archived_at = @archived_at, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetBillingEntity :one
SELECT sqlc.embed(b),
  ARRAY(SELECT t.tax_id FROM billing_entity_taxes t
        WHERE t.organization_id = b.organization_id AND t.billing_entity_id = b.id ORDER BY t.position)::uuid[] AS tax_ids,
  ARRAY(SELECT s.invoice_custom_section_id FROM billing_entity_invoice_custom_sections s
        WHERE s.organization_id = b.organization_id AND s.billing_entity_id = b.id ORDER BY s.position)::uuid[] AS invoice_custom_section_ids
FROM billing_entities b
WHERE b.organization_id = @organization_id AND b.id = @id;

-- name: GetBillingEntityByCode :one
SELECT sqlc.embed(b),
  ARRAY(SELECT t.tax_id FROM billing_entity_taxes t
        WHERE t.organization_id = b.organization_id AND t.billing_entity_id = b.id ORDER BY t.position)::uuid[] AS tax_ids,
  ARRAY(SELECT s.invoice_custom_section_id FROM billing_entity_invoice_custom_sections s
        WHERE s.organization_id = b.organization_id AND s.billing_entity_id = b.id ORDER BY s.position)::uuid[] AS invoice_custom_section_ids
FROM billing_entities b
WHERE b.organization_id = @organization_id AND b.code = @code AND b.archived_at IS NULL;

-- name: GetDefaultBillingEntity :one
SELECT sqlc.embed(b),
  ARRAY(SELECT t.tax_id FROM billing_entity_taxes t
        WHERE t.organization_id = b.organization_id AND t.billing_entity_id = b.id ORDER BY t.position)::uuid[] AS tax_ids,
  ARRAY(SELECT s.invoice_custom_section_id FROM billing_entity_invoice_custom_sections s
        WHERE s.organization_id = b.organization_id AND s.billing_entity_id = b.id ORDER BY s.position)::uuid[] AS invoice_custom_section_ids
FROM billing_entities b
WHERE b.organization_id = @organization_id AND b.is_default;

-- name: ListBillingEntities :many
SELECT sqlc.embed(b),
  ARRAY(SELECT t.tax_id FROM billing_entity_taxes t
        WHERE t.organization_id = b.organization_id AND t.billing_entity_id = b.id ORDER BY t.position)::uuid[] AS tax_ids,
  ARRAY(SELECT s.invoice_custom_section_id FROM billing_entity_invoice_custom_sections s
        WHERE s.organization_id = b.organization_id AND s.billing_entity_id = b.id ORDER BY s.position)::uuid[] AS invoice_custom_section_ids
FROM billing_entities b
WHERE b.organization_id = @organization_id
  AND (@include_archived::boolean OR b.archived_at IS NULL)
  AND (sqlc.narg(after)::uuid IS NULL OR b.id > sqlc.narg(after))
ORDER BY b.id
LIMIT @lim;

-- name: DeleteBillingEntityTaxes :exec
DELETE FROM billing_entity_taxes WHERE organization_id = @organization_id AND billing_entity_id = @billing_entity_id;

-- name: InsertBillingEntityTaxes :exec
INSERT INTO billing_entity_taxes (organization_id, billing_entity_id, tax_id, position)
SELECT @organization_id, @billing_entity_id, t.id, t.ord
FROM unnest(@tax_ids::uuid[]) WITH ORDINALITY AS t(id, ord);

-- name: DeleteBillingEntitySections :exec
DELETE FROM billing_entity_invoice_custom_sections WHERE organization_id = @organization_id AND billing_entity_id = @billing_entity_id;

-- name: InsertBillingEntitySections :exec
INSERT INTO billing_entity_invoice_custom_sections (organization_id, billing_entity_id, invoice_custom_section_id, position)
SELECT @organization_id, @billing_entity_id, s.id, s.ord
FROM unnest(@section_ids::uuid[]) WITH ORDINALITY AS s(id, ord);

-- name: CreateInvoiceCustomSection :execrows
INSERT INTO invoice_custom_sections (
  organization_id, id, code, name, display_name, details, description, section_type, deleted_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @code, @name, @display_name, @details, @description, @section_type, @deleted_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateInvoiceCustomSection :execrows
UPDATE invoice_custom_sections SET
  organization_id = @organization_id, id = @id, code = @code, name = @name, display_name = @display_name, details = @details,
  description = @description, section_type = @section_type, deleted_at = @deleted_at, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: GetInvoiceCustomSection :one
SELECT * FROM invoice_custom_sections WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: GetInvoiceCustomSectionByCode :one
SELECT * FROM invoice_custom_sections WHERE organization_id = @organization_id AND code = @code AND deleted_at IS NULL;

-- name: ListInvoiceCustomSectionsByIDs :many
SELECT * FROM invoice_custom_sections
WHERE organization_id = @organization_id AND id = ANY(@ids::uuid[]) AND deleted_at IS NULL
ORDER BY id;

-- name: ListInvoiceCustomSections :many
SELECT * FROM invoice_custom_sections
WHERE organization_id = @organization_id AND deleted_at IS NULL
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;

-- name: ListManualInvoiceCustomSections :many
-- Lists the manual sections by name (byte order), then by created_at,
-- newest first, then by id; the cursor is (@after_name, @after_at, @after).
SELECT * FROM invoice_custom_sections
WHERE organization_id = @organization_id AND section_type = 'manual' AND deleted_at IS NULL
  AND (sqlc.narg(after)::uuid IS NULL
       OR name COLLATE "C" > sqlc.narg(after_name)::text COLLATE "C"
       OR (name = sqlc.narg(after_name)::text
           AND (created_at < sqlc.narg(after_at)::timestamptz
                OR (created_at = sqlc.narg(after_at)::timestamptz AND id > sqlc.narg(after)))))
ORDER BY name COLLATE "C", created_at DESC, id
LIMIT @lim;

-- name: DeleteInvoiceCustomSection :execrows
-- Soft-deletes the live section; the Deselect*InvoiceCustomSection queries
-- remove it from the selections.
UPDATE invoice_custom_sections SET deleted_at = @deleted_at, updated_at = @deleted_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DeselectBillingEntitiesInvoiceCustomSection :exec
DELETE FROM billing_entity_invoice_custom_sections
WHERE organization_id = @organization_id AND invoice_custom_section_id = @id;

-- name: DeselectCustomersInvoiceCustomSection :exec
DELETE FROM customer_invoice_custom_sections
WHERE organization_id = @organization_id AND invoice_custom_section_id = @id;

-- name: DeselectSubscriptionsInvoiceCustomSection :exec
DELETE FROM subscription_invoice_custom_sections
WHERE organization_id = @organization_id AND invoice_custom_section_id = @id;
