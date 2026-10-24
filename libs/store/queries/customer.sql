-- Customers and their taxes and invoice custom sections.

-- name: CreateCustomer :execrows
INSERT INTO customers (
  organization_id, id, external_id, billing_entity_id, customer_type, account_type,
  name, firstname, lastname, legal_name, legal_number, tax_identification_number,
  email, phone, url, logo_url,
  billing_address_line1, billing_address_line2, billing_address_city, billing_address_state, billing_address_zipcode, billing_address_country,
  shipping_address_line1, shipping_address_line2, shipping_address_city, shipping_address_state, shipping_address_zipcode, shipping_address_country,
  currency, timezone, document_locale, invoice_grace_period_days, net_payment_term_days,
  finalize_zero_amount_invoice, issuing_date_anchor, issuing_date_adjustment, skip_invoice_custom_sections,
  applied_dunning_campaign_id, exclude_from_dunning_campaign, dunning_campaign_attempts, last_dunning_campaign_attempt_at,
  sequential_id, slug, payment_receipt_counter, metadata, x402_agent_address, deleted_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @external_id, @billing_entity_id, @customer_type, @account_type,
  @name, @firstname, @lastname, @legal_name, @legal_number, @tax_identification_number,
  @email, @phone, @url, @logo_url,
  @billing_address_line1, @billing_address_line2, @billing_address_city, @billing_address_state, @billing_address_zipcode, @billing_address_country,
  @shipping_address_line1, @shipping_address_line2, @shipping_address_city, @shipping_address_state, @shipping_address_zipcode, @shipping_address_country,
  @currency, @timezone, @document_locale, @invoice_grace_period_days, @net_payment_term_days,
  @finalize_zero_amount_invoice, @issuing_date_anchor, @issuing_date_adjustment, @skip_invoice_custom_sections,
  @applied_dunning_campaign_id, @exclude_from_dunning_campaign, @dunning_campaign_attempts, @last_dunning_campaign_attempt_at,
  @sequential_id, @slug, @payment_receipt_counter, @metadata, @x402_agent_address, @deleted_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateCustomer :execrows
UPDATE customers SET
  -- The key columns are assigned to themselves first so that the parameters
  -- follow the order of CreateCustomer (see repo_customer.go).
  organization_id = @organization_id, id = @id, external_id = @external_id, billing_entity_id = @billing_entity_id, customer_type = @customer_type, account_type = @account_type,
  name = @name, firstname = @firstname, lastname = @lastname, legal_name = @legal_name, legal_number = @legal_number, tax_identification_number = @tax_identification_number,
  email = @email, phone = @phone, url = @url, logo_url = @logo_url,
  billing_address_line1 = @billing_address_line1, billing_address_line2 = @billing_address_line2, billing_address_city = @billing_address_city, billing_address_state = @billing_address_state, billing_address_zipcode = @billing_address_zipcode, billing_address_country = @billing_address_country,
  shipping_address_line1 = @shipping_address_line1, shipping_address_line2 = @shipping_address_line2, shipping_address_city = @shipping_address_city, shipping_address_state = @shipping_address_state, shipping_address_zipcode = @shipping_address_zipcode, shipping_address_country = @shipping_address_country,
  currency = @currency, timezone = @timezone, document_locale = @document_locale, invoice_grace_period_days = @invoice_grace_period_days, net_payment_term_days = @net_payment_term_days,
  finalize_zero_amount_invoice = @finalize_zero_amount_invoice, issuing_date_anchor = @issuing_date_anchor, issuing_date_adjustment = @issuing_date_adjustment, skip_invoice_custom_sections = @skip_invoice_custom_sections,
  applied_dunning_campaign_id = @applied_dunning_campaign_id, exclude_from_dunning_campaign = @exclude_from_dunning_campaign, dunning_campaign_attempts = @dunning_campaign_attempts, last_dunning_campaign_attempt_at = @last_dunning_campaign_attempt_at,
  sequential_id = @sequential_id, slug = @slug, payment_receipt_counter = @payment_receipt_counter, metadata = @metadata, x402_agent_address = @x402_agent_address, deleted_at = @deleted_at, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetCustomer :one
SELECT sqlc.embed(c),
  ARRAY(SELECT t.tax_id FROM customer_taxes t
        WHERE t.organization_id = c.organization_id AND t.customer_id = c.id ORDER BY t.position)::uuid[] AS tax_ids,
  ARRAY(SELECT s.invoice_custom_section_id FROM customer_invoice_custom_sections s
        WHERE s.organization_id = c.organization_id AND s.customer_id = c.id AND NOT s.system ORDER BY s.position)::uuid[] AS invoice_custom_section_ids,
  ARRAY(SELECT s.invoice_custom_section_id FROM customer_invoice_custom_sections s
        WHERE s.organization_id = c.organization_id AND s.customer_id = c.id AND s.system ORDER BY s.position)::uuid[] AS system_invoice_custom_section_ids
FROM customers c
WHERE c.organization_id = @organization_id AND c.id = @id;

-- name: GetCustomerByExternalID :one
SELECT sqlc.embed(c),
  ARRAY(SELECT t.tax_id FROM customer_taxes t
        WHERE t.organization_id = c.organization_id AND t.customer_id = c.id ORDER BY t.position)::uuid[] AS tax_ids,
  ARRAY(SELECT s.invoice_custom_section_id FROM customer_invoice_custom_sections s
        WHERE s.organization_id = c.organization_id AND s.customer_id = c.id AND NOT s.system ORDER BY s.position)::uuid[] AS invoice_custom_section_ids,
  ARRAY(SELECT s.invoice_custom_section_id FROM customer_invoice_custom_sections s
        WHERE s.organization_id = c.organization_id AND s.customer_id = c.id AND s.system ORDER BY s.position)::uuid[] AS system_invoice_custom_section_ids
FROM customers c
WHERE c.organization_id = @organization_id AND c.external_id = @external_id AND c.deleted_at IS NULL;

-- name: GetCustomerByX402AgentAddress :one
SELECT sqlc.embed(c),
  ARRAY(SELECT t.tax_id FROM customer_taxes t
        WHERE t.organization_id = c.organization_id AND t.customer_id = c.id ORDER BY t.position)::uuid[] AS tax_ids,
  ARRAY(SELECT s.invoice_custom_section_id FROM customer_invoice_custom_sections s
        WHERE s.organization_id = c.organization_id AND s.customer_id = c.id AND NOT s.system ORDER BY s.position)::uuid[] AS invoice_custom_section_ids,
  ARRAY(SELECT s.invoice_custom_section_id FROM customer_invoice_custom_sections s
        WHERE s.organization_id = c.organization_id AND s.customer_id = c.id AND s.system ORDER BY s.position)::uuid[] AS system_invoice_custom_section_ids
FROM customers c
WHERE c.organization_id = @organization_id AND c.x402_agent_address = @x402_agent_address AND c.deleted_at IS NULL;

-- name: ListCustomers :many
-- The customer list of the API (customer.Filter). @recent lists by
-- created_at, newest first, then by id ascending (base_query.rb:111-112);
-- its cursor is (@after_at, @after).
SELECT sqlc.embed(c),
  ARRAY(SELECT t.tax_id FROM customer_taxes t
        WHERE t.organization_id = c.organization_id AND t.customer_id = c.id ORDER BY t.position)::uuid[] AS tax_ids,
  ARRAY(SELECT s.invoice_custom_section_id FROM customer_invoice_custom_sections s
        WHERE s.organization_id = c.organization_id AND s.customer_id = c.id AND NOT s.system ORDER BY s.position)::uuid[] AS invoice_custom_section_ids,
  ARRAY(SELECT s.invoice_custom_section_id FROM customer_invoice_custom_sections s
        WHERE s.organization_id = c.organization_id AND s.customer_id = c.id AND s.system ORDER BY s.position)::uuid[] AS system_invoice_custom_section_ids
FROM customers c
WHERE c.organization_id = @organization_id
  AND (@include_deleted::boolean OR c.deleted_at IS NULL)
  AND (sqlc.narg(external_id)::text IS NULL OR c.external_id = sqlc.narg(external_id))
  AND (cardinality(@billing_entity_ids::uuid[]) = 0 OR c.billing_entity_id = ANY(@billing_entity_ids::uuid[]))
  AND (cardinality(@account_types::text[]) = 0 OR c.account_type = ANY(@account_types::text[]))
  AND (sqlc.narg(customer_type)::text IS NULL OR c.customer_type = sqlc.narg(customer_type))
  AND (sqlc.narg(has_customer_type)::boolean IS NULL OR (c.customer_type IS NOT NULL) = sqlc.narg(has_customer_type))
  AND (cardinality(@countries::text[]) = 0 OR c.billing_address_country = ANY(@countries::text[]))
  AND (cardinality(@states::text[]) = 0 OR c.billing_address_state = ANY(@states::text[]))
  AND (cardinality(@zipcodes::text[]) = 0 OR c.billing_address_zipcode = ANY(@zipcodes::text[]))
  AND (cardinality(@currencies::text[]) = 0 OR c.currency = ANY(@currencies::text[]))
  AND (sqlc.narg(has_tax_identification_number)::boolean IS NULL
       OR (c.tax_identification_number IS NOT NULL) = sqlc.narg(has_tax_identification_number))
  AND c.metadata @> @metadata_present::jsonb
  AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(c.metadata) m WHERE m->>'key' = ANY(@metadata_absent::text[]))
  AND (sqlc.narg(active_subscriptions_min)::integer IS NULL
       OR (SELECT count(*) FROM subscriptions s
           WHERE s.organization_id = c.organization_id AND s.customer_id = c.id AND s.status = 'active')
          BETWEEN sqlc.narg(active_subscriptions_min)::integer AND sqlc.narg(active_subscriptions_max)::integer)
  AND (sqlc.narg(search)::text IS NULL
       OR c.external_id ILIKE sqlc.narg(search) OR c.name ILIKE sqlc.narg(search)
       OR c.legal_name ILIKE sqlc.narg(search) OR c.firstname ILIKE sqlc.narg(search)
       OR c.lastname ILIKE sqlc.narg(search) OR c.email ILIKE sqlc.narg(search))
  AND (sqlc.narg(after)::uuid IS NULL
       OR (@recent::boolean AND (c.created_at < sqlc.narg(after_at)::timestamptz
                                 OR (c.created_at = sqlc.narg(after_at)::timestamptz AND c.id > sqlc.narg(after))))
       OR (NOT @recent::boolean AND c.id > sqlc.narg(after)))
ORDER BY CASE WHEN @recent::boolean THEN c.created_at END DESC, c.id
LIMIT @lim;

-- name: ListCustomersForDunning :many
-- A NULL campaign lists the customers without a campaign of their own.
SELECT sqlc.embed(c),
  ARRAY(SELECT t.tax_id FROM customer_taxes t
        WHERE t.organization_id = c.organization_id AND t.customer_id = c.id ORDER BY t.position)::uuid[] AS tax_ids,
  ARRAY(SELECT s.invoice_custom_section_id FROM customer_invoice_custom_sections s
        WHERE s.organization_id = c.organization_id AND s.customer_id = c.id AND NOT s.system ORDER BY s.position)::uuid[] AS invoice_custom_section_ids,
  ARRAY(SELECT s.invoice_custom_section_id FROM customer_invoice_custom_sections s
        WHERE s.organization_id = c.organization_id AND s.customer_id = c.id AND s.system ORDER BY s.position)::uuid[] AS system_invoice_custom_section_ids
FROM customers c
WHERE c.organization_id = @organization_id
  AND c.deleted_at IS NULL AND NOT c.exclude_from_dunning_campaign
  AND c.applied_dunning_campaign_id IS NOT DISTINCT FROM sqlc.narg(campaign)::uuid
  AND (sqlc.narg(after)::uuid IS NULL OR c.id > sqlc.narg(after))
ORDER BY c.id
LIMIT @lim;

-- name: ResetCustomersDunningCampaign :execrows
UPDATE customers SET
  applied_dunning_campaign_id = NULL, dunning_campaign_attempts = '{}', last_dunning_campaign_attempt_at = NULL
WHERE organization_id = @organization_id AND applied_dunning_campaign_id = @campaign::uuid;

-- name: DeleteCustomerTaxes :exec
DELETE FROM customer_taxes WHERE organization_id = @organization_id AND customer_id = @customer_id;

-- name: InsertCustomerTaxes :exec
INSERT INTO customer_taxes (organization_id, customer_id, tax_id, position)
SELECT @organization_id, @customer_id, t.id, t.ord
FROM unnest(@tax_ids::uuid[]) WITH ORDINALITY AS t(id, ord);

-- name: DeleteCustomerSections :exec
DELETE FROM customer_invoice_custom_sections WHERE organization_id = @organization_id AND customer_id = @customer_id;

-- name: InsertCustomerSections :exec
INSERT INTO customer_invoice_custom_sections (organization_id, customer_id, system, invoice_custom_section_id, position)
SELECT @organization_id, @customer_id, @system::boolean, s.id, s.ord
FROM unnest(@section_ids::uuid[]) WITH ORDINALITY AS s(id, ord);

-- name: FlagCustomerWalletRefresh :exec
UPDATE customers c SET awaiting_wallet_refresh = true, wallet_refresh_generation = c.wallet_refresh_generation + 1
WHERE c.organization_id = @organization_id AND c.id = @id
  AND EXISTS (SELECT 1 FROM wallets w
              WHERE w.organization_id = c.organization_id AND w.customer_id = c.id AND w.status = 'active');

-- name: LockCustomerWalletRefreshPending :one
-- FOR KEY SHARE does not block other signals nor customer updates, only
-- the FOR UPDATE of ClaimCustomerWalletRefresh, which so waits for every
-- signal that skipped its write.
SELECT (awaiting_wallet_refresh AND wallet_refresh_claimed <> wallet_refresh_generation)::boolean AS pending
FROM customers WHERE organization_id = @organization_id AND id = @id
FOR KEY SHARE;

-- name: ClaimCustomerWalletRefresh :one
WITH locked AS (
  SELECT l.id AS locked_id FROM customers l
  WHERE l.organization_id = @organization_id::uuid AND l.id = @id::uuid FOR UPDATE
)
UPDATE customers c SET wallet_refresh_claimed = c.wallet_refresh_generation
FROM locked
WHERE c.organization_id = @organization_id::uuid AND c.id = locked.locked_id
RETURNING c.wallet_refresh_generation;

-- name: ClearCustomerWalletRefresh :exec
UPDATE customers SET awaiting_wallet_refresh = false
WHERE organization_id = @organization_id AND id = @id AND wallet_refresh_generation = @generation;

-- name: ListCustomersAwaitingWalletRefresh :many
SELECT c.id FROM customers c
WHERE c.organization_id = @organization_id AND c.awaiting_wallet_refresh AND c.deleted_at IS NULL
  AND EXISTS (SELECT 1 FROM wallets w
              WHERE w.organization_id = c.organization_id AND w.customer_id = c.id AND w.status = 'active')
  AND (sqlc.narg(after)::uuid IS NULL OR c.id > sqlc.narg(after))
ORDER BY c.id
LIMIT @lim;
