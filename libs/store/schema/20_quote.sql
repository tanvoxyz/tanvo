-- Quotes, quote versions, order forms, orders (domain §21).
--
-- A quote has numbered versions, at most one of them draft or approved; an
-- approved version has at most one order form, and a signed order form at
-- most one order. Numbers are unique per organization; sequential ids come
-- from NextSequence (per organization and creation year) under a lock.

CREATE TABLE quotes (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  sequential_id integer NOT NULL,
  customer_id uuid NOT NULL,
  order_type text NOT NULL,
  subscription_id uuid,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT quotes_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT quotes_customer_fkey FOREIGN KEY (organization_id, customer_id) REFERENCES customers (organization_id, id),
  CONSTRAINT quotes_subscription_fkey FOREIGN KEY (organization_id, subscription_id) REFERENCES subscriptions (organization_id, id),
  CONSTRAINT quotes_number_key UNIQUE (organization_id, number),
  CONSTRAINT quotes_order_type_check CHECK (order_type IN ('subscription_creation', 'subscription_amendment', 'one_off')),
  CONSTRAINT quotes_sequential_id_check CHECK (sequential_id >= 0)
);

CREATE INDEX quotes_customer_idx ON quotes (organization_id, customer_id);
CREATE INDEX quotes_subscription_idx ON quotes (organization_id, subscription_id) WHERE subscription_id IS NOT NULL;
CREATE INDEX quotes_created_at_idx ON quotes (organization_id, created_at);

ALTER TABLE quotes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON quotes
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The owners of quotes (members responsible for them), in their order.
CREATE TABLE quote_owners (
  organization_id uuid NOT NULL,
  quote_id uuid NOT NULL,
  membership_id uuid NOT NULL,
  position integer NOT NULL,
  PRIMARY KEY (organization_id, quote_id, membership_id),
  CONSTRAINT quote_owners_quote_fkey FOREIGN KEY (organization_id, quote_id) REFERENCES quotes (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT quote_owners_membership_fkey FOREIGN KEY (organization_id, membership_id) REFERENCES memberships (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX quote_owners_membership_idx ON quote_owners (organization_id, membership_id);

ALTER TABLE quote_owners ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON quote_owners
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE quote_versions (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  quote_id uuid NOT NULL,
  version integer NOT NULL,
  status text NOT NULL,
  void_reason text,
  -- The proposal, decoded by libs/billing, kept verbatim.
  content json,
  currency text,
  billing_entity_id uuid,
  approved_at timestamptz,
  voided_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT quote_versions_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT quote_versions_quote_fkey FOREIGN KEY (organization_id, quote_id) REFERENCES quotes (organization_id, id),
  CONSTRAINT quote_versions_billing_entity_fkey FOREIGN KEY (organization_id, billing_entity_id) REFERENCES billing_entities (organization_id, id),
  CONSTRAINT quote_versions_version_key UNIQUE (organization_id, quote_id, version),
  CONSTRAINT quote_versions_version_check CHECK (version >= 1),
  CONSTRAINT quote_versions_status_check CHECK (status IN ('draft', 'approved', 'voided')),
  CONSTRAINT quote_versions_void_reason_check CHECK (void_reason IN ('manual', 'superseded', 'cascade_of_expired', 'cascade_of_voided'))
);

-- A quote has at most one draft or approved version.
CREATE UNIQUE INDEX quote_versions_active_key ON quote_versions (organization_id, quote_id) WHERE status <> 'voided';
CREATE INDEX quote_versions_billing_entity_idx ON quote_versions (organization_id, billing_entity_id) WHERE billing_entity_id IS NOT NULL;

ALTER TABLE quote_versions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON quote_versions
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE order_forms (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  sequential_id integer NOT NULL,
  customer_id uuid NOT NULL,
  quote_version_id uuid NOT NULL,
  status text NOT NULL,
  void_reason text,
  expires_at timestamptz,
  signed_at timestamptz,
  signed_by text,
  signed_document text,
  voided_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT order_forms_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT order_forms_customer_fkey FOREIGN KEY (organization_id, customer_id) REFERENCES customers (organization_id, id),
  CONSTRAINT order_forms_quote_version_fkey FOREIGN KEY (organization_id, quote_version_id) REFERENCES quote_versions (organization_id, id),
  CONSTRAINT order_forms_number_key UNIQUE (organization_id, number),
  -- A quote version has at most one order form.
  CONSTRAINT order_forms_quote_version_key UNIQUE (organization_id, quote_version_id),
  CONSTRAINT order_forms_status_check CHECK (status IN ('generated', 'signed', 'expired', 'voided')),
  CONSTRAINT order_forms_void_reason_check CHECK (void_reason IN ('manual', 'expired', 'invalid')),
  CONSTRAINT order_forms_sequential_id_check CHECK (sequential_id >= 0)
);

CREATE INDEX order_forms_customer_idx ON order_forms (organization_id, customer_id);
CREATE INDEX order_forms_created_at_idx ON order_forms (organization_id, created_at);
-- The expiry scan across organizations.
CREATE INDEX order_forms_expiring_idx ON order_forms (expires_at) WHERE status = 'generated' AND expires_at IS NOT NULL;

ALTER TABLE order_forms ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON order_forms
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE orders (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  sequential_id integer NOT NULL,
  customer_id uuid NOT NULL,
  order_form_id uuid NOT NULL,
  status text NOT NULL,
  execution_mode text,
  execute_at timestamptz,
  executed_at timestamptz,
  -- What the execution created, kept verbatim.
  execution_record json,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT orders_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT orders_customer_fkey FOREIGN KEY (organization_id, customer_id) REFERENCES customers (organization_id, id),
  CONSTRAINT orders_order_form_fkey FOREIGN KEY (organization_id, order_form_id) REFERENCES order_forms (organization_id, id),
  CONSTRAINT orders_number_key UNIQUE (organization_id, number),
  -- An order form has at most one order.
  CONSTRAINT orders_order_form_key UNIQUE (organization_id, order_form_id),
  CONSTRAINT orders_status_check CHECK (status IN ('created', 'executed', 'failed')),
  CONSTRAINT orders_execution_mode_check CHECK (execution_mode IN ('execute', 'order_only')),
  CONSTRAINT orders_sequential_id_check CHECK (sequential_id >= 0)
);

CREATE INDEX orders_customer_idx ON orders (organization_id, customer_id);
CREATE INDEX orders_created_at_idx ON orders (organization_id, created_at);
-- The execution scan across organizations.
CREATE INDEX orders_executable_idx ON orders (execute_at) WHERE status = 'created' AND execute_at IS NOT NULL;

ALTER TABLE orders ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON orders
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
