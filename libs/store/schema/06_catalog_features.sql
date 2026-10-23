-- Catalog: commitments, usage thresholds, features, privileges, entitlements
-- and entitlement removals (domain §7.8–§7.10). The foreign keys to
-- subscriptions are added in 90_fk_catalog_features.sql.

-- Minimum commitments of plans: at most one per plan and type.
CREATE TABLE commitments (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  plan_id uuid NOT NULL,
  commitment_type text NOT NULL,
  amount bigint NOT NULL,
  invoice_display_name text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT commitments_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT commitments_plan_fkey FOREIGN KEY (organization_id, plan_id) REFERENCES plans (organization_id, id),
  CONSTRAINT commitments_plan_type_key UNIQUE (organization_id, plan_id, commitment_type),
  CONSTRAINT commitments_commitment_type_check CHECK (commitment_type IN ('minimum_commitment')),
  CONSTRAINT commitments_amount_check CHECK (amount > 0)
);

ALTER TABLE commitments ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON commitments
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The taxes of commitments, in their order. Deleting a tax removes it.
CREATE TABLE commitment_taxes (
  organization_id uuid NOT NULL,
  commitment_id uuid NOT NULL,
  tax_id uuid NOT NULL,
  position integer NOT NULL,
  PRIMARY KEY (organization_id, commitment_id, tax_id),
  CONSTRAINT commitment_taxes_commitment_fkey FOREIGN KEY (organization_id, commitment_id) REFERENCES commitments (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT commitment_taxes_tax_fkey FOREIGN KEY (organization_id, tax_id) REFERENCES taxes (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX commitment_taxes_tax_idx ON commitment_taxes (organization_id, tax_id);

ALTER TABLE commitment_taxes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON commitment_taxes
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Progressive billing thresholds of a plan or of a subscription (exactly
-- one owner). Per owner, non-recurring amounts are unique and there is at
-- most one recurring threshold.
CREATE TABLE usage_thresholds (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  plan_id uuid,
  subscription_id uuid,
  amount bigint NOT NULL,
  recurring boolean NOT NULL DEFAULT false,
  threshold_display_name text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT usage_thresholds_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT usage_thresholds_plan_fkey FOREIGN KEY (organization_id, plan_id) REFERENCES plans (organization_id, id),
  CONSTRAINT usage_thresholds_owner_check CHECK ((plan_id IS NULL) <> (subscription_id IS NULL)),
  CONSTRAINT usage_thresholds_amount_check CHECK (amount > 0)
);

CREATE INDEX usage_thresholds_plan_idx ON usage_thresholds (organization_id, plan_id) WHERE plan_id IS NOT NULL;
CREATE INDEX usage_thresholds_subscription_idx ON usage_thresholds (organization_id, subscription_id) WHERE subscription_id IS NOT NULL;
CREATE UNIQUE INDEX usage_thresholds_plan_amount_key ON usage_thresholds (organization_id, plan_id, amount) WHERE plan_id IS NOT NULL AND NOT recurring;
CREATE UNIQUE INDEX usage_thresholds_plan_recurring_key ON usage_thresholds (organization_id, plan_id) WHERE plan_id IS NOT NULL AND recurring;
CREATE UNIQUE INDEX usage_thresholds_subscription_amount_key ON usage_thresholds (organization_id, subscription_id, amount) WHERE subscription_id IS NOT NULL AND NOT recurring;
CREATE UNIQUE INDEX usage_thresholds_subscription_recurring_key ON usage_thresholds (organization_id, subscription_id) WHERE subscription_id IS NOT NULL AND recurring;

ALTER TABLE usage_thresholds ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON usage_thresholds
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Features are soft-deleted (deleted_at, lago feature.rb:5-9): reads and the
-- uniqueness of a code in the organization see kept rows only.
CREATE TABLE features (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text,
  description text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT features_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id)
);

CREATE UNIQUE INDEX features_code_key ON features (organization_id, code) WHERE deleted_at IS NULL;

ALTER TABLE features ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON features
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The privileges of a feature, in their order; part of the feature
-- aggregate, written with it. Privileges are soft-deleted (deleted_at, lago
-- privilege.rb:5-9): reads and the uniqueness of a code in the feature see
-- kept rows only.
CREATE TABLE feature_privileges (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  feature_id uuid NOT NULL,
  position integer NOT NULL,
  code text NOT NULL,
  name text,
  value_type text NOT NULL,
  select_options text[] NOT NULL DEFAULT '{}',
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT feature_privileges_feature_fkey FOREIGN KEY (organization_id, feature_id) REFERENCES features (organization_id, id),
  CONSTRAINT feature_privileges_value_type_check CHECK (value_type IN ('integer', 'string', 'boolean', 'select'))
);

CREATE UNIQUE INDEX feature_privileges_code_key ON feature_privileges (organization_id, feature_id, code) WHERE deleted_at IS NULL;

ALTER TABLE feature_privileges ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON feature_privileges
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Grants of a feature to a plan or to a subscription (exactly one owner),
-- at most one per feature and owner. privilege_values maps privilege codes
-- to their stored values. Entitlements are soft-deleted (deleted_at, lago
-- entitlement.rb:5-9): reads and the uniqueness of a feature per owner see
-- kept rows only.
CREATE TABLE entitlements (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  feature_id uuid NOT NULL,
  plan_id uuid,
  subscription_id uuid,
  privilege_values jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT entitlements_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT entitlements_feature_fkey FOREIGN KEY (organization_id, feature_id) REFERENCES features (organization_id, id),
  CONSTRAINT entitlements_plan_fkey FOREIGN KEY (organization_id, plan_id) REFERENCES plans (organization_id, id),
  CONSTRAINT entitlements_owner_check CHECK ((plan_id IS NULL) <> (subscription_id IS NULL)),
  CONSTRAINT entitlements_privilege_values_check CHECK (jsonb_typeof(privilege_values) = 'object')
);

CREATE INDEX entitlements_feature_idx ON entitlements (organization_id, feature_id);
CREATE UNIQUE INDEX entitlements_plan_feature_key ON entitlements (organization_id, plan_id, feature_id) WHERE plan_id IS NOT NULL AND deleted_at IS NULL;
CREATE UNIQUE INDEX entitlements_subscription_feature_key ON entitlements (organization_id, subscription_id, feature_id) WHERE subscription_id IS NOT NULL AND deleted_at IS NULL;

ALTER TABLE entitlements ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON entitlements
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Removals, for one subscription, of a feature (privilege_code = '') or of
-- one privilege granted by its plan (lago's subscription_feature_removals).
CREATE TABLE entitlement_removals (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  subscription_id uuid NOT NULL,
  feature_id uuid NOT NULL,
  privilege_code text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT entitlement_removals_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT entitlement_removals_feature_fkey FOREIGN KEY (organization_id, feature_id) REFERENCES features (organization_id, id),
  CONSTRAINT entitlement_removals_subscription_feature_privilege_key UNIQUE (organization_id, subscription_id, feature_id, privilege_code)
);

CREATE INDEX entitlement_removals_feature_idx ON entitlement_removals (organization_id, feature_id);

ALTER TABLE entitlement_removals ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON entitlement_removals
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
