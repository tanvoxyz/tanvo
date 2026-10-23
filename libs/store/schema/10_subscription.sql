-- Subscriptions, activation rules, fixed charge events, applied usage thresholds, lifetime usage (domain §9, §7.5, §7.9, §10.3).

-- Subscriptions (domain §9.1). external_id is unique per status among the
-- live statuses (pending, incomplete, active): a downgrade's pending
-- subscription shares its external ID with the active one it replaces.
-- A chain of upgrades and downgrades links each subscription to the one it
-- replaces through previous_subscription_id. The payment method foreign
-- key is added in 90_fk_subscription.sql. last_received_event_on is the day,
-- in the customer's time zone, an active subscription last received usage;
-- only ingestion writes it (the daily usage selection).
CREATE TABLE subscriptions (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  external_id text NOT NULL,
  customer_id uuid NOT NULL,
  plan_id uuid NOT NULL,
  name text,
  billing_time text NOT NULL,
  status text NOT NULL,
  subscription_at timestamptz NOT NULL,
  started_at timestamptz,
  ending_at timestamptz,
  terminated_at timestamptz,
  canceled_at timestamptz,
  trial_ended_at timestamptz,
  cancellation_reason text,
  previous_subscription_id uuid,
  on_termination_credit_note text,
  on_termination_invoice text NOT NULL,
  payment_method_type text NOT NULL,
  payment_method_id uuid,
  progressive_billing_disabled boolean NOT NULL,
  consolidate_invoice boolean NOT NULL,
  purchase_order_number text,
  billing_entity_id uuid,
  skip_invoice_custom_sections boolean NOT NULL,
  next_billing_at timestamptz,
  skip_daily_usage boolean NOT NULL DEFAULT false,
  last_received_event_on date,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT subscriptions_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT subscriptions_customer_fkey FOREIGN KEY (organization_id, customer_id) REFERENCES customers (organization_id, id),
  CONSTRAINT subscriptions_plan_fkey FOREIGN KEY (organization_id, plan_id) REFERENCES plans (organization_id, id),
  CONSTRAINT subscriptions_previous_subscription_fkey FOREIGN KEY (organization_id, previous_subscription_id) REFERENCES subscriptions (organization_id, id),
  CONSTRAINT subscriptions_billing_entity_fkey FOREIGN KEY (organization_id, billing_entity_id) REFERENCES billing_entities (organization_id, id),
  CONSTRAINT subscriptions_billing_time_check CHECK (billing_time IN ('calendar', 'anniversary')),
  CONSTRAINT subscriptions_status_check CHECK (status IN ('pending', 'incomplete', 'active', 'terminated', 'canceled')),
  CONSTRAINT subscriptions_cancellation_reason_check CHECK (cancellation_reason IN ('payment_failed', 'timeout', 'manual')),
  CONSTRAINT subscriptions_on_termination_credit_note_check CHECK (on_termination_credit_note IN ('credit', 'refund', 'offset', 'skip')),
  CONSTRAINT subscriptions_on_termination_invoice_check CHECK (on_termination_invoice IN ('generate', 'skip')),
  CONSTRAINT subscriptions_payment_method_type_check CHECK (payment_method_type IN ('provider', 'manual'))
);

CREATE UNIQUE INDEX subscriptions_external_id_key ON subscriptions (organization_id, external_id, status)
  WHERE status IN ('pending', 'incomplete', 'active');
CREATE INDEX subscriptions_external_id_idx ON subscriptions (organization_id, external_id);
CREATE INDEX subscriptions_customer_idx ON subscriptions (organization_id, customer_id);
CREATE INDEX subscriptions_plan_idx ON subscriptions (organization_id, plan_id);
CREATE INDEX subscriptions_previous_subscription_idx ON subscriptions (organization_id, previous_subscription_id)
  WHERE previous_subscription_id IS NOT NULL;
CREATE INDEX subscriptions_billing_entity_idx ON subscriptions (organization_id, billing_entity_id)
  WHERE billing_entity_id IS NOT NULL;
CREATE INDEX subscriptions_payment_method_idx ON subscriptions (organization_id, payment_method_id)
  WHERE payment_method_id IS NOT NULL;
CREATE INDEX subscriptions_next_billing_at_idx ON subscriptions (organization_id, next_billing_at)
  WHERE status = 'active' AND next_billing_at IS NOT NULL;
CREATE INDEX subscriptions_subscription_at_idx ON subscriptions (organization_id, subscription_at)
  WHERE status = 'pending' AND previous_subscription_id IS NULL;
CREATE INDEX subscriptions_ending_at_idx ON subscriptions (organization_id, ending_at)
  WHERE status = 'active' AND ending_at IS NOT NULL;

ALTER TABLE subscriptions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON subscriptions
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The invoice custom sections selected by a subscription (domain §11.9), in
-- their order. Deleting a section removes it from the selections.
CREATE TABLE subscription_invoice_custom_sections (
  organization_id uuid NOT NULL,
  subscription_id uuid NOT NULL,
  invoice_custom_section_id uuid NOT NULL,
  position integer NOT NULL,
  PRIMARY KEY (organization_id, subscription_id, invoice_custom_section_id),
  CONSTRAINT subscription_invoice_custom_sections_subscription_fkey FOREIGN KEY (organization_id, subscription_id) REFERENCES subscriptions (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT subscription_invoice_custom_sections_section_fkey FOREIGN KEY (organization_id, invoice_custom_section_id) REFERENCES invoice_custom_sections (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX subscription_invoice_custom_sections_section_idx ON subscription_invoice_custom_sections (organization_id, invoice_custom_section_id);

ALTER TABLE subscription_invoice_custom_sections ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON subscription_invoice_custom_sections
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Activation rules (domain §9.3).
CREATE TABLE subscription_activation_rules (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  subscription_id uuid NOT NULL,
  rule_type text NOT NULL,
  timeout_hours integer NOT NULL,
  status text NOT NULL,
  expires_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT subscription_activation_rules_subscription_fkey FOREIGN KEY (organization_id, subscription_id) REFERENCES subscriptions (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT subscription_activation_rules_rule_type_check CHECK (rule_type IN ('payment')),
  CONSTRAINT subscription_activation_rules_timeout_hours_check CHECK (timeout_hours >= 0),
  CONSTRAINT subscription_activation_rules_status_check CHECK (status IN ('inactive', 'pending', 'satisfied', 'not_applicable', 'declined', 'failed', 'expired'))
);

CREATE INDEX subscription_activation_rules_subscription_idx ON subscription_activation_rules (organization_id, subscription_id);
CREATE INDEX subscription_activation_rules_expires_at_idx ON subscription_activation_rules (organization_id, expires_at)
  WHERE status = 'pending' AND expires_at IS NOT NULL;

ALTER TABLE subscription_activation_rules ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON subscription_activation_rules
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Fixed charge unit changes of a subscription (domain §7.5). At most one
-- event per subscription, fixed charge and day (in the customer's time
-- zone) is kept; the repository removes the earlier one, since the day
-- depends on a time zone the row does not hold. The events of a deleted
-- fixed charge go with it.
CREATE TABLE fixed_charge_events (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  subscription_id uuid NOT NULL,
  fixed_charge_id uuid NOT NULL,
  units numeric NOT NULL,
  timestamp timestamptz NOT NULL,
  apply_units_immediately boolean NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT fixed_charge_events_subscription_fkey FOREIGN KEY (organization_id, subscription_id) REFERENCES subscriptions (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT fixed_charge_events_fixed_charge_fkey FOREIGN KEY (organization_id, fixed_charge_id) REFERENCES fixed_charges (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT fixed_charge_events_units_check CHECK (units >= 0)
);

CREATE INDEX fixed_charge_events_subscription_idx ON fixed_charge_events (organization_id, subscription_id, fixed_charge_id, timestamp);
CREATE INDEX fixed_charge_events_fixed_charge_idx ON fixed_charge_events (organization_id, fixed_charge_id);

ALTER TABLE fixed_charge_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON fixed_charge_events
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Units of a fixed charge of the original plan overridden for one
-- subscription, without overriding the plan (domain §7.5). At most one
-- kept override per subscription and fixed charge; an override is
-- soft-deleted when it moves onto an override plan.
CREATE TABLE fixed_charge_units_overrides (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  subscription_id uuid NOT NULL,
  fixed_charge_id uuid NOT NULL,
  units numeric NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT fixed_charge_units_overrides_subscription_fkey FOREIGN KEY (organization_id, subscription_id) REFERENCES subscriptions (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT fixed_charge_units_overrides_fixed_charge_fkey FOREIGN KEY (organization_id, fixed_charge_id) REFERENCES fixed_charges (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT fixed_charge_units_overrides_units_check CHECK (units >= 0)
);

CREATE UNIQUE INDEX fixed_charge_units_overrides_kept_key ON fixed_charge_units_overrides (organization_id, subscription_id, fixed_charge_id)
  WHERE deleted_at IS NULL;
CREATE INDEX fixed_charge_units_overrides_fixed_charge_idx ON fixed_charge_units_overrides (organization_id, fixed_charge_id)
  WHERE deleted_at IS NULL;

ALTER TABLE fixed_charge_units_overrides ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON fixed_charge_units_overrides
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Usage thresholds reached by subscriptions and the progressive-billing
-- invoices they produced (domain §7.9, §22.16); one record per threshold
-- and invoice. A record goes with its threshold or its (draft) invoice.
-- The invoice foreign key is added in 90_fk_subscription.sql.
CREATE TABLE applied_usage_thresholds (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  usage_threshold_id uuid NOT NULL,
  subscription_id uuid NOT NULL,
  invoice_id uuid NOT NULL,
  lifetime_usage_amount bigint NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT applied_usage_thresholds_usage_threshold_fkey FOREIGN KEY (organization_id, usage_threshold_id) REFERENCES usage_thresholds (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT applied_usage_thresholds_subscription_fkey FOREIGN KEY (organization_id, subscription_id) REFERENCES subscriptions (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT applied_usage_thresholds_invoice_threshold_key UNIQUE (organization_id, invoice_id, usage_threshold_id),
  CONSTRAINT applied_usage_thresholds_lifetime_usage_amount_check CHECK (lifetime_usage_amount >= 0)
);

CREATE INDEX applied_usage_thresholds_usage_threshold_idx ON applied_usage_thresholds (organization_id, usage_threshold_id);
CREATE INDEX applied_usage_thresholds_subscription_idx ON applied_usage_thresholds (organization_id, subscription_id);

ALTER TABLE applied_usage_thresholds ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON applied_usage_thresholds
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Lifetime usage of subscription chains (domain §10.3): one record per
-- external subscription ID, pointing at the chain's current subscription.
CREATE TABLE lifetime_usages (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  subscription_id uuid NOT NULL,
  external_subscription_id text NOT NULL,
  currency text NOT NULL,
  historical_usage_amount bigint NOT NULL,
  invoiced_usage_amount bigint NOT NULL,
  current_usage_amount bigint NOT NULL,
  recalculate_invoiced_usage boolean NOT NULL,
  recalculate_current_usage boolean NOT NULL,
  invoiced_usage_amount_refreshed_at timestamptz,
  current_usage_amount_refreshed_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT lifetime_usages_subscription_fkey FOREIGN KEY (organization_id, subscription_id) REFERENCES subscriptions (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT lifetime_usages_external_subscription_id_key UNIQUE (organization_id, external_subscription_id),
  CONSTRAINT lifetime_usages_historical_usage_amount_check CHECK (historical_usage_amount >= 0),
  CONSTRAINT lifetime_usages_invoiced_usage_amount_check CHECK (invoiced_usage_amount >= 0),
  CONSTRAINT lifetime_usages_current_usage_amount_check CHECK (current_usage_amount >= 0)
);

CREATE INDEX lifetime_usages_subscription_idx ON lifetime_usages (organization_id, subscription_id);
CREATE INDEX lifetime_usages_recalculate_idx ON lifetime_usages (organization_id, id)
  WHERE recalculate_invoiced_usage OR recalculate_current_usage;

ALTER TABLE lifetime_usages ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON lifetime_usages
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
