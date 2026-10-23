-- Webhook endpoints, deliveries and domain events (domain §18, §2.3).

-- A webhook endpoint; its URL is unique per organization. event_types NULL
-- subscribes to every type, an empty array to none.
CREATE TABLE webhook_endpoints (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  url text NOT NULL,
  name text,
  event_types text[],
  secret text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT webhook_endpoints_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT webhook_endpoints_url_key UNIQUE (organization_id, url),
  CONSTRAINT webhook_endpoints_event_types_check CHECK (event_types <@ ARRAY['alert.triggered', 'billable_metric.created', 'billable_metric.updated', 'billable_metric.deleted', 'plan.created', 'plan.updated', 'plan.deleted', 'feature.created', 'feature.updated', 'feature.deleted', 'customer.created', 'customer.updated', 'customer.vies_check', 'customer.accounting_provider_created', 'customer.accounting_provider_error', 'customer.crm_provider_created', 'customer.crm_provider_error', 'customer.payment_provider_created', 'customer.payment_provider_error', 'customer.checkout_url_generated', 'customer.tax_provider_error', 'subscription.started', 'subscription.incomplete', 'subscription.updated', 'subscription.trial_ended', 'subscription.termination_alert', 'subscription.terminated', 'subscription.canceled', 'subscription.usage_threshold_reached', 'events.errors', 'fee.created', 'fee.tax_provider_error', 'invoice.created', 'invoice.drafted', 'invoice.one_off_created', 'invoice.paid_credit_added', 'invoice.generated', 'invoice.ready_to_finalize', 'invoice.voided', 'invoice.deleted', 'invoice.payment_status_updated', 'invoice.payment_overdue', 'invoice.payment_failure', 'invoice.payment_dispute_lost', 'invoice.resynced', 'credit_note.created', 'credit_note.generated', 'credit_note.refund_failure', 'payment.succeeded', 'payment.requires_action', 'payment_provider.error', 'payment_receipt.created', 'payment_receipt.generated', 'payment_request.created', 'payment_request.payment_status_updated', 'payment_request.payment_failure', 'wallet.created', 'wallet.updated', 'wallet.terminated', 'wallet.depleted_ongoing_balance', 'wallet_transaction.created', 'wallet_transaction.updated', 'wallet_transaction.payment_failure', 'dunning_campaign.finished', 'integration.provider_error', 'quote.created', 'quote.approved', 'quote.voided', 'order_form.created', 'order_form.signed', 'order_form.expired', 'order_form.voided', 'order.created', 'order.executed'])
);

ALTER TABLE webhook_endpoints ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON webhook_endpoints
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Domain events, recorded in the transaction of the change they describe.
-- The payload is the API representation of the object, kept verbatim.
CREATE TABLE domain_events (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  event_type text NOT NULL,
  object_type text NOT NULL,
  object_id uuid NOT NULL,
  payload json NOT NULL,
  occurred_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT domain_events_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT domain_events_event_type_check CHECK (event_type IN ('alert.triggered', 'billable_metric.created', 'billable_metric.updated', 'billable_metric.deleted', 'plan.created', 'plan.updated', 'plan.deleted', 'feature.created', 'feature.updated', 'feature.deleted', 'customer.created', 'customer.updated', 'customer.vies_check', 'customer.accounting_provider_created', 'customer.accounting_provider_error', 'customer.crm_provider_created', 'customer.crm_provider_error', 'customer.payment_provider_created', 'customer.payment_provider_error', 'customer.checkout_url_generated', 'customer.tax_provider_error', 'subscription.started', 'subscription.incomplete', 'subscription.updated', 'subscription.trial_ended', 'subscription.termination_alert', 'subscription.terminated', 'subscription.canceled', 'subscription.usage_threshold_reached', 'events.errors', 'fee.created', 'fee.tax_provider_error', 'invoice.created', 'invoice.drafted', 'invoice.one_off_created', 'invoice.paid_credit_added', 'invoice.generated', 'invoice.ready_to_finalize', 'invoice.voided', 'invoice.deleted', 'invoice.payment_status_updated', 'invoice.payment_overdue', 'invoice.payment_failure', 'invoice.payment_dispute_lost', 'invoice.resynced', 'credit_note.created', 'credit_note.generated', 'credit_note.refund_failure', 'payment.succeeded', 'payment.requires_action', 'payment_provider.error', 'payment_receipt.created', 'payment_receipt.generated', 'payment_request.created', 'payment_request.payment_status_updated', 'payment_request.payment_failure', 'wallet.created', 'wallet.updated', 'wallet.terminated', 'wallet.depleted_ongoing_balance', 'wallet_transaction.created', 'wallet_transaction.updated', 'wallet_transaction.payment_failure', 'dunning_campaign.finished', 'integration.provider_error', 'quote.created', 'quote.approved', 'quote.voided', 'order_form.created', 'order_form.signed', 'order_form.expired', 'order_form.voided', 'order.created', 'order.executed'))
);

CREATE INDEX domain_events_object_idx ON domain_events (organization_id, object_type, object_id, id);

ALTER TABLE domain_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON domain_events
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The deliveries of domain events to endpoints, deleted with their
-- endpoint and purged after the retention period. event_id is the
-- Standard Webhooks message id; it has no foreign key because domain events
-- and deliveries have independent retentions. The payload is the body as
-- sent, verbatim.
CREATE TABLE webhook_deliveries (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  webhook_endpoint_id uuid NOT NULL,
  event_id uuid NOT NULL,
  event_type text NOT NULL,
  object_type text NOT NULL,
  object_id uuid NOT NULL,
  payload json NOT NULL,
  status text NOT NULL,
  attempts integer NOT NULL DEFAULT 0,
  last_attempt_at timestamptz,
  next_attempt_at timestamptz,
  http_status integer,
  response text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT webhook_deliveries_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT webhook_deliveries_webhook_endpoint_fkey FOREIGN KEY (organization_id, webhook_endpoint_id) REFERENCES webhook_endpoints (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT webhook_deliveries_event_type_check CHECK (event_type IN ('alert.triggered', 'billable_metric.created', 'billable_metric.updated', 'billable_metric.deleted', 'plan.created', 'plan.updated', 'plan.deleted', 'feature.created', 'feature.updated', 'feature.deleted', 'customer.created', 'customer.updated', 'customer.vies_check', 'customer.accounting_provider_created', 'customer.accounting_provider_error', 'customer.crm_provider_created', 'customer.crm_provider_error', 'customer.payment_provider_created', 'customer.payment_provider_error', 'customer.checkout_url_generated', 'customer.tax_provider_error', 'subscription.started', 'subscription.incomplete', 'subscription.updated', 'subscription.trial_ended', 'subscription.termination_alert', 'subscription.terminated', 'subscription.canceled', 'subscription.usage_threshold_reached', 'events.errors', 'fee.created', 'fee.tax_provider_error', 'invoice.created', 'invoice.drafted', 'invoice.one_off_created', 'invoice.paid_credit_added', 'invoice.generated', 'invoice.ready_to_finalize', 'invoice.voided', 'invoice.deleted', 'invoice.payment_status_updated', 'invoice.payment_overdue', 'invoice.payment_failure', 'invoice.payment_dispute_lost', 'invoice.resynced', 'credit_note.created', 'credit_note.generated', 'credit_note.refund_failure', 'payment.succeeded', 'payment.requires_action', 'payment_provider.error', 'payment_receipt.created', 'payment_receipt.generated', 'payment_request.created', 'payment_request.payment_status_updated', 'payment_request.payment_failure', 'wallet.created', 'wallet.updated', 'wallet.terminated', 'wallet.depleted_ongoing_balance', 'wallet_transaction.created', 'wallet_transaction.updated', 'wallet_transaction.payment_failure', 'dunning_campaign.finished', 'integration.provider_error', 'quote.created', 'quote.approved', 'quote.voided', 'order_form.created', 'order_form.signed', 'order_form.expired', 'order_form.voided', 'order.created', 'order.executed')),
  CONSTRAINT webhook_deliveries_status_check CHECK (status IN ('pending', 'retrying', 'succeeded', 'failed')),
  CONSTRAINT webhook_deliveries_attempts_check CHECK (attempts >= 0)
);

CREATE INDEX webhook_deliveries_endpoint_idx ON webhook_deliveries (organization_id, webhook_endpoint_id, id);
-- The dispatcher sweep, across organizations.
CREATE INDEX webhook_deliveries_due_idx ON webhook_deliveries (next_attempt_at) WHERE status IN ('pending', 'retrying') AND next_attempt_at IS NOT NULL;
-- The retention purge, across organizations.
CREATE INDEX webhook_deliveries_created_at_idx ON webhook_deliveries (created_at);

ALTER TABLE webhook_deliveries ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON webhook_deliveries
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
