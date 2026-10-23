-- Usage attribution types and values (parity §1.42).
--
-- Types are soft-deleted (deleted_at, lago usage_attribution_type.rb:4-6
-- and destroy_service.rb:17-20): a deleted type keeps its row so its
-- children still name their parent, and a code is unique among the kept
-- types of the organization. The attribution keys of the kept types do not
-- overlap; the service checks it (usage_attribution_type.rb:46-55).

CREATE TABLE usage_attribution_types (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text,
  description text,
  attribution_keys text[] NOT NULL,
  role text NOT NULL,
  parent_id uuid,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT usage_attribution_types_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT usage_attribution_types_parent_fkey FOREIGN KEY (organization_id, parent_id) REFERENCES usage_attribution_types (organization_id, id),
  CONSTRAINT usage_attribution_types_role_check CHECK (role IN ('hierarchical', 'flat')),
  CONSTRAINT usage_attribution_types_attribution_keys_check CHECK (cardinality(attribution_keys) BETWEEN 1 AND 4)
);

CREATE UNIQUE INDEX usage_attribution_types_code_key ON usage_attribution_types (organization_id, code) WHERE deleted_at IS NULL;
CREATE INDEX usage_attribution_types_parent_idx ON usage_attribution_types (organization_id, parent_id) WHERE parent_id IS NOT NULL;

ALTER TABLE usage_attribution_types ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON usage_attribution_types
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- A value is unique per customer and type, deleted values included
-- (index_usage_attribution_values_on_customer_type_and_value).
CREATE TABLE usage_attribution_values (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  usage_attribution_type_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  parent_id uuid,
  value text NOT NULL,
  last_seen_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT usage_attribution_values_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT usage_attribution_values_type_fkey FOREIGN KEY (organization_id, usage_attribution_type_id) REFERENCES usage_attribution_types (organization_id, id),
  CONSTRAINT usage_attribution_values_customer_fkey FOREIGN KEY (organization_id, customer_id) REFERENCES customers (organization_id, id),
  CONSTRAINT usage_attribution_values_parent_fkey FOREIGN KEY (organization_id, parent_id) REFERENCES usage_attribution_values (organization_id, id),
  CONSTRAINT usage_attribution_values_value_key UNIQUE (organization_id, customer_id, usage_attribution_type_id, value)
);

CREATE INDEX usage_attribution_values_type_idx ON usage_attribution_values (organization_id, usage_attribution_type_id);
CREATE INDEX usage_attribution_values_customer_seen_idx ON usage_attribution_values (organization_id, customer_id, last_seen_at);

ALTER TABLE usage_attribution_values ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON usage_attribution_values
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
