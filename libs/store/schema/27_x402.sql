-- x402 connections and settlements (parity §1.36).
--
-- A connection is an organization's account at an x402 facilitator; a
-- settlement is one attempt to settle a verified payment through it. The
-- invoices' x402_connection_id (12_invoice.sql) gets its foreign key here.

-- An organization's account at a facilitator: the networks and the token it
-- accepts, the payout address of each network family, and the sealed
-- credentials (x402.Credentials, sealed per organization). One live
-- connection per code.
CREATE TABLE x402_connections (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  facilitator text NOT NULL DEFAULT 'coinbase_cdp',
  asset text NOT NULL DEFAULT 'usdc',
  networks text[] NOT NULL DEFAULT '{}',
  -- The payout address of each family: {"evm": "0x…", "svm": "…"}.
  payout_addresses jsonb NOT NULL,
  auto_create_customers boolean NOT NULL DEFAULT true,
  secrets bytea NOT NULL,
  deleted_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT x402_connections_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT x402_connections_facilitator_check CHECK (facilitator IN ('coinbase_cdp')),
  CONSTRAINT x402_connections_asset_check CHECK (asset IN ('usdc')),
  CONSTRAINT x402_connections_payout_addresses_check CHECK (jsonb_typeof(payout_addresses) = 'object')
);

CREATE UNIQUE INDEX x402_connections_code_live_key ON x402_connections (organization_id, code) WHERE deleted_at IS NULL;

ALTER TABLE x402_connections ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON x402_connections
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- One attempt to settle a verified payment: its verified facts (payer,
-- payee, amount, digest, settings) never change; its status, references,
-- hash, reconciliation time, payload and error reason do. The payment
-- digest and the transaction hash are unique across organizations: one
-- transfer is settled once.
CREATE TABLE x402_settlements (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  x402_connection_id uuid NOT NULL,
  customer_id uuid,
  subscription_id uuid,
  wallet_transaction_id uuid,
  payment_id uuid,
  invoice_id uuid,
  kind text NOT NULL,
  status text NOT NULL DEFAULT 'pending',
  settled_by text NOT NULL DEFAULT 'tanvo',
  network text NOT NULL,
  asset text NOT NULL,
  payer_address text NOT NULL,
  payee_address text NOT NULL,
  settled_amount_atomic numeric(38, 0) NOT NULL,
  settled_amount_cents bigint NOT NULL,
  transaction_hash text,
  reconcile_after timestamptz,
  payment_digest text NOT NULL,
  purchase_settings jsonb,
  payload jsonb NOT NULL DEFAULT '{}',
  error_reason text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT x402_settlements_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT x402_settlements_connection_fkey FOREIGN KEY (organization_id, x402_connection_id) REFERENCES x402_connections (organization_id, id),
  CONSTRAINT x402_settlements_customer_fkey FOREIGN KEY (organization_id, customer_id) REFERENCES customers (organization_id, id),
  CONSTRAINT x402_settlements_subscription_fkey FOREIGN KEY (organization_id, subscription_id) REFERENCES subscriptions (organization_id, id),
  CONSTRAINT x402_settlements_wallet_transaction_fkey FOREIGN KEY (organization_id, wallet_transaction_id) REFERENCES wallet_transactions (organization_id, id),
  CONSTRAINT x402_settlements_payment_fkey FOREIGN KEY (organization_id, payment_id) REFERENCES payments (organization_id, id),
  CONSTRAINT x402_settlements_invoice_fkey FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id),
  CONSTRAINT x402_settlements_kind_check CHECK (kind IN ('credit_purchase', 'invoice_payment')),
  CONSTRAINT x402_settlements_status_check CHECK (status IN ('pending', 'settled', 'failed')),
  CONSTRAINT x402_settlements_settled_by_check CHECK (settled_by IN ('tanvo', 'merchant')),
  CONSTRAINT x402_settlements_purchase_settings_check CHECK (purchase_settings IS NULL OR jsonb_typeof(purchase_settings) = 'object'),
  CONSTRAINT x402_settlements_payload_check CHECK (jsonb_typeof(payload) = 'object'),
  CONSTRAINT x402_settlements_amount_positive_check CHECK (settled_amount_atomic > 0),
  CONSTRAINT x402_settlements_credit_purchase_has_settings_check CHECK (kind <> 'credit_purchase' OR purchase_settings IS NOT NULL),
  CONSTRAINT x402_settlements_granted_has_subscription_check CHECK (wallet_transaction_id IS NULL OR subscription_id IS NOT NULL),
  CONSTRAINT x402_settlements_invoice_payment_has_invoice_check CHECK (kind <> 'invoice_payment' OR invoice_id IS NOT NULL),
  CONSTRAINT x402_settlements_merchant_settled_credit_purchase_check CHECK (settled_by = 'tanvo' OR (kind = 'credit_purchase' AND status = 'settled')),
  CONSTRAINT x402_settlements_pending_has_reconcile_after_check CHECK (status <> 'pending' OR reconcile_after IS NOT NULL),
  CONSTRAINT x402_settlements_settled_has_hash_check CHECK (status <> 'settled' OR transaction_hash IS NOT NULL)
);

-- One live attempt per payment, across organizations.
CREATE UNIQUE INDEX x402_settlements_payment_digest_key ON x402_settlements (payment_digest) WHERE status IN ('pending', 'settled');
-- One settlement per transfer, across organizations.
CREATE UNIQUE INDEX x402_settlements_transaction_hash_key ON x402_settlements (network, transaction_hash) WHERE transaction_hash IS NOT NULL;
-- One pending credit purchase per payer.
CREATE UNIQUE INDEX x402_settlements_pending_credit_purchase_payer_key ON x402_settlements (organization_id, payer_address)
  WHERE kind = 'credit_purchase' AND status = 'pending';
-- One pending attempt per invoice.
CREATE UNIQUE INDEX x402_settlements_pending_invoice_key ON x402_settlements (organization_id, invoice_id) WHERE status = 'pending';
-- One settlement per payment.
CREATE UNIQUE INDEX x402_settlements_payment_key ON x402_settlements (organization_id, payment_id) WHERE payment_id IS NOT NULL;
CREATE INDEX x402_settlements_connection_idx ON x402_settlements (organization_id, x402_connection_id);
CREATE INDEX x402_settlements_customer_idx ON x402_settlements (organization_id, customer_id) WHERE customer_id IS NOT NULL;
CREATE INDEX x402_settlements_invoice_idx ON x402_settlements (organization_id, invoice_id) WHERE invoice_id IS NOT NULL;
CREATE INDEX x402_settlements_pending_reconcile_after_idx ON x402_settlements (organization_id, reconcile_after) WHERE status = 'pending';

ALTER TABLE x402_settlements ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON x402_settlements
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The connection an invoice is paid through.
ALTER TABLE invoices
  ADD CONSTRAINT invoices_x402_connection_fkey FOREIGN KEY (organization_id, x402_connection_id) REFERENCES x402_connections (organization_id, id);

CREATE INDEX invoices_x402_connection_idx ON invoices (organization_id, x402_connection_id) WHERE x402_connection_id IS NOT NULL;
