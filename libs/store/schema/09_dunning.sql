-- Dunning campaigns and their thresholds (domain §16), and the foreign keys
-- of the campaigns applied to billing entities and customers.

CREATE TABLE dunning_campaigns (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  description text,
  days_between_attempts integer NOT NULL,
  max_attempts integer NOT NULL,
  bcc_emails text[] NOT NULL DEFAULT '{}',
  deleted_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT dunning_campaigns_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT dunning_campaigns_days_between_attempts_check CHECK (days_between_attempts >= 1),
  CONSTRAINT dunning_campaigns_max_attempts_check CHECK (max_attempts >= 1)
);

-- Codes are unique among live (not deleted) campaigns.
CREATE UNIQUE INDEX dunning_campaigns_code_live_key ON dunning_campaigns (organization_id, code) WHERE deleted_at IS NULL;

ALTER TABLE dunning_campaigns ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON dunning_campaigns
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The thresholds of a campaign, one per currency, in their order.
CREATE TABLE dunning_campaign_thresholds (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  dunning_campaign_id uuid NOT NULL,
  currency text NOT NULL,
  amount bigint NOT NULL,
  position integer NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT dunning_campaign_thresholds_campaign_fkey FOREIGN KEY (organization_id, dunning_campaign_id) REFERENCES dunning_campaigns (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT dunning_campaign_thresholds_currency_key UNIQUE (organization_id, dunning_campaign_id, currency),
  CONSTRAINT dunning_campaign_thresholds_amount_check CHECK (amount >= 0)
);

ALTER TABLE dunning_campaign_thresholds ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON dunning_campaign_thresholds
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Campaigns are soft-deleted, and deleting one resets the billing entities
-- and customers that use it in the application (domain §16.1); a campaign
-- still applied somewhere cannot be hard-deleted.
ALTER TABLE billing_entities
  ADD CONSTRAINT billing_entities_applied_dunning_campaign_fkey FOREIGN KEY (organization_id, applied_dunning_campaign_id) REFERENCES dunning_campaigns (organization_id, id);
CREATE INDEX billing_entities_applied_dunning_campaign_idx ON billing_entities (organization_id, applied_dunning_campaign_id) WHERE applied_dunning_campaign_id IS NOT NULL;

ALTER TABLE customers
  ADD CONSTRAINT customers_applied_dunning_campaign_fkey FOREIGN KEY (organization_id, applied_dunning_campaign_id) REFERENCES dunning_campaigns (organization_id, id);
