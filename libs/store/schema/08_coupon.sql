-- Coupons, their plan and billable metric limitations, and applied coupons
-- (domain §8).

CREATE TABLE coupons (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  description text,
  coupon_type text NOT NULL,
  amount bigint,
  amount_currency text,
  percentage_rate numeric,
  frequency text NOT NULL,
  frequency_duration integer,
  reusable boolean NOT NULL DEFAULT true,
  expiration text NOT NULL,
  expiration_at timestamptz,
  status text NOT NULL,
  terminated_at timestamptz,
  deleted_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT coupons_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT coupons_coupon_type_check CHECK (coupon_type IN ('fixed_amount', 'percentage')),
  CONSTRAINT coupons_frequency_check CHECK (frequency IN ('once', 'recurring', 'forever')),
  CONSTRAINT coupons_expiration_check CHECK (expiration IN ('no_expiration', 'time_limit')),
  CONSTRAINT coupons_status_check CHECK (status IN ('active', 'terminated'))
);

-- Codes are unique among the coupons that are not deleted.
CREATE UNIQUE INDEX coupons_code_kept_key ON coupons (organization_id, code) WHERE deleted_at IS NULL;
-- The periodic termination job scans the expiring active coupons of every
-- organization.
CREATE INDEX coupons_expiration_at_idx ON coupons (expiration_at) WHERE status = 'active' AND expiration = 'time_limit' AND deleted_at IS NULL;

ALTER TABLE coupons ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON coupons
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The plans a coupon is limited to, in their order. Deleting a plan removes
-- it from the limitations.
CREATE TABLE coupon_plans (
  organization_id uuid NOT NULL,
  coupon_id uuid NOT NULL,
  plan_id uuid NOT NULL,
  position integer NOT NULL,
  PRIMARY KEY (organization_id, coupon_id, plan_id),
  CONSTRAINT coupon_plans_coupon_fkey FOREIGN KEY (organization_id, coupon_id) REFERENCES coupons (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT coupon_plans_plan_fkey FOREIGN KEY (organization_id, plan_id) REFERENCES plans (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX coupon_plans_plan_idx ON coupon_plans (organization_id, plan_id);

ALTER TABLE coupon_plans ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON coupon_plans
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The billable metrics a coupon is limited to, in their order. Deleting a
-- metric removes it from the limitations.
CREATE TABLE coupon_billable_metrics (
  organization_id uuid NOT NULL,
  coupon_id uuid NOT NULL,
  billable_metric_id uuid NOT NULL,
  position integer NOT NULL,
  PRIMARY KEY (organization_id, coupon_id, billable_metric_id),
  CONSTRAINT coupon_billable_metrics_coupon_fkey FOREIGN KEY (organization_id, coupon_id) REFERENCES coupons (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT coupon_billable_metrics_billable_metric_fkey FOREIGN KEY (organization_id, billable_metric_id) REFERENCES billable_metrics (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX coupon_billable_metrics_billable_metric_idx ON coupon_billable_metrics (organization_id, billable_metric_id);

ALTER TABLE coupon_billable_metrics ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON coupon_billable_metrics
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE applied_coupons (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  coupon_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  amount bigint,
  amount_currency text,
  percentage_rate numeric,
  frequency text NOT NULL,
  frequency_duration integer,
  frequency_duration_remaining integer,
  remaining_amount bigint,
  status text NOT NULL,
  terminated_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT applied_coupons_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT applied_coupons_coupon_fkey FOREIGN KEY (organization_id, coupon_id) REFERENCES coupons (organization_id, id),
  CONSTRAINT applied_coupons_customer_fkey FOREIGN KEY (organization_id, customer_id) REFERENCES customers (organization_id, id),
  CONSTRAINT applied_coupons_frequency_check CHECK (frequency IN ('once', 'recurring', 'forever')),
  CONSTRAINT applied_coupons_status_check CHECK (status IN ('active', 'terminated'))
);

CREATE INDEX applied_coupons_customer_idx ON applied_coupons (organization_id, customer_id, created_at);
CREATE INDEX applied_coupons_coupon_idx ON applied_coupons (organization_id, coupon_id);

ALTER TABLE applied_coupons ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON applied_coupons
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
