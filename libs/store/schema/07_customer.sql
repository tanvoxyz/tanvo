-- Customers (domain §6), with their taxes and invoice custom sections in
-- order. The foreign key of applied_dunning_campaign_id is added in
-- 09_dunning.sql.

CREATE TABLE customers (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  external_id text NOT NULL,
  billing_entity_id uuid NOT NULL,
  customer_type text,
  account_type text NOT NULL,
  name text,
  firstname text,
  lastname text,
  legal_name text,
  legal_number text,
  tax_identification_number text,
  email text,
  phone text,
  url text,
  logo_url text,
  billing_address_line1 text NOT NULL DEFAULT '',
  billing_address_line2 text NOT NULL DEFAULT '',
  billing_address_city text NOT NULL DEFAULT '',
  billing_address_state text NOT NULL DEFAULT '',
  billing_address_zipcode text NOT NULL DEFAULT '',
  billing_address_country text NOT NULL DEFAULT '',
  shipping_address_line1 text NOT NULL DEFAULT '',
  shipping_address_line2 text NOT NULL DEFAULT '',
  shipping_address_city text NOT NULL DEFAULT '',
  shipping_address_state text NOT NULL DEFAULT '',
  shipping_address_zipcode text NOT NULL DEFAULT '',
  shipping_address_country text NOT NULL DEFAULT '',
  currency text,
  timezone text,
  document_locale text,
  invoice_grace_period_days integer,
  net_payment_term_days integer,
  finalize_zero_amount_invoice text NOT NULL,
  issuing_date_anchor text,
  issuing_date_adjustment text,
  skip_invoice_custom_sections boolean NOT NULL DEFAULT false,
  -- References dunning_campaigns; the foreign key is added in 09_dunning.sql.
  applied_dunning_campaign_id uuid,
  exclude_from_dunning_campaign boolean NOT NULL DEFAULT false,
  -- Attempts per currency code: {"EUR": 2}.
  dunning_campaign_attempts jsonb NOT NULL DEFAULT '{}',
  last_dunning_campaign_attempt_at timestamptz,
  sequential_id bigint NOT NULL DEFAULT 0,
  slug text NOT NULL DEFAULT '',
  payment_receipt_counter bigint NOT NULL DEFAULT 0,
  metadata jsonb NOT NULL DEFAULT '[]',
  -- Set while the ongoing balances of the customer's wallets await a
  -- refresh; wallet_refresh_generation grows when a flag is raised, so
  -- that a refresh clears only the flag it observed, and
  -- wallet_refresh_claimed is the generation the last refresh started
  -- from: a signal while the flag is raised and unclaimed writes nothing.
  awaiting_wallet_refresh boolean NOT NULL DEFAULT false,
  wallet_refresh_generation bigint NOT NULL DEFAULT 0,
  wallet_refresh_claimed bigint NOT NULL DEFAULT 0,
  deleted_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT customers_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT customers_billing_entity_fkey FOREIGN KEY (organization_id, billing_entity_id) REFERENCES billing_entities (organization_id, id),
  CONSTRAINT customers_customer_type_check CHECK (customer_type IN ('company', 'individual')),
  CONSTRAINT customers_account_type_check CHECK (account_type IN ('customer', 'partner')),
  CONSTRAINT customers_finalize_zero_amount_invoice_check CHECK (finalize_zero_amount_invoice IN ('inherit', 'skip', 'finalize')),
  CONSTRAINT customers_issuing_date_anchor_check CHECK (issuing_date_anchor IN ('current_period_end', 'next_period_start')),
  CONSTRAINT customers_issuing_date_adjustment_check CHECK (issuing_date_adjustment IN ('keep_anchor', 'align_with_finalization_date')),
  CONSTRAINT customers_invoice_grace_period_days_check CHECK (invoice_grace_period_days >= 0),
  CONSTRAINT customers_net_payment_term_days_check CHECK (net_payment_term_days >= 0),
  CONSTRAINT customers_dunning_campaign_attempts_check CHECK (jsonb_typeof(dunning_campaign_attempts) = 'object'),
  CONSTRAINT customers_metadata_check CHECK (jsonb_typeof(metadata) = 'array')
);

-- External IDs are unique among non-deleted customers.
CREATE UNIQUE INDEX customers_external_id_live_key ON customers (organization_id, external_id) WHERE deleted_at IS NULL;
CREATE INDEX customers_billing_entity_idx ON customers (organization_id, billing_entity_id);
CREATE INDEX customers_awaiting_wallet_refresh_idx ON customers (organization_id, id) WHERE awaiting_wallet_refresh;
CREATE INDEX customers_applied_dunning_campaign_idx ON customers (organization_id, applied_dunning_campaign_id) WHERE applied_dunning_campaign_id IS NOT NULL;

ALTER TABLE customers ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON customers
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The customer's taxes (domain §22.10, step 7), in their order. Deleting a
-- tax removes it from its customers.
CREATE TABLE customer_taxes (
  organization_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  tax_id uuid NOT NULL,
  position integer NOT NULL,
  PRIMARY KEY (organization_id, customer_id, tax_id),
  CONSTRAINT customer_taxes_customer_fkey FOREIGN KEY (organization_id, customer_id) REFERENCES customers (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT customer_taxes_tax_fkey FOREIGN KEY (organization_id, tax_id) REFERENCES taxes (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX customer_taxes_tax_idx ON customer_taxes (organization_id, tax_id);

ALTER TABLE customer_taxes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON customer_taxes
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The invoice custom sections of a customer (domain §11.9), in their
-- order: the manually selected ones (system = false) and the
-- system-generated ones attached by features (system = true). Deleting a
-- section removes it from its customers.
CREATE TABLE customer_invoice_custom_sections (
  organization_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  system boolean NOT NULL,
  invoice_custom_section_id uuid NOT NULL,
  position integer NOT NULL,
  PRIMARY KEY (organization_id, customer_id, system, invoice_custom_section_id),
  CONSTRAINT customer_invoice_custom_sections_customer_fkey FOREIGN KEY (organization_id, customer_id) REFERENCES customers (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT customer_invoice_custom_sections_section_fkey FOREIGN KEY (organization_id, invoice_custom_section_id) REFERENCES invoice_custom_sections (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX customer_invoice_custom_sections_section_idx ON customer_invoice_custom_sections (organization_id, invoice_custom_section_id);

ALTER TABLE customer_invoice_custom_sections ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON customer_invoice_custom_sections
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The VIES check of a customer's VAT number waiting to be (re)tried
-- (domain §22.10): at most one per customer, kept while the customer's EU
-- tax is undecided and removed when the check completes or is no longer
-- needed. last_error_type is the tax.VIESErrorType of the last failure.
CREATE TABLE pending_vies_checks (
  organization_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  billing_entity_id uuid NOT NULL,
  tax_identification_number text,
  attempts_count integer NOT NULL DEFAULT 0,
  last_attempt_at timestamptz,
  last_error_type text,
  last_error_message text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, customer_id),
  CONSTRAINT pending_vies_checks_customer_fkey FOREIGN KEY (organization_id, customer_id) REFERENCES customers (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT pending_vies_checks_billing_entity_fkey FOREIGN KEY (organization_id, billing_entity_id) REFERENCES billing_entities (organization_id, id),
  CONSTRAINT pending_vies_checks_attempts_count_check CHECK (attempts_count >= 0),
  CONSTRAINT pending_vies_checks_last_error_type_check CHECK (last_error_type IN ('rate_limit', 'timeout', 'blocked', 'invalid_requester', 'service_unavailable', 'member_state_unavailable', 'unknown'))
);

CREATE INDEX pending_vies_checks_billing_entity_idx ON pending_vies_checks (organization_id, billing_entity_id);

ALTER TABLE pending_vies_checks ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pending_vies_checks
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
