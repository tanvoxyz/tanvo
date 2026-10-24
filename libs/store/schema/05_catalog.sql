-- Catalog: pricing units, billable metrics and their filters, plans, add-ons,
-- charges and charge filters, fixed charges (domain §7.1–§7.6), with the tax
-- lists of plans, add-ons, charges and fixed charges (domain §22.10).
--
-- Rows are deleted, not soft-deleted, except billable metrics (below): the
-- repositories remove them and the services delete the dependent rows first
-- (a metric's charges, a plan's override plans, charges and fixed charges,
-- an add-on's fixed charges), so the references below refuse a delete that
-- would leave a dangling row.
-- Filters and tax lists belong to their parent and go with it. Deleting a
-- tax removes it from the lists.

CREATE TABLE pricing_units (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  short_name text NOT NULL,
  description text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT pricing_units_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT pricing_units_code_key UNIQUE (organization_id, code)
);

ALTER TABLE pricing_units ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pricing_units
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Billable metrics are soft-deleted (deleted_at, lago billable_metric.rb:5-9
-- and destroy_service.rb:33): a deleted metric keeps its row and its filters
-- so history can still name it, reads see kept rows only, and a code is
-- unique among the kept metrics of the organization.
CREATE TABLE billable_metrics (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  description text,
  aggregation_type text NOT NULL,
  field_name text,
  weighted_interval text,
  recurring boolean NOT NULL DEFAULT false,
  expression text,
  rounding_function text,
  rounding_precision integer,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT billable_metrics_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT billable_metrics_aggregation_type_check CHECK (aggregation_type IN ('count', 'sum', 'max', 'unique_count', 'weighted_sum', 'latest')),
  CONSTRAINT billable_metrics_weighted_interval_check CHECK (weighted_interval IN ('seconds')),
  CONSTRAINT billable_metrics_rounding_function_check CHECK (rounding_function IN ('round', 'floor', 'ceil'))
);

CREATE UNIQUE INDEX billable_metrics_code_key ON billable_metrics (organization_id, code) WHERE deleted_at IS NULL;

ALTER TABLE billable_metrics ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON billable_metrics
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The filters of a metric, in their order; keys are unique among the kept
-- filters of a metric. A filter removed from its metric, or the filters of
-- a deleted metric, are discarded (deleted_at), so product filter values
-- and history keep naming them.
CREATE TABLE billable_metric_filters (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  billable_metric_id uuid NOT NULL,
  key text NOT NULL,
  filter_values text[] NOT NULL,
  position integer NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT billable_metric_filters_billable_metric_fkey FOREIGN KEY (organization_id, billable_metric_id) REFERENCES billable_metrics (organization_id, id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX billable_metric_filters_key_idx ON billable_metric_filters (organization_id, billable_metric_id, key) WHERE deleted_at IS NULL;

ALTER TABLE billable_metric_filters ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON billable_metric_filters
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE plans (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  invoice_display_name text,
  description text,
  billing_interval text NOT NULL,
  amount bigint NOT NULL,
  currency text NOT NULL,
  pay_in_advance boolean NOT NULL DEFAULT false,
  trial_period_days integer NOT NULL DEFAULT 0,
  bill_charges_monthly boolean,
  bill_fixed_charges_monthly boolean,
  -- The original plan of an override plan (domain §7.2.1).
  parent_id uuid,
  metadata jsonb NOT NULL DEFAULT '[]',
  status text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT plans_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT plans_parent_fkey FOREIGN KEY (organization_id, parent_id) REFERENCES plans (organization_id, id),
  CONSTRAINT plans_billing_interval_check CHECK (billing_interval IN ('weekly', 'monthly', 'quarterly', 'semiannual', 'yearly')),
  CONSTRAINT plans_status_check CHECK (status IN ('active', 'pending_deletion', 'deleted')),
  CONSTRAINT plans_amount_check CHECK (amount >= 0),
  CONSTRAINT plans_trial_period_days_check CHECK (trial_period_days >= 0),
  CONSTRAINT plans_parent_id_check CHECK (parent_id <> id)
);

-- Codes are unique among original plans that are not deleted; override
-- plans share their parent's code, and a deleted plan (kept for its
-- subscriptions, domain §7.2) frees its code.
CREATE UNIQUE INDEX plans_code_key ON plans (organization_id, code) WHERE parent_id IS NULL AND status <> 'deleted';
CREATE INDEX plans_parent_idx ON plans (organization_id, parent_id) WHERE parent_id IS NOT NULL;

ALTER TABLE plans ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON plans
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE plan_taxes (
  organization_id uuid NOT NULL,
  plan_id uuid NOT NULL,
  tax_id uuid NOT NULL,
  position integer NOT NULL,
  PRIMARY KEY (organization_id, plan_id, tax_id),
  CONSTRAINT plan_taxes_plan_fkey FOREIGN KEY (organization_id, plan_id) REFERENCES plans (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT plan_taxes_tax_fkey FOREIGN KEY (organization_id, tax_id) REFERENCES taxes (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX plan_taxes_tax_idx ON plan_taxes (organization_id, tax_id);

ALTER TABLE plan_taxes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON plan_taxes
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Add-ons are soft-deleted (deleted_at, lago add_on.rb): a discarded one
-- stays for the fees that reference it, and frees its code.
CREATE TABLE add_ons (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  invoice_display_name text,
  description text,
  amount bigint NOT NULL,
  currency text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT add_ons_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id)
);

CREATE UNIQUE INDEX add_ons_code_key ON add_ons (organization_id, code) WHERE deleted_at IS NULL;

ALTER TABLE add_ons ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON add_ons
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE add_on_taxes (
  organization_id uuid NOT NULL,
  add_on_id uuid NOT NULL,
  tax_id uuid NOT NULL,
  position integer NOT NULL,
  PRIMARY KEY (organization_id, add_on_id, tax_id),
  CONSTRAINT add_on_taxes_add_on_fkey FOREIGN KEY (organization_id, add_on_id) REFERENCES add_ons (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT add_on_taxes_tax_fkey FOREIGN KEY (organization_id, tax_id) REFERENCES taxes (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX add_on_taxes_tax_idx ON add_on_taxes (organization_id, tax_id);

ALTER TABLE add_on_taxes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON add_on_taxes
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Charges are soft-deleted (deleted_at, lago charge.rb): a discarded one
-- stays for the fees that reference it, and frees its code.
CREATE TABLE charges (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  plan_id uuid NOT NULL,
  -- The charge of the original plan an override charge copies.
  parent_id uuid,
  code text NOT NULL,
  billable_metric_id uuid NOT NULL,
  charge_model text NOT NULL,
  properties json NOT NULL,
  pay_in_advance boolean NOT NULL DEFAULT false,
  invoiceable boolean NOT NULL DEFAULT true,
  regroup_paid_fees text,
  prorated boolean NOT NULL DEFAULT false,
  -- Splits the usage by the event's target_wallet_code property and pays
  -- each targeted fee from that wallet only.
  accepts_target_wallet boolean NOT NULL DEFAULT false,
  min_amount bigint NOT NULL DEFAULT 0,
  invoice_display_name text,
  applied_pricing_unit_id uuid,
  conversion_rate numeric,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT charges_plan_fkey FOREIGN KEY (organization_id, plan_id) REFERENCES plans (organization_id, id),
  CONSTRAINT charges_parent_fkey FOREIGN KEY (organization_id, parent_id) REFERENCES charges (organization_id, id),
  CONSTRAINT charges_billable_metric_fkey FOREIGN KEY (organization_id, billable_metric_id) REFERENCES billable_metrics (organization_id, id),
  CONSTRAINT charges_applied_pricing_unit_fkey FOREIGN KEY (organization_id, applied_pricing_unit_id) REFERENCES pricing_units (organization_id, id),
  CONSTRAINT charges_charge_model_check CHECK (charge_model IN ('standard', 'graduated', 'graduated_percentage', 'package', 'percentage', 'volume', 'dynamic')),
  CONSTRAINT charges_regroup_paid_fees_check CHECK (regroup_paid_fees IN ('invoice')),
  CONSTRAINT charges_min_amount_check CHECK (min_amount >= 0),
  CONSTRAINT charges_applied_pricing_unit_check CHECK ((applied_pricing_unit_id IS NULL) = (conversion_rate IS NULL)),
  CONSTRAINT charges_conversion_rate_check CHECK (conversion_rate > 0),
  CONSTRAINT charges_parent_id_check CHECK (parent_id <> id)
);

-- Codes are unique among a plan's kept charges that are not override
-- copies.
CREATE UNIQUE INDEX charges_code_key ON charges (organization_id, plan_id, code) WHERE parent_id IS NULL AND deleted_at IS NULL;
CREATE INDEX charges_plan_idx ON charges (organization_id, plan_id) WHERE deleted_at IS NULL;
CREATE INDEX charges_billable_metric_idx ON charges (organization_id, billable_metric_id) WHERE deleted_at IS NULL;
CREATE INDEX charges_parent_idx ON charges (organization_id, parent_id) WHERE parent_id IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX charges_applied_pricing_unit_idx ON charges (organization_id, applied_pricing_unit_id) WHERE applied_pricing_unit_id IS NOT NULL;

ALTER TABLE charges ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON charges
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The filters of a charge, in their order. values maps metric filter keys to
-- the allowed values (filter_values, a store-encoded jsonb object; lago
-- charge_filter_values); codes are unique among the kept filters of a
-- charge (an empty code is not yet assigned). Filters are soft-deleted
-- (deleted_at, lago charge_filter.rb and charge_filter_value.rb) and stay
-- for the fees that reference them.
CREATE TABLE charge_filters (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  charge_id uuid NOT NULL,
  filter_values jsonb NOT NULL,
  properties json NOT NULL,
  invoice_display_name text,
  code text NOT NULL DEFAULT '',
  position integer NOT NULL,
  created_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT charge_filters_charge_fkey FOREIGN KEY (organization_id, charge_id) REFERENCES charges (organization_id, id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX charge_filters_code_key ON charge_filters (organization_id, charge_id, code) WHERE code <> '' AND deleted_at IS NULL;
CREATE INDEX charge_filters_charge_idx ON charge_filters (organization_id, charge_id, position) WHERE deleted_at IS NULL;

ALTER TABLE charge_filters ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON charge_filters
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE charge_taxes (
  organization_id uuid NOT NULL,
  charge_id uuid NOT NULL,
  tax_id uuid NOT NULL,
  position integer NOT NULL,
  PRIMARY KEY (organization_id, charge_id, tax_id),
  CONSTRAINT charge_taxes_charge_fkey FOREIGN KEY (organization_id, charge_id) REFERENCES charges (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT charge_taxes_tax_fkey FOREIGN KEY (organization_id, tax_id) REFERENCES taxes (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX charge_taxes_tax_idx ON charge_taxes (organization_id, tax_id);

ALTER TABLE charge_taxes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON charge_taxes
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Fixed charges are soft-deleted (deleted_at, lago fixed_charge.rb): a
-- discarded one stays for the fees that reference it, and frees its code.
CREATE TABLE fixed_charges (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  plan_id uuid NOT NULL,
  -- The fixed charge of the original plan an override fixed charge copies.
  parent_id uuid,
  code text NOT NULL,
  add_on_id uuid NOT NULL,
  charge_model text NOT NULL,
  properties json NOT NULL,
  units numeric NOT NULL,
  pay_in_advance boolean NOT NULL DEFAULT false,
  prorated boolean NOT NULL DEFAULT false,
  invoice_display_name text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT fixed_charges_plan_fkey FOREIGN KEY (organization_id, plan_id) REFERENCES plans (organization_id, id),
  CONSTRAINT fixed_charges_parent_fkey FOREIGN KEY (organization_id, parent_id) REFERENCES fixed_charges (organization_id, id),
  CONSTRAINT fixed_charges_add_on_fkey FOREIGN KEY (organization_id, add_on_id) REFERENCES add_ons (organization_id, id),
  CONSTRAINT fixed_charges_charge_model_check CHECK (charge_model IN ('standard', 'graduated', 'volume')),
  CONSTRAINT fixed_charges_units_check CHECK (units >= 0),
  CONSTRAINT fixed_charges_parent_id_check CHECK (parent_id <> id)
);

-- Codes are unique among a plan's kept fixed charges that are not override
-- copies.
CREATE UNIQUE INDEX fixed_charges_code_key ON fixed_charges (organization_id, plan_id, code) WHERE parent_id IS NULL AND deleted_at IS NULL;
CREATE INDEX fixed_charges_plan_idx ON fixed_charges (organization_id, plan_id) WHERE deleted_at IS NULL;
CREATE INDEX fixed_charges_add_on_idx ON fixed_charges (organization_id, add_on_id) WHERE deleted_at IS NULL;
CREATE INDEX fixed_charges_parent_idx ON fixed_charges (organization_id, parent_id) WHERE parent_id IS NOT NULL AND deleted_at IS NULL;

ALTER TABLE fixed_charges ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON fixed_charges
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE fixed_charge_taxes (
  organization_id uuid NOT NULL,
  fixed_charge_id uuid NOT NULL,
  tax_id uuid NOT NULL,
  position integer NOT NULL,
  PRIMARY KEY (organization_id, fixed_charge_id, tax_id),
  CONSTRAINT fixed_charge_taxes_fixed_charge_fkey FOREIGN KEY (organization_id, fixed_charge_id) REFERENCES fixed_charges (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT fixed_charge_taxes_tax_fkey FOREIGN KEY (organization_id, tax_id) REFERENCES taxes (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX fixed_charge_taxes_tax_idx ON fixed_charge_taxes (organization_id, tax_id);

ALTER TABLE fixed_charge_taxes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON fixed_charge_taxes
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
