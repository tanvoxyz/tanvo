-- Alerts, thresholds and triggered alerts (domain §19).

-- Alerts (domain §19.1) watch a subscription (by external ID, usage types)
-- or a wallet (wallet types). The code is unique per subscription or
-- wallet, and a wallet has at most one alert per type. Alerts are
-- soft-deleted (deleted_at, lago usage_monitoring/alert.rb:5-7,30): every
-- read and unique key covers only the rows not deleted, and an alert
-- watching a discarded billable metric is discarded with it.
CREATE TABLE alerts (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text,
  alert_type text NOT NULL,
  subscription_external_id text,
  wallet_id uuid,
  billable_metric_id uuid,
  direction text NOT NULL,
  previous_value numeric NOT NULL,
  last_processed_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT alerts_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT alerts_wallet_fkey FOREIGN KEY (organization_id, wallet_id) REFERENCES wallets (organization_id, id),
  CONSTRAINT alerts_billable_metric_fkey FOREIGN KEY (organization_id, billable_metric_id) REFERENCES billable_metrics (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT alerts_alert_type_check CHECK (alert_type IN (
    'current_usage_amount', 'billable_metric_current_usage_amount', 'billable_metric_current_usage_units',
    'lifetime_usage_amount', 'billable_metric_lifetime_usage_units', 'wallet_balance_amount',
    'wallet_credits_balance', 'wallet_ongoing_balance_amount', 'wallet_credits_ongoing_balance'
  )),
  CONSTRAINT alerts_direction_check CHECK (direction IN ('increasing', 'decreasing'))
);

CREATE UNIQUE INDEX alerts_subscription_code_key ON alerts (organization_id, subscription_external_id, code)
  WHERE wallet_id IS NULL AND deleted_at IS NULL;
CREATE UNIQUE INDEX alerts_wallet_code_key ON alerts (organization_id, wallet_id, code)
  WHERE wallet_id IS NOT NULL AND deleted_at IS NULL;
CREATE UNIQUE INDEX alerts_wallet_alert_type_key ON alerts (organization_id, wallet_id, alert_type)
  WHERE wallet_id IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX alerts_billable_metric_idx ON alerts (organization_id, billable_metric_id)
  WHERE billable_metric_id IS NOT NULL AND deleted_at IS NULL;

ALTER TABLE alerts ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON alerts
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The thresholds of an alert, in their order; replaced as a whole by an
-- update. The key is the position: threshold IDs are kept as given.
CREATE TABLE alert_thresholds (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  alert_id uuid NOT NULL,
  position integer NOT NULL,
  code text,
  value numeric NOT NULL,
  recurring boolean NOT NULL,
  PRIMARY KEY (organization_id, alert_id, position),
  CONSTRAINT alert_thresholds_alert_fkey FOREIGN KEY (organization_id, alert_id) REFERENCES alerts (organization_id, id) ON DELETE CASCADE
);

ALTER TABLE alert_thresholds ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON alert_thresholds
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Alert firings (domain §19.2). They outlive their alert, so alert_id has
-- no foreign key. crossed_thresholds is a jsonb array of
-- {code, value, recurring}.
CREATE TABLE triggered_alerts (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  alert_id uuid NOT NULL,
  subscription_id uuid,
  wallet_id uuid,
  current_value numeric NOT NULL,
  previous_value numeric NOT NULL,
  crossed_thresholds jsonb NOT NULL,
  triggered_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT triggered_alerts_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT triggered_alerts_subscription_fkey FOREIGN KEY (organization_id, subscription_id) REFERENCES subscriptions (organization_id, id),
  CONSTRAINT triggered_alerts_wallet_fkey FOREIGN KEY (organization_id, wallet_id) REFERENCES wallets (organization_id, id)
);

CREATE INDEX triggered_alerts_alert_idx ON triggered_alerts (organization_id, alert_id);
CREATE INDEX triggered_alerts_subscription_idx ON triggered_alerts (organization_id, subscription_id)
  WHERE subscription_id IS NOT NULL;
CREATE INDEX triggered_alerts_wallet_idx ON triggered_alerts (organization_id, wallet_id)
  WHERE wallet_id IS NOT NULL;

ALTER TABLE triggered_alerts ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON triggered_alerts
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
