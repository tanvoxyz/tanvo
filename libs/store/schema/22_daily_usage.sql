-- Daily usage snapshots of subscriptions (the revenue analytics source).

-- One snapshot of a subscription's current usage per day in the customer's
-- time zone (usage_date), with its change from the previous snapshot of the
-- same charges period [from_datetime, to_datetime). usage and usage_diff
-- are the subscription.UsageSnapshot JSON form; usage_diff is an empty
-- object for the first day of a history fill.
CREATE TABLE daily_usages (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  customer_id uuid NOT NULL,
  subscription_id uuid NOT NULL,
  external_subscription_id text NOT NULL,
  usage jsonb NOT NULL,
  usage_diff jsonb NOT NULL,
  from_datetime timestamptz NOT NULL,
  to_datetime timestamptz NOT NULL,
  refreshed_at timestamptz NOT NULL,
  usage_date date NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT daily_usages_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT daily_usages_customer_fkey FOREIGN KEY (organization_id, customer_id) REFERENCES customers (organization_id, id),
  CONSTRAINT daily_usages_subscription_fkey FOREIGN KEY (organization_id, subscription_id) REFERENCES subscriptions (organization_id, id)
);

CREATE INDEX daily_usages_external_subscription_id_idx ON daily_usages (organization_id, external_subscription_id);
CREATE INDEX daily_usages_customer_idx ON daily_usages (organization_id, customer_id);
CREATE INDEX daily_usages_subscription_usage_date_idx ON daily_usages (organization_id, subscription_id, usage_date);
CREATE INDEX daily_usages_usage_date_idx ON daily_usages (organization_id, usage_date);

ALTER TABLE daily_usages ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON daily_usages
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
