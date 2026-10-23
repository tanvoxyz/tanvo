-- The product catalog (parity §1.14, domain §21.4): product categories,
-- products and their filters, rate cards with their rates and taxes, and
-- the rate cards of plans with their phases.
--
-- Every table is soft-deleted (lago discard on deleted_at): a discarded
-- row is kept with deleted_at set for history, codes and positions are
-- unique among kept rows (partial unique indexes), and the services
-- cascade discards as lago's destroy services do.

CREATE TABLE product_categories (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  description text,
  invoice_display_name text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT product_categories_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id)
);

CREATE UNIQUE INDEX product_categories_code_idx ON product_categories (organization_id, code) WHERE deleted_at IS NULL;

ALTER TABLE product_categories ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_categories
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- A metered product sells a metric; a fixed one none (product.rb:62-70).
CREATE TABLE products (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  product_category_id uuid,
  billable_metric_id uuid,
  product_type text NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  description text,
  invoice_display_name text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT products_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT products_product_category_fkey FOREIGN KEY (organization_id, product_category_id) REFERENCES product_categories (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT products_billable_metric_fkey FOREIGN KEY (organization_id, billable_metric_id) REFERENCES billable_metrics (organization_id, id),
  CONSTRAINT products_product_type_check CHECK (product_type IN ('metered', 'fixed')),
  CONSTRAINT products_billable_metric_check CHECK ((product_type = 'metered') = (billable_metric_id IS NOT NULL))
);

CREATE UNIQUE INDEX products_code_idx ON products (organization_id, code) WHERE deleted_at IS NULL;
CREATE INDEX products_product_category_idx ON products (organization_id, product_category_id);
CREATE INDEX products_billable_metric_idx ON products (organization_id, billable_metric_id);

ALTER TABLE products ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON products
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE product_filters (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  product_id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  description text,
  invoice_display_name text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT product_filters_product_fkey FOREIGN KEY (organization_id, product_id) REFERENCES products (organization_id, id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX product_filters_code_idx ON product_filters (organization_id, product_id, code) WHERE deleted_at IS NULL;

ALTER TABLE product_filters ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_filters
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The metric filter values of a product filter, in their order; a NULL
-- value selects every value of the metric filter. The metric filter is not
-- a foreign key: a metric's filters are rewritten with their IDs kept, and
-- the metric services refuse to drop one a product filter uses. Changed
-- values discard the kept rows and insert new ones (lago replace_values),
-- so a row is keyed by a store-generated id.
CREATE TABLE product_filter_values (
  organization_id uuid NOT NULL,
  id bigint GENERATED ALWAYS AS IDENTITY,
  product_filter_id uuid NOT NULL,
  position integer NOT NULL,
  billable_metric_filter_id uuid NOT NULL,
  value text,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT product_filter_values_product_filter_fkey FOREIGN KEY (organization_id, product_filter_id) REFERENCES product_filters (organization_id, id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX product_filter_values_position_idx ON product_filter_values (organization_id, product_filter_id, position) WHERE deleted_at IS NULL;

CREATE INDEX product_filter_values_billable_metric_filter_idx ON product_filter_values (organization_id, billable_metric_filter_id);

ALTER TABLE product_filter_values ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_filter_values
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE rate_cards (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  product_id uuid NOT NULL,
  product_filter_id uuid,
  code text NOT NULL,
  name text NOT NULL,
  description text,
  currency text NOT NULL,
  billing_timing text NOT NULL,
  proration boolean NOT NULL,
  display_on_invoice boolean NOT NULL,
  regroup_paid_fees text,
  pricing_unit_id uuid,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT rate_cards_product_fkey FOREIGN KEY (organization_id, product_id) REFERENCES products (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT rate_cards_product_filter_fkey FOREIGN KEY (organization_id, product_filter_id) REFERENCES product_filters (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT rate_cards_pricing_unit_fkey FOREIGN KEY (organization_id, pricing_unit_id) REFERENCES pricing_units (organization_id, id),
  CONSTRAINT rate_cards_billing_timing_check CHECK (billing_timing IN ('arrears', 'advance')),
  CONSTRAINT rate_cards_regroup_paid_fees_check CHECK (regroup_paid_fees IN ('invoice'))
);

CREATE UNIQUE INDEX rate_cards_code_idx ON rate_cards (organization_id, code) WHERE deleted_at IS NULL;
CREATE INDEX rate_cards_product_idx ON rate_cards (organization_id, product_id);
CREATE INDEX rate_cards_product_filter_idx ON rate_cards (organization_id, product_filter_id);

ALTER TABLE rate_cards ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON rate_cards
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE rate_card_taxes (
  organization_id uuid NOT NULL,
  rate_card_id uuid NOT NULL,
  tax_id uuid NOT NULL,
  position integer NOT NULL,
  PRIMARY KEY (organization_id, rate_card_id, tax_id),
  CONSTRAINT rate_card_taxes_rate_card_fkey FOREIGN KEY (organization_id, rate_card_id) REFERENCES rate_cards (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT rate_card_taxes_tax_fkey FOREIGN KEY (organization_id, tax_id) REFERENCES taxes (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX rate_card_taxes_tax_idx ON rate_card_taxes (organization_id, tax_id);

ALTER TABLE rate_card_taxes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON rate_card_taxes
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The rate timeline of a card: codes and effective instants are unique
-- among the card's kept rates (rate_card_rate.rb:46, :134-140).
CREATE TABLE rate_card_rates (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  rate_card_id uuid NOT NULL,
  code text NOT NULL,
  effective_from timestamptz NOT NULL,
  rate_model text NOT NULL,
  properties json NOT NULL,
  min_amount bigint NOT NULL,
  billing_interval_count integer NOT NULL,
  billing_interval_unit text NOT NULL,
  pricing_unit_conversion_rate numeric,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT rate_card_rates_rate_card_fkey FOREIGN KEY (organization_id, rate_card_id) REFERENCES rate_cards (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT rate_card_rates_rate_model_check CHECK (rate_model IN ('standard', 'graduated', 'graduated_percentage', 'package', 'percentage', 'volume', 'dynamic')),
  CONSTRAINT rate_card_rates_billing_interval_unit_check CHECK (billing_interval_unit IN ('day', 'week', 'month', 'year')),
  CONSTRAINT rate_card_rates_min_amount_check CHECK (min_amount >= 0),
  CONSTRAINT rate_card_rates_billing_interval_count_check CHECK (billing_interval_count >= 1),
  CONSTRAINT rate_card_rates_pricing_unit_conversion_rate_check CHECK (pricing_unit_conversion_rate > 0)
);

CREATE UNIQUE INDEX rate_card_rates_code_idx ON rate_card_rates (organization_id, rate_card_id, code) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX rate_card_rates_effective_from_idx ON rate_card_rates (organization_id, rate_card_id, effective_from) WHERE deleted_at IS NULL;

ALTER TABLE rate_card_rates ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON rate_card_rates
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- A card is applied once per plan among the kept rows (plan_rate_card.rb:17).
CREATE TABLE plan_rate_cards (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  plan_id uuid NOT NULL,
  rate_card_id uuid NOT NULL,
  units numeric,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT plan_rate_cards_plan_fkey FOREIGN KEY (organization_id, plan_id) REFERENCES plans (organization_id, id),
  CONSTRAINT plan_rate_cards_rate_card_fkey FOREIGN KEY (organization_id, rate_card_id) REFERENCES rate_cards (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT plan_rate_cards_units_check CHECK (units >= 0)
);

CREATE UNIQUE INDEX plan_rate_cards_rate_card_key_idx ON plan_rate_cards (organization_id, plan_id, rate_card_id) WHERE deleted_at IS NULL;
CREATE INDEX plan_rate_cards_rate_card_idx ON plan_rate_cards (organization_id, rate_card_id);

ALTER TABLE plan_rate_cards ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON plan_rate_cards
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The phases of a plan rate card, with their rate overrides embedded
-- (override_rate_model NULL for none). Positions run from 1; the services
-- keep them contiguous and the indefinite phase last.
CREATE TABLE rate_phases (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  plan_rate_card_id uuid NOT NULL,
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
  CONSTRAINT rate_phases_plan_rate_card_fkey FOREIGN KEY (organization_id, plan_rate_card_id) REFERENCES plan_rate_cards (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT rate_phases_position_check CHECK (position > 0),
  CONSTRAINT rate_phases_billing_interval_cycle_count_check CHECK (billing_interval_cycle_count > 0),
  CONSTRAINT rate_phases_override_check CHECK ((override_rate_model IS NULL) = (override_properties IS NULL)),
  CONSTRAINT rate_phases_override_rate_model_check CHECK (override_rate_model IN ('standard', 'graduated', 'graduated_percentage', 'package', 'percentage', 'volume', 'dynamic')),
  CONSTRAINT rate_phases_override_billing_interval_unit_check CHECK (override_billing_interval_unit IN ('day', 'week', 'month', 'year'))
);

CREATE UNIQUE INDEX rate_phases_code_idx ON rate_phases (organization_id, plan_rate_card_id, code) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX rate_phases_position_idx ON rate_phases (organization_id, plan_rate_card_id, position) WHERE deleted_at IS NULL;

ALTER TABLE rate_phases ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON rate_phases
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
