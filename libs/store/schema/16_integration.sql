-- Integrations, integration customers, mappings, items, resources (domain §17).

-- An integration's code is unique per organization among the live payment
-- providers, and among the other integrations; an integration is the
-- default of its category at most once. Payment providers are discarded
-- (deleted_at) rather than deleted, as payments keep referencing them.
-- Settings are the non-secret provider settings (a JSON object); secrets
-- are the encrypted credentials, opaque to the store.
CREATE TABLE integrations (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  category text NOT NULL,
  provider text NOT NULL,
  is_default boolean NOT NULL DEFAULT false,
  settings jsonb,
  secrets bytea,
  sync_invoices boolean NOT NULL DEFAULT false,
  sync_credit_notes boolean NOT NULL DEFAULT false,
  sync_payments boolean NOT NULL DEFAULT false,
  sync_subscriptions boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT integrations_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT integrations_category_check CHECK (category IN ('payment', 'accounting', 'tax', 'crm', 'auth')),
  CONSTRAINT integrations_provider_check CHECK (provider IN ('stripe', 'adyen', 'gocardless', 'cashfree', 'moneyhash', 'flutterwave', 'netsuite', 'xero', 'anrok', 'avalara', 'hubspot', 'salesforce', 'okta', 'entra_id', 'oidc'))
);

CREATE UNIQUE INDEX integrations_payment_code_key ON integrations (organization_id, code) WHERE category = 'payment' AND deleted_at IS NULL;
CREATE UNIQUE INDEX integrations_other_code_key ON integrations (organization_id, code) WHERE category <> 'payment';
CREATE UNIQUE INDEX integrations_default_key ON integrations (organization_id, category) WHERE is_default AND deleted_at IS NULL;
CREATE INDEX integrations_provider_idx ON integrations (organization_id, provider);
-- Single sign-on routes an email domain to the auth integrations claiming it
-- in any organization (ListIntegrationsByDomain).
CREATE INDEX integrations_auth_domain_idx ON integrations (provider, lower(settings->>'domain')) WHERE category = 'auth';

ALTER TABLE integrations ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON integrations
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The link of a customer to its record in an accounting, tax or CRM
-- integration. Several customers may link the same external customer; a
-- customer has one tax link and one default link per category.
CREATE TABLE integration_customers (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  integration_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  category text NOT NULL,
  external_customer_id text NOT NULL DEFAULT '',
  subsidiary_id text,
  sync_with_provider boolean NOT NULL DEFAULT false,
  is_default boolean NOT NULL DEFAULT false,
  settings jsonb,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT integration_customers_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT integration_customers_integration_fkey FOREIGN KEY (organization_id, integration_id) REFERENCES integrations (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT integration_customers_customer_fkey FOREIGN KEY (organization_id, customer_id) REFERENCES customers (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT integration_customers_category_check CHECK (category IN ('payment', 'accounting', 'tax', 'crm', 'auth'))
);

CREATE INDEX integration_customers_external_customer_idx ON integration_customers (organization_id, integration_id, external_customer_id) WHERE external_customer_id <> '';
CREATE UNIQUE INDEX integration_customers_tax_key ON integration_customers (organization_id, customer_id) WHERE category = 'tax';
CREATE UNIQUE INDEX integration_customers_default_key ON integration_customers (organization_id, customer_id, category) WHERE is_default;
CREATE INDEX integration_customers_customer_idx ON integration_customers (organization_id, customer_id, id);
CREATE INDEX integration_customers_integration_idx ON integration_customers (organization_id, integration_id);

ALTER TABLE integration_customers ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON integration_customers
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The mapping of an add-on or billable metric (polymorphic, without foreign
-- key) to an external item, optionally per billing entity: unique per
-- (integration, mappable, billing entity), a NULL billing entity counting
-- as one value.
CREATE TABLE integration_mappings (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  integration_id uuid NOT NULL,
  mappable_type text NOT NULL,
  mappable_id uuid NOT NULL,
  billing_entity_id uuid,
  external_id text NOT NULL,
  external_account_code text,
  external_name text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT integration_mappings_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT integration_mappings_integration_fkey FOREIGN KEY (organization_id, integration_id) REFERENCES integrations (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT integration_mappings_billing_entity_fkey FOREIGN KEY (organization_id, billing_entity_id) REFERENCES billing_entities (organization_id, id),
  CONSTRAINT integration_mappings_mappable_type_check CHECK (mappable_type IN ('add_on', 'billable_metric'))
);

CREATE UNIQUE INDEX integration_mappings_mappable_key ON integration_mappings (organization_id, integration_id, mappable_type, mappable_id) WHERE billing_entity_id IS NULL;
CREATE UNIQUE INDEX integration_mappings_mappable_billing_entity_key ON integration_mappings (organization_id, integration_id, mappable_type, mappable_id, billing_entity_id) WHERE billing_entity_id IS NOT NULL;
CREATE INDEX integration_mappings_integration_idx ON integration_mappings (organization_id, integration_id, id);
CREATE INDEX integration_mappings_billing_entity_idx ON integration_mappings (organization_id, billing_entity_id) WHERE billing_entity_id IS NOT NULL;

ALTER TABLE integration_mappings ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON integration_mappings
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The mapping of a collection (fallback item, coupons, taxes, …) of an
-- integration, optionally per billing entity: unique per (integration,
-- type, billing entity). Currencies maps ISO 4217 codes to external codes.
CREATE TABLE integration_collection_mappings (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  integration_id uuid NOT NULL,
  mapping_type text NOT NULL,
  billing_entity_id uuid,
  external_id text,
  external_account_code text,
  external_name text,
  tax_nexus text,
  tax_type text,
  tax_code text,
  currencies jsonb,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT integration_collection_mappings_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT integration_collection_mappings_integration_fkey FOREIGN KEY (organization_id, integration_id) REFERENCES integrations (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT integration_collection_mappings_billing_entity_fkey FOREIGN KEY (organization_id, billing_entity_id) REFERENCES billing_entities (organization_id, id),
  CONSTRAINT integration_collection_mappings_mapping_type_check CHECK (mapping_type IN ('fallback_item', 'coupon', 'subscription_fee', 'minimum_commitment', 'tax', 'prepaid_credit', 'credit_note', 'account', 'currencies'))
);

CREATE UNIQUE INDEX integration_collection_mappings_type_key ON integration_collection_mappings (organization_id, integration_id, mapping_type) WHERE billing_entity_id IS NULL;
CREATE UNIQUE INDEX integration_collection_mappings_type_billing_entity_key ON integration_collection_mappings (organization_id, integration_id, mapping_type, billing_entity_id) WHERE billing_entity_id IS NOT NULL;
CREATE INDEX integration_collection_mappings_integration_idx ON integration_collection_mappings (organization_id, integration_id, id);
CREATE INDEX integration_collection_mappings_billing_entity_idx ON integration_collection_mappings (organization_id, billing_entity_id) WHERE billing_entity_id IS NOT NULL;

ALTER TABLE integration_collection_mappings ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON integration_collection_mappings
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The cached external catalog: one row per (integration, item type,
-- external id), the conflict target of the upsert.
CREATE TABLE integration_items (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  integration_id uuid NOT NULL,
  item_type text NOT NULL,
  external_id text NOT NULL,
  external_account_code text,
  external_name text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT integration_items_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT integration_items_integration_fkey FOREIGN KEY (organization_id, integration_id) REFERENCES integrations (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT integration_items_external_id_key UNIQUE (organization_id, integration_id, item_type, external_id),
  CONSTRAINT integration_items_item_type_check CHECK (item_type IN ('standard', 'tax', 'account'))
);

CREATE INDEX integration_items_integration_idx ON integration_items (organization_id, integration_id, id);

ALTER TABLE integration_items ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON integration_items
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The record of a local object (polymorphic, without foreign key) synced
-- to an integration: unique per (integration, resource type, syncable id).
CREATE TABLE integration_resources (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  integration_id uuid NOT NULL,
  resource_type text NOT NULL,
  syncable_id uuid NOT NULL,
  external_id text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT integration_resources_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT integration_resources_integration_fkey FOREIGN KEY (organization_id, integration_id) REFERENCES integrations (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT integration_resources_syncable_key UNIQUE (organization_id, integration_id, resource_type, syncable_id),
  CONSTRAINT integration_resources_resource_type_check CHECK (resource_type IN ('invoice', 'payment', 'credit_note', 'subscription'))
);

ALTER TABLE integration_resources ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON integration_resources
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
