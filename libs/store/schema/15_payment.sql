-- Payments, receipts, refunds, payment requests, methods, provider customers, intents, inbound webhooks (domain §15).
--
-- The provider connections are integrations of the payment category
-- (16_integration.sql, a later file): the foreign keys to integrations are
-- added in 90_fk_payment.sql. Payment history keeps its connection, so an
-- integration referenced by payment rows cannot be deleted.

-- A customer's identity at one provider connection (domain §15.5); one live
-- row per (customer, connection).
CREATE TABLE payment_provider_customers (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  customer_id uuid NOT NULL,
  integration_id uuid NOT NULL,
  provider_customer_id text,
  sync_with_provider boolean NOT NULL DEFAULT false,
  provider_payment_methods text[] NOT NULL DEFAULT '{}',
  deleted_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT payment_provider_customers_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT payment_provider_customers_customer_fkey FOREIGN KEY (organization_id, customer_id) REFERENCES customers (organization_id, id)
);

CREATE UNIQUE INDEX payment_provider_customers_customer_integration_live_key ON payment_provider_customers (organization_id, customer_id, integration_id) WHERE deleted_at IS NULL;
CREATE INDEX payment_provider_customers_integration_idx ON payment_provider_customers (organization_id, integration_id) WHERE deleted_at IS NULL;
CREATE INDEX payment_provider_customers_provider_customer_idx ON payment_provider_customers (organization_id, integration_id, provider_customer_id) WHERE deleted_at IS NULL;

ALTER TABLE payment_provider_customers ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON payment_provider_customers
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- A customer's payment method at a provider connection (domain §15.5);
-- details is the store-encoded payment.MethodDetails. At most one live
-- default per customer.
CREATE TABLE payment_methods (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  customer_id uuid NOT NULL,
  integration_id uuid,
  payment_provider_customer_id uuid,
  provider_method_id text NOT NULL,
  details jsonb NOT NULL,
  is_default boolean NOT NULL DEFAULT false,
  deleted_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT payment_methods_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT payment_methods_customer_fkey FOREIGN KEY (organization_id, customer_id) REFERENCES customers (organization_id, id),
  CONSTRAINT payment_methods_provider_customer_fkey FOREIGN KEY (organization_id, payment_provider_customer_id) REFERENCES payment_provider_customers (organization_id, id),
  CONSTRAINT payment_methods_provider_method_id_check CHECK (provider_method_id <> '')
);

CREATE UNIQUE INDEX payment_methods_default_live_key ON payment_methods (organization_id, customer_id) WHERE is_default AND deleted_at IS NULL;
CREATE INDEX payment_methods_customer_idx ON payment_methods (organization_id, customer_id);
CREATE INDEX payment_methods_provider_method_idx ON payment_methods (organization_id, integration_id, provider_method_id) WHERE deleted_at IS NULL;
CREATE INDEX payment_methods_provider_customer_idx ON payment_methods (organization_id, payment_provider_customer_id);

ALTER TABLE payment_methods ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON payment_methods
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Requests to pay several overdue invoices at once (domain §15.4); the
-- invoices are in payment_request_invoices.
CREATE TABLE payment_requests (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  customer_id uuid NOT NULL,
  amount bigint NOT NULL,
  currency text NOT NULL,
  email text,
  payment_status text NOT NULL,
  payment_attempts integer NOT NULL DEFAULT 0,
  dunning_campaign_id uuid,
  payment_method_type text,
  payment_method_id uuid,
  ready_for_payment_processing boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT payment_requests_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT payment_requests_customer_fkey FOREIGN KEY (organization_id, customer_id) REFERENCES customers (organization_id, id),
  CONSTRAINT payment_requests_dunning_campaign_fkey FOREIGN KEY (organization_id, dunning_campaign_id) REFERENCES dunning_campaigns (organization_id, id),
  CONSTRAINT payment_requests_payment_method_fkey FOREIGN KEY (organization_id, payment_method_id) REFERENCES payment_methods (organization_id, id),
  CONSTRAINT payment_requests_amount_check CHECK (amount >= 0),
  CONSTRAINT payment_requests_payment_status_check CHECK (payment_status IN ('pending', 'succeeded', 'failed')),
  CONSTRAINT payment_requests_payment_attempts_check CHECK (payment_attempts >= 0),
  CONSTRAINT payment_requests_payment_method_type_check CHECK (payment_method_type IN ('provider', 'manual'))
);

CREATE INDEX payment_requests_customer_idx ON payment_requests (organization_id, customer_id);
CREATE INDEX payment_requests_dunning_campaign_idx ON payment_requests (organization_id, dunning_campaign_id) WHERE dunning_campaign_id IS NOT NULL;
CREATE INDEX payment_requests_payment_method_idx ON payment_requests (organization_id, payment_method_id) WHERE payment_method_id IS NOT NULL;

ALTER TABLE payment_requests ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON payment_requests
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The invoices of a payment request, in their order.
CREATE TABLE payment_request_invoices (
  organization_id uuid NOT NULL,
  payment_request_id uuid NOT NULL,
  invoice_id uuid NOT NULL,
  position integer NOT NULL,
  PRIMARY KEY (organization_id, payment_request_id, invoice_id),
  CONSTRAINT payment_request_invoices_payment_request_fkey FOREIGN KEY (organization_id, payment_request_id) REFERENCES payment_requests (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT payment_request_invoices_invoice_fkey FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id)
);

CREATE INDEX payment_request_invoices_invoice_idx ON payment_request_invoices (organization_id, invoice_id);

ALTER TABLE payment_request_invoices ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON payment_request_invoices
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Attempts to collect a payable: an invoice or a payment request (domain
-- §15.1). provider_payment_data is the provider's document kept verbatim;
-- provider_method_details is the store-encoded payment.MethodDetails (NULL
-- when unknown). At most one non-final provider payment per payable.
CREATE TABLE payments (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  payable_type text NOT NULL,
  invoice_id uuid,
  payment_request_id uuid,
  customer_id uuid NOT NULL,
  payment_kind text NOT NULL,
  status text NOT NULL,
  provider_status text,
  amount bigint NOT NULL,
  currency text NOT NULL,
  reference text,
  paid_at timestamptz,
  integration_id uuid,
  provider_payment_id text,
  provider_payment_method_id text,
  provider_payment_data json,
  provider_method_details jsonb,
  payment_method_id uuid,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  -- The provider's code of the last failure (authentication_required, …),
  -- which makes the next attempt on-session.
  error_code text,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT payments_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT payments_invoice_fkey FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id),
  CONSTRAINT payments_payment_request_fkey FOREIGN KEY (organization_id, payment_request_id) REFERENCES payment_requests (organization_id, id),
  CONSTRAINT payments_customer_fkey FOREIGN KEY (organization_id, customer_id) REFERENCES customers (organization_id, id),
  CONSTRAINT payments_payment_method_fkey FOREIGN KEY (organization_id, payment_method_id) REFERENCES payment_methods (organization_id, id),
  CONSTRAINT payments_payable_type_check CHECK (payable_type IN ('invoice', 'payment_request')),
  CONSTRAINT payments_payable_check CHECK (
    (payable_type = 'invoice' AND invoice_id IS NOT NULL AND payment_request_id IS NULL)
    OR (payable_type = 'payment_request' AND payment_request_id IS NOT NULL AND invoice_id IS NULL)
  ),
  CONSTRAINT payments_payment_kind_check CHECK (payment_kind IN ('provider', 'manual', 'x402')),
  CONSTRAINT payments_status_check CHECK (status IN ('pending', 'processing', 'succeeded', 'failed')),
  CONSTRAINT payments_amount_check CHECK (amount >= 0),
  CONSTRAINT payments_reference_check CHECK (char_length(reference) <= 40)
);

CREATE UNIQUE INDEX payments_invoice_pending_provider_key ON payments (organization_id, invoice_id)
  WHERE payment_kind = 'provider' AND status IN ('pending', 'processing') AND invoice_id IS NOT NULL;
CREATE UNIQUE INDEX payments_payment_request_pending_provider_key ON payments (organization_id, payment_request_id)
  WHERE payment_kind = 'provider' AND status IN ('pending', 'processing') AND payment_request_id IS NOT NULL;
CREATE INDEX payments_invoice_idx ON payments (organization_id, invoice_id) WHERE invoice_id IS NOT NULL;
CREATE INDEX payments_payment_request_idx ON payments (organization_id, payment_request_id) WHERE payment_request_id IS NOT NULL;
CREATE INDEX payments_customer_idx ON payments (organization_id, customer_id);
CREATE INDEX payments_provider_payment_idx ON payments (organization_id, integration_id, provider_payment_id) WHERE provider_payment_id IS NOT NULL;
CREATE INDEX payments_payment_method_idx ON payments (organization_id, payment_method_id) WHERE payment_method_id IS NOT NULL;

ALTER TABLE payments ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON payments
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- One receipt per payment (domain §15.2); numbers are unique per
-- organization.
CREATE TABLE payment_receipts (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  payment_id uuid NOT NULL,
  billing_entity_id uuid,
  number text NOT NULL,
  file text,
  xml_file text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT payment_receipts_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT payment_receipts_payment_fkey FOREIGN KEY (organization_id, payment_id) REFERENCES payments (organization_id, id),
  CONSTRAINT payment_receipts_billing_entity_fkey FOREIGN KEY (organization_id, billing_entity_id) REFERENCES billing_entities (organization_id, id),
  CONSTRAINT payment_receipts_payment_key UNIQUE (organization_id, payment_id),
  CONSTRAINT payment_receipts_number_key UNIQUE (organization_id, number)
);

CREATE INDEX payment_receipts_billing_entity_idx ON payment_receipts (organization_id, billing_entity_id) WHERE billing_entity_id IS NOT NULL;

ALTER TABLE payment_receipts ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON payment_receipts
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Refunds of payments through the provider that collected them (domain
-- §15.3), for a credit note or an expired subscription activation.
CREATE TABLE refunds (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  payment_id uuid NOT NULL,
  reason text,
  credit_note_id uuid,
  subscription_id uuid,
  integration_id uuid,
  payment_provider_customer_id uuid,
  amount bigint NOT NULL,
  currency text NOT NULL,
  status text NOT NULL,
  provider_refund_id text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT refunds_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT refunds_payment_fkey FOREIGN KEY (organization_id, payment_id) REFERENCES payments (organization_id, id),
  CONSTRAINT refunds_credit_note_fkey FOREIGN KEY (organization_id, credit_note_id) REFERENCES credit_notes (organization_id, id),
  CONSTRAINT refunds_subscription_fkey FOREIGN KEY (organization_id, subscription_id) REFERENCES subscriptions (organization_id, id),
  CONSTRAINT refunds_provider_customer_fkey FOREIGN KEY (organization_id, payment_provider_customer_id) REFERENCES payment_provider_customers (organization_id, id),
  CONSTRAINT refunds_reason_check CHECK (reason IN ('credit_note', 'subscription_activation_expired')),
  CONSTRAINT refunds_amount_check CHECK (amount > 0),
  CONSTRAINT refunds_status_check CHECK (status IN ('pending', 'succeeded', 'failed'))
);

CREATE INDEX refunds_payment_idx ON refunds (organization_id, payment_id);
CREATE INDEX refunds_credit_note_idx ON refunds (organization_id, credit_note_id) WHERE credit_note_id IS NOT NULL;
CREATE INDEX refunds_subscription_idx ON refunds (organization_id, subscription_id) WHERE subscription_id IS NOT NULL;
CREATE INDEX refunds_provider_customer_idx ON refunds (organization_id, payment_provider_customer_id) WHERE payment_provider_customer_id IS NOT NULL;
CREATE INDEX refunds_provider_refund_idx ON refunds (organization_id, integration_id, provider_refund_id) WHERE provider_refund_id IS NOT NULL;

ALTER TABLE refunds ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON refunds
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Hosted checkout links of invoices (domain §15.1); at most one active link
-- per invoice.
CREATE TABLE payment_intents (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  invoice_id uuid NOT NULL,
  status text NOT NULL,
  payment_url text,
  provider_session_id text,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT payment_intents_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT payment_intents_invoice_fkey FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id),
  CONSTRAINT payment_intents_status_check CHECK (status IN ('active', 'expired'))
);

CREATE UNIQUE INDEX payment_intents_invoice_active_key ON payment_intents (organization_id, invoice_id) WHERE status = 'active';
CREATE INDEX payment_intents_invoice_idx ON payment_intents (organization_id, invoice_id);
CREATE INDEX payment_intents_expires_at_idx ON payment_intents (organization_id, expires_at) WHERE status = 'active';

ALTER TABLE payment_intents ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON payment_intents
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Webhooks received from payment providers, stored before processing
-- (domain §15.6); the payload is the raw body, kept verbatim.
CREATE TABLE inbound_webhooks (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  source text NOT NULL,
  integration_id uuid,
  code text,
  event_type text NOT NULL,
  payload json NOT NULL,
  signature text,
  status text NOT NULL,
  attempts integer NOT NULL DEFAULT 0,
  processing_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT inbound_webhooks_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT inbound_webhooks_source_check CHECK (source <> ''),
  CONSTRAINT inbound_webhooks_event_type_check CHECK (event_type <> ''),
  CONSTRAINT inbound_webhooks_status_check CHECK (status IN ('pending', 'processing', 'succeeded', 'failed')),
  CONSTRAINT inbound_webhooks_attempts_check CHECK (attempts >= 0)
);

CREATE INDEX inbound_webhooks_retriable_idx ON inbound_webhooks (organization_id, created_at) WHERE status <> 'succeeded';
CREATE INDEX inbound_webhooks_updated_at_idx ON inbound_webhooks (organization_id, updated_at);
CREATE INDEX inbound_webhooks_integration_idx ON inbound_webhooks (organization_id, integration_id) WHERE integration_id IS NOT NULL;

ALTER TABLE inbound_webhooks ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON inbound_webhooks
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
