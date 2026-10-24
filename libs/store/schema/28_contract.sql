-- Contracts: the rate cards applied to subscriptions priced by rate cards
-- (subscriptions.contract), their phases and their billing segments
-- (domain §21.4).

-- A rate card applied to a contract, at most once among the kept rows
-- (contract_rate_card.rb:27). The contract's ending_at is the only end of
-- its cards; next_billing_at is NULL once the card's schedule is exhausted.
CREATE TABLE subscription_rate_cards (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  subscription_id uuid NOT NULL,
  rate_card_id uuid NOT NULL,
  units numeric,
  effective_date date NOT NULL,
  billing_anchor_date date NOT NULL,
  next_billing_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT subscription_rate_cards_subscription_fkey FOREIGN KEY (organization_id, subscription_id) REFERENCES subscriptions (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT subscription_rate_cards_rate_card_fkey FOREIGN KEY (organization_id, rate_card_id) REFERENCES rate_cards (organization_id, id),
  CONSTRAINT subscription_rate_cards_units_check CHECK (units >= 0)
);

CREATE UNIQUE INDEX subscription_rate_cards_rate_card_key_idx ON subscription_rate_cards (organization_id, subscription_id, rate_card_id) WHERE deleted_at IS NULL;
CREATE INDEX subscription_rate_cards_rate_card_idx ON subscription_rate_cards (organization_id, rate_card_id);
CREATE INDEX subscription_rate_cards_next_billing_at_idx ON subscription_rate_cards (organization_id, next_billing_at) WHERE deleted_at IS NULL;

ALTER TABLE subscription_rate_cards ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON subscription_rate_cards
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The phases of a contract rate card, as rate_phases are of a plan rate
-- card, with their rate overrides embedded (override_rate_model NULL for
-- none).
CREATE TABLE subscription_rate_phases (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  subscription_rate_card_id uuid NOT NULL,
  code text NOT NULL,
  position integer NOT NULL,
  name text,
  billing_interval_cycle_count integer,
  override_rate_model text,
  override_properties json,
  override_min_amount bigint,
  override_billing_interval_count integer,
  override_billing_interval_unit text,
  override_pricing_unit_conversion_rate numeric,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT subscription_rate_phases_card_fkey FOREIGN KEY (organization_id, subscription_rate_card_id) REFERENCES subscription_rate_cards (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT subscription_rate_phases_position_check CHECK (position > 0),
  CONSTRAINT subscription_rate_phases_billing_interval_cycle_count_check CHECK (billing_interval_cycle_count > 0),
  CONSTRAINT subscription_rate_phases_override_check CHECK ((override_rate_model IS NULL) = (override_properties IS NULL)),
  CONSTRAINT subscription_rate_phases_override_rate_model_check CHECK (override_rate_model IN ('standard', 'graduated', 'graduated_percentage', 'package', 'percentage', 'volume', 'dynamic')),
  CONSTRAINT subscription_rate_phases_override_billing_interval_unit_check CHECK (override_billing_interval_unit IN ('day', 'week', 'month', 'year'))
);

CREATE UNIQUE INDEX subscription_rate_phases_code_idx ON subscription_rate_phases (organization_id, subscription_rate_card_id, code) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX subscription_rate_phases_position_idx ON subscription_rate_phases (organization_id, subscription_rate_card_id, position) WHERE deleted_at IS NULL;

ALTER TABLE subscription_rate_phases ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON subscription_rate_phases
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- A priced window [started_at, ended_at) of a contract rate card's
-- schedule (billing_segment.rb). Segments of a card never overlap; the
-- writer checks it under the customer's billing schedule lock, and the
-- unique start backs it.
CREATE TABLE billing_segments (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  subscription_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  subscription_rate_card_id uuid NOT NULL,
  invoice_id uuid,
  rate_card_rate_id uuid,
  rate_phase_id uuid,
  pricing_unit_id uuid,
  cycle_started_at timestamptz NOT NULL,
  started_at timestamptz NOT NULL,
  ended_at timestamptz NOT NULL,
  billing_at timestamptz NOT NULL,
  rate_model text NOT NULL,
  rate_properties json NOT NULL,
  currency text NOT NULL,
  proration_ratio numeric NOT NULL,
  status text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT billing_segments_subscription_fkey FOREIGN KEY (organization_id, subscription_id) REFERENCES subscriptions (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT billing_segments_customer_fkey FOREIGN KEY (organization_id, customer_id) REFERENCES customers (organization_id, id),
  CONSTRAINT billing_segments_card_fkey FOREIGN KEY (organization_id, subscription_rate_card_id) REFERENCES subscription_rate_cards (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT billing_segments_invoice_fkey FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id) ON DELETE SET NULL (invoice_id),
  CONSTRAINT billing_segments_rate_card_rate_fkey FOREIGN KEY (organization_id, rate_card_rate_id) REFERENCES rate_card_rates (organization_id, id),
  CONSTRAINT billing_segments_rate_phase_fkey FOREIGN KEY (organization_id, rate_phase_id) REFERENCES subscription_rate_phases (organization_id, id),
  CONSTRAINT billing_segments_status_check CHECK (status IN ('pending', 'processing', 'done', 'failed')),
  CONSTRAINT billing_segments_rate_check CHECK (rate_card_rate_id IS NOT NULL OR rate_phase_id IS NOT NULL),
  CONSTRAINT billing_segments_period_check CHECK (started_at <= ended_at AND cycle_started_at <= started_at),
  CONSTRAINT billing_segments_proration_ratio_check CHECK (proration_ratio >= 0 AND proration_ratio <= 1),
  CONSTRAINT billing_segments_currency_check CHECK (currency <> '')
);

CREATE UNIQUE INDEX billing_segments_started_at_key_idx ON billing_segments (organization_id, subscription_rate_card_id, started_at);
CREATE INDEX billing_segments_customer_status_idx ON billing_segments (organization_id, customer_id, status);
CREATE INDEX billing_segments_subscription_idx ON billing_segments (organization_id, subscription_id);
CREATE INDEX billing_segments_invoice_idx ON billing_segments (organization_id, invoice_id) WHERE invoice_id IS NOT NULL;

ALTER TABLE billing_segments ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON billing_segments
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
