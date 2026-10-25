-- Wallets, wallet transactions, recurring transaction rules (domain §14).

CREATE TABLE wallets (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  customer_id uuid NOT NULL,
  code text,
  name text,
  status text NOT NULL,
  currency text NOT NULL,
  rate_amount numeric NOT NULL,
  priority integer NOT NULL,
  expiration_at timestamptz,
  balance_amount bigint NOT NULL DEFAULT 0,
  credits_balance numeric NOT NULL DEFAULT 0,
  ongoing_balance_amount bigint NOT NULL DEFAULT 0,
  credits_ongoing_balance numeric NOT NULL DEFAULT 0,
  ongoing_usage_balance_amount bigint NOT NULL DEFAULT 0,
  credits_ongoing_usage_balance numeric NOT NULL DEFAULT 0,
  consumed_amount bigint NOT NULL DEFAULT 0,
  consumed_credits numeric NOT NULL DEFAULT 0,
  paid_top_up_min_amount bigint,
  paid_top_up_max_amount bigint,
  invoice_requires_successful_payment boolean NOT NULL DEFAULT false,
  allowed_fee_types text[] NOT NULL DEFAULT '{}',
  traceable boolean NOT NULL DEFAULT false,
  purchase_order_number text,
  x402_enabled boolean NOT NULL DEFAULT false,
  billing_entity_id uuid,
  payment_method_type text,
  -- References payment_methods; the foreign key is added in 90_fk_wallet.sql.
  payment_method_id uuid,
  last_balance_sync_at timestamptz,
  last_consumed_credit_at timestamptz,
  depleted_ongoing_balance boolean NOT NULL DEFAULT false,
  metadata jsonb NOT NULL DEFAULT '[]',
  -- The invoice custom sections the credit invoices print (lago
  -- invoice_custom_section); sections are soft-deleted, never removed, so
  -- the IDs have no foreign key and deleted ones are ignored.
  skip_invoice_custom_sections boolean NOT NULL DEFAULT false,
  invoice_custom_section_ids uuid[] NOT NULL DEFAULT '{}',
  terminated_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT wallets_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT wallets_customer_fkey FOREIGN KEY (organization_id, customer_id) REFERENCES customers (organization_id, id),
  CONSTRAINT wallets_billing_entity_fkey FOREIGN KEY (organization_id, billing_entity_id) REFERENCES billing_entities (organization_id, id),
  CONSTRAINT wallets_status_check CHECK (status IN ('active', 'terminated')),
  CONSTRAINT wallets_rate_amount_check CHECK (rate_amount > 0),
  CONSTRAINT wallets_metadata_check CHECK (jsonb_typeof(metadata) = 'array'),
  CONSTRAINT wallets_priority_check CHECK (priority BETWEEN 1 AND 50),
  CONSTRAINT wallets_paid_top_up_min_amount_check CHECK (paid_top_up_min_amount > 0),
  CONSTRAINT wallets_paid_top_up_max_amount_check CHECK (paid_top_up_max_amount > 0),
  CONSTRAINT wallets_allowed_fee_types_check CHECK (allowed_fee_types <@ ARRAY['subscription', 'charge', 'fixed_charge', 'add_on', 'credit', 'commitment', 'product']::text[]),
  CONSTRAINT wallets_payment_method_type_check CHECK (payment_method_type IN ('provider', 'manual'))
);

-- Codes are unique among the customer's active wallets.
CREATE UNIQUE INDEX wallets_code_active_key ON wallets (organization_id, customer_id, code) WHERE status = 'active' AND code IS NOT NULL;
CREATE INDEX wallets_customer_idx ON wallets (organization_id, customer_id);
CREATE INDEX wallets_x402_enabled_idx ON wallets (organization_id, customer_id) WHERE x402_enabled;
CREATE INDEX wallets_billing_entity_idx ON wallets (organization_id, billing_entity_id) WHERE billing_entity_id IS NOT NULL;
CREATE INDEX wallets_payment_method_idx ON wallets (organization_id, payment_method_id) WHERE payment_method_id IS NOT NULL;
-- Wallets due for termination at their expiration.
CREATE INDEX wallets_expiration_idx ON wallets (organization_id, expiration_at) WHERE status = 'active' AND expiration_at IS NOT NULL;

ALTER TABLE wallets ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON wallets
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The billable metrics a wallet is limited to, in their order. Deleting a
-- metric removes it from the wallets' targets.
CREATE TABLE wallet_billable_metrics (
  organization_id uuid NOT NULL,
  wallet_id uuid NOT NULL,
  billable_metric_id uuid NOT NULL,
  position integer NOT NULL,
  PRIMARY KEY (organization_id, wallet_id, billable_metric_id),
  CONSTRAINT wallet_billable_metrics_wallet_fkey FOREIGN KEY (organization_id, wallet_id) REFERENCES wallets (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT wallet_billable_metrics_billable_metric_fkey FOREIGN KEY (organization_id, billable_metric_id) REFERENCES billable_metrics (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX wallet_billable_metrics_billable_metric_idx ON wallet_billable_metrics (organization_id, billable_metric_id);

ALTER TABLE wallet_billable_metrics ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON wallet_billable_metrics
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE recurring_transaction_rules (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  wallet_id uuid NOT NULL,
  "trigger" text NOT NULL,
  "interval" text,
  threshold_credits numeric,
  method text NOT NULL,
  paid_credits numeric NOT NULL DEFAULT 0,
  granted_credits numeric NOT NULL DEFAULT 0,
  target_ongoing_balance numeric,
  grants_target_top_up boolean NOT NULL DEFAULT false,
  started_at timestamptz,
  expiration_at timestamptz,
  status text NOT NULL,
  invoice_requires_successful_payment boolean NOT NULL DEFAULT false,
  transaction_metadata jsonb NOT NULL DEFAULT '[]',
  transaction_name text,
  ignore_paid_top_up_limits boolean NOT NULL DEFAULT false,
  purchase_order_number text,
  payment_method_type text,
  -- References payment_methods; the foreign key is added in 90_fk_wallet.sql.
  payment_method_id uuid,
  -- The invoice custom sections the top-up transactions take (lago
  -- invoice_custom_section); sections are soft-deleted, never removed, so
  -- the IDs have no foreign key and deleted ones are ignored.
  skip_invoice_custom_sections boolean NOT NULL DEFAULT false,
  invoice_custom_section_ids uuid[] NOT NULL DEFAULT '{}',
  terminated_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT recurring_transaction_rules_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT recurring_transaction_rules_wallet_fkey FOREIGN KEY (organization_id, wallet_id) REFERENCES wallets (organization_id, id),
  CONSTRAINT recurring_transaction_rules_trigger_check CHECK ("trigger" IN ('interval', 'threshold')),
  CONSTRAINT recurring_transaction_rules_interval_check CHECK ("interval" IN ('weekly', 'monthly', 'quarterly', 'semiannual', 'yearly')),
  CONSTRAINT recurring_transaction_rules_method_check CHECK (method IN ('fixed', 'target')),
  CONSTRAINT recurring_transaction_rules_status_check CHECK (status IN ('active', 'terminated')),
  CONSTRAINT recurring_transaction_rules_threshold_credits_check CHECK (threshold_credits >= 0),
  CONSTRAINT recurring_transaction_rules_paid_credits_check CHECK (paid_credits >= 0),
  CONSTRAINT recurring_transaction_rules_granted_credits_check CHECK (granted_credits >= 0),
  CONSTRAINT recurring_transaction_rules_payment_method_type_check CHECK (payment_method_type IN ('provider', 'manual'))
);

CREATE INDEX recurring_transaction_rules_wallet_idx ON recurring_transaction_rules (organization_id, wallet_id);
CREATE INDEX recurring_transaction_rules_active_idx ON recurring_transaction_rules (organization_id, "trigger") WHERE status = 'active';
CREATE INDEX recurring_transaction_rules_payment_method_idx ON recurring_transaction_rules (organization_id, payment_method_id) WHERE payment_method_id IS NOT NULL;

ALTER TABLE recurring_transaction_rules ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON recurring_transaction_rules
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE wallet_transactions (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  wallet_id uuid NOT NULL,
  transaction_type text NOT NULL,
  transaction_status text NOT NULL,
  status text NOT NULL,
  source text NOT NULL,
  currency text NOT NULL,
  amount bigint NOT NULL,
  credit_amount numeric NOT NULL,
  remaining_amount bigint,
  priority integer NOT NULL,
  name text,
  invoice_id uuid,
  credit_note_id uuid,
  -- The rule that created the transaction. Deleting a rule keeps its
  -- transactions as history, so this column has no foreign key.
  recurring_transaction_rule_id uuid,
  metadata jsonb NOT NULL DEFAULT '[]',
  payment_method_type text,
  -- References payment_methods; the foreign key is added in 90_fk_wallet.sql.
  payment_method_id uuid,
  invoice_requires_successful_payment boolean NOT NULL DEFAULT false,
  purchase_order_number text,
  -- The voided invoice whose credits a granted transaction gives back.
  voided_invoice_id uuid,
  -- The billing entity snapshotted at creation (lago billing_entity_id):
  -- the wallet's, or a void's inbound transaction's; it issues the credit
  -- invoice.
  billing_entity_id uuid,
  -- The invoice custom sections the credit invoice prints (lago
  -- invoice_custom_section); sections are soft-deleted, never removed, so
  -- the IDs have no foreign key and deleted ones are ignored.
  skip_invoice_custom_sections boolean NOT NULL DEFAULT false,
  invoice_custom_section_ids uuid[] NOT NULL DEFAULT '{}',
  settled_at timestamptz,
  failed_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT wallet_transactions_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT wallet_transactions_wallet_fkey FOREIGN KEY (organization_id, wallet_id) REFERENCES wallets (organization_id, id),
  CONSTRAINT wallet_transactions_invoice_fkey FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id),
  CONSTRAINT wallet_transactions_credit_note_fkey FOREIGN KEY (organization_id, credit_note_id) REFERENCES credit_notes (organization_id, id),
  CONSTRAINT wallet_transactions_voided_invoice_fkey FOREIGN KEY (organization_id, voided_invoice_id) REFERENCES invoices (organization_id, id),
  CONSTRAINT wallet_transactions_billing_entity_fkey FOREIGN KEY (organization_id, billing_entity_id) REFERENCES billing_entities (organization_id, id),
  CONSTRAINT wallet_transactions_transaction_type_check CHECK (transaction_type IN ('inbound', 'outbound')),
  CONSTRAINT wallet_transactions_transaction_status_check CHECK (
    (transaction_type = 'inbound' AND transaction_status IN ('purchased', 'granted')) OR
    (transaction_type = 'outbound' AND transaction_status IN ('invoiced', 'voided'))
  ),
  CONSTRAINT wallet_transactions_status_check CHECK (status IN ('pending', 'settled', 'failed')),
  CONSTRAINT wallet_transactions_source_check CHECK (source IN ('manual', 'interval', 'threshold')),
  CONSTRAINT wallet_transactions_amount_check CHECK (amount >= 0),
  CONSTRAINT wallet_transactions_credit_amount_check CHECK (credit_amount >= 0),
  CONSTRAINT wallet_transactions_remaining_amount_check CHECK (remaining_amount IS NULL OR (transaction_type = 'inbound' AND remaining_amount >= 0)),
  CONSTRAINT wallet_transactions_priority_check CHECK (priority BETWEEN 1 AND 50),
  CONSTRAINT wallet_transactions_payment_method_type_check CHECK (payment_method_type IN ('provider', 'manual'))
);

CREATE INDEX wallet_transactions_wallet_idx ON wallet_transactions (organization_id, wallet_id);
CREATE INDEX wallet_transactions_invoice_idx ON wallet_transactions (organization_id, invoice_id) WHERE invoice_id IS NOT NULL;
CREATE INDEX wallet_transactions_voided_invoice_idx ON wallet_transactions (organization_id, voided_invoice_id) WHERE voided_invoice_id IS NOT NULL;
CREATE INDEX wallet_transactions_billing_entity_idx ON wallet_transactions (organization_id, billing_entity_id) WHERE billing_entity_id IS NOT NULL;
CREATE INDEX wallet_transactions_credit_note_idx ON wallet_transactions (organization_id, credit_note_id) WHERE credit_note_id IS NOT NULL;
CREATE INDEX wallet_transactions_payment_method_idx ON wallet_transactions (organization_id, payment_method_id) WHERE payment_method_id IS NOT NULL;
-- The pending purchases of recurring rules (threshold rules wait for them).
CREATE INDEX wallet_transactions_pending_rule_idx ON wallet_transactions (organization_id, recurring_transaction_rule_id) WHERE status = 'pending' AND recurring_transaction_rule_id IS NOT NULL;
-- The inbound transactions of traceable wallets with value left.
CREATE INDEX wallet_transactions_available_idx ON wallet_transactions (organization_id, wallet_id, priority) WHERE transaction_type = 'inbound' AND status = 'settled' AND remaining_amount > 0;

ALTER TABLE wallet_transactions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON wallet_transactions
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Links an outbound transaction of a traceable wallet to the inbound
-- transactions it consumed.
CREATE TABLE wallet_transaction_consumptions (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  inbound_transaction_id uuid NOT NULL,
  outbound_transaction_id uuid NOT NULL,
  consumed_amount bigint NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT wallet_transaction_consumptions_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT wallet_transaction_consumptions_inbound_fkey FOREIGN KEY (organization_id, inbound_transaction_id) REFERENCES wallet_transactions (organization_id, id),
  CONSTRAINT wallet_transaction_consumptions_outbound_fkey FOREIGN KEY (organization_id, outbound_transaction_id) REFERENCES wallet_transactions (organization_id, id),
  CONSTRAINT wallet_transaction_consumptions_transactions_key UNIQUE (organization_id, inbound_transaction_id, outbound_transaction_id),
  CONSTRAINT wallet_transaction_consumptions_consumed_amount_check CHECK (consumed_amount > 0)
);

CREATE INDEX wallet_transaction_consumptions_outbound_idx ON wallet_transaction_consumptions (organization_id, outbound_transaction_id);

ALTER TABLE wallet_transaction_consumptions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON wallet_transaction_consumptions
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
