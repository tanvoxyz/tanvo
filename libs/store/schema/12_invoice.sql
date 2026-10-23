-- Invoices, invoice subscriptions, fees, applied taxes, adjusted fees,
-- settlements and error details (domain §11, §12).
--
-- An invoice owns its subscriptions, fees, applied taxes, adjustments,
-- settlements and error details: deleting a draft (the only invoices that
-- are deleted, §11.7) cascades to them.
--
-- References to catalog objects (charges, charge filters, fixed charges,
-- add-ons, commitments, taxes) are snapshots without foreign keys: the
-- catalog deletes those rows (catalog.ChargeRepository.Delete and the
-- like) while the issued documents that billed them are kept (§7.7).
-- Events live in the event store, not in PostgreSQL.

CREATE TABLE invoices (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  customer_id uuid NOT NULL,
  billing_entity_id uuid NOT NULL,
  invoice_type text NOT NULL,
  status text NOT NULL,
  payment_status text NOT NULL,
  tax_status text,
  number text NOT NULL,
  sequential_id bigint,
  billing_entity_sequential_id bigint,
  currency text NOT NULL,
  fees_amount bigint NOT NULL,
  progressive_billing_credit_amount bigint NOT NULL,
  coupons_amount bigint NOT NULL,
  sub_total_excluding_taxes_amount bigint NOT NULL,
  taxes_amount bigint NOT NULL,
  taxes_rate numeric NOT NULL,
  sub_total_including_taxes_amount bigint NOT NULL,
  credit_notes_amount bigint NOT NULL,
  prepaid_credit_amount bigint NOT NULL,
  prepaid_granted_credit_amount bigint NOT NULL,
  prepaid_purchased_credit_amount bigint NOT NULL,
  total_amount bigint NOT NULL,
  total_paid_amount bigint NOT NULL,
  offsetted_amount bigint NOT NULL,
  issuing_date date,
  payment_due_date date,
  expected_finalization_date date,
  applied_grace_period_days integer NOT NULL,
  net_payment_term_days integer NOT NULL,
  payment_overdue boolean NOT NULL,
  payment_dispute_lost_at timestamptz,
  self_billed boolean NOT NULL,
  voided_invoice_id uuid,
  skip_automatic_payment boolean NOT NULL,
  payment_method_type text,
  payment_method_id uuid,
  purchase_order_number text,
  -- References x402 connections (parity §1.36); no foreign key until they
  -- are stored.
  x402_connection_id uuid,
  x402_payment_token text,
  metadata jsonb NOT NULL,
  file text,
  xml_file text,
  ready_for_payment_processing boolean NOT NULL,
  refresh_requested boolean NOT NULL,
  finalized_at timestamptz,
  voided_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT invoices_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT invoices_customer_fkey FOREIGN KEY (organization_id, customer_id) REFERENCES customers (organization_id, id),
  CONSTRAINT invoices_billing_entity_fkey FOREIGN KEY (organization_id, billing_entity_id) REFERENCES billing_entities (organization_id, id),
  CONSTRAINT invoices_voided_invoice_fkey FOREIGN KEY (organization_id, voided_invoice_id) REFERENCES invoices (organization_id, id),
  CONSTRAINT invoices_invoice_type_check CHECK (invoice_type IN ('subscription', 'credit', 'one_off', 'advance_charges', 'progressive_billing')),
  CONSTRAINT invoices_status_check CHECK (status IN ('generating', 'draft', 'open', 'finalized', 'closed', 'voided', 'failed')),
  CONSTRAINT invoices_payment_status_check CHECK (payment_status IN ('pending', 'succeeded', 'failed')),
  CONSTRAINT invoices_tax_status_check CHECK (tax_status IN ('pending', 'succeeded', 'failed')),
  CONSTRAINT invoices_payment_method_type_check CHECK (payment_method_type IN ('provider', 'manual')),
  CONSTRAINT invoices_currency_check CHECK (currency <> '')
);

-- Numbers are unique per organization, except the shared draft numbers
-- ("<prefix>-DRAFT") and the empty number of invoices not numbered yet.
CREATE UNIQUE INDEX invoices_number_key ON invoices (organization_id, number) WHERE number <> '' AND number NOT LIKE '%-DRAFT';
CREATE INDEX invoices_customer_idx ON invoices (organization_id, customer_id, billing_entity_id);
CREATE INDEX invoices_billing_entity_idx ON invoices (organization_id, billing_entity_id);
CREATE INDEX invoices_voided_invoice_idx ON invoices (organization_id, voided_invoice_id) WHERE voided_invoice_id IS NOT NULL;
CREATE INDEX invoices_payment_method_idx ON invoices (organization_id, payment_method_id) WHERE payment_method_id IS NOT NULL;
CREATE INDEX invoices_purchase_order_number_idx ON invoices (organization_id, lower(purchase_order_number)) WHERE purchase_order_number IS NOT NULL;
-- The x402 payment token is unique across all organizations.
CREATE UNIQUE INDEX invoices_x402_payment_token_key ON invoices (x402_payment_token) WHERE x402_payment_token IS NOT NULL;
-- The scheduler scans: drafts to finalize or refresh, overdue candidates.
CREATE INDEX invoices_drafts_idx ON invoices (organization_id, expected_finalization_date) WHERE status = 'draft';
CREATE INDEX invoices_overdue_idx ON invoices (organization_id, payment_due_date) WHERE status = 'finalized' AND NOT payment_overdue;

ALTER TABLE invoices ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON invoices
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The zero period of an invoice subscription is stored as the zero instant
-- (0001-01-01 UTC), so that the idempotency keys never hold a NULL.
CREATE TABLE invoice_subscriptions (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  invoice_id uuid NOT NULL,
  subscription_id uuid NOT NULL,
  invoicing_reason text NOT NULL,
  from_datetime timestamptz NOT NULL,
  to_datetime timestamptz NOT NULL,
  charges_from_datetime timestamptz NOT NULL,
  charges_to_datetime timestamptz NOT NULL,
  fixed_charges_from_datetime timestamptz NOT NULL,
  fixed_charges_to_datetime timestamptz NOT NULL,
  billing_timestamp timestamptz NOT NULL,
  recurring boolean NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT invoice_subscriptions_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT invoice_subscriptions_invoice_fkey FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT invoice_subscriptions_subscription_fkey FOREIGN KEY (organization_id, subscription_id) REFERENCES subscriptions (organization_id, id),
  CONSTRAINT invoice_subscriptions_invoicing_reason_check CHECK (invoicing_reason IN ('subscription_starting', 'subscription_periodic', 'subscription_terminating', 'in_advance_charge', 'in_advance_charge_periodic', 'progressive_billing'))
);

-- The idempotency keys of invoice.Subscription.Keys: a recurring row per
-- charges period and per fixed charges period (when billed, that is not the
-- zero instant), a starting and a terminating row per subscription.
CREATE UNIQUE INDEX invoice_subscriptions_charges_key ON invoice_subscriptions (organization_id, subscription_id, charges_from_datetime, charges_to_datetime)
  WHERE recurring AND charges_from_datetime <> '0001-01-01 00:00:00+00';
CREATE UNIQUE INDEX invoice_subscriptions_fixed_charges_key ON invoice_subscriptions (organization_id, subscription_id, fixed_charges_from_datetime, fixed_charges_to_datetime)
  WHERE recurring AND fixed_charges_from_datetime <> '0001-01-01 00:00:00+00';
CREATE UNIQUE INDEX invoice_subscriptions_reason_key ON invoice_subscriptions (organization_id, subscription_id, invoicing_reason)
  WHERE invoicing_reason IN ('subscription_starting', 'subscription_terminating');
CREATE INDEX invoice_subscriptions_invoice_idx ON invoice_subscriptions (organization_id, invoice_id);
CREATE INDEX invoice_subscriptions_subscription_idx ON invoice_subscriptions (organization_id, subscription_id, from_datetime);

ALTER TABLE invoice_subscriptions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON invoice_subscriptions
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Fees. The billed periods (invoice.FeeProperties) are columns; their zero
-- instants are stored as 0001-01-01 UTC. applied_taxes is the jsonb array of
-- the fee's tax snapshots, read and written with the fee; pricing_unit_usage
-- is the jsonb snapshot of the pricing unit amounts; presentation_breakdowns
-- is the jsonb array of the fee's units by presentation group.
CREATE TABLE fees (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  invoice_id uuid,
  subscription_id uuid,
  fee_type text NOT NULL,
  charge_id uuid,
  charge_filter_id uuid,
  fixed_charge_id uuid,
  add_on_id uuid,
  commitment_id uuid,
  wallet_transaction_id uuid,
  grouped_by jsonb NOT NULL,
  invoice_display_name text,
  description text,
  amount bigint NOT NULL,
  precise_amount numeric NOT NULL,
  unit_amount bigint NOT NULL,
  precise_unit_amount numeric NOT NULL,
  units numeric NOT NULL,
  total_aggregated_units numeric NOT NULL,
  events_count bigint NOT NULL,
  amount_details jsonb,
  taxes_amount bigint NOT NULL,
  taxes_precise_amount numeric NOT NULL,
  taxes_rate numeric NOT NULL,
  taxes_base_rate numeric NOT NULL,
  precise_coupons_amount numeric NOT NULL,
  precise_credit_notes_amount numeric NOT NULL,
  currency text NOT NULL,
  pay_in_advance boolean NOT NULL,
  invoiceable boolean NOT NULL,
  payment_status text,
  from_datetime timestamptz NOT NULL,
  to_datetime timestamptz NOT NULL,
  charges_from_datetime timestamptz NOT NULL,
  charges_to_datetime timestamptz NOT NULL,
  billing_timestamp timestamptz NOT NULL,
  charges_duration_days integer NOT NULL,
  fixed_charges_from_datetime timestamptz NOT NULL,
  fixed_charges_to_datetime timestamptz NOT NULL,
  fixed_charges_duration_days integer NOT NULL,
  true_up_parent_fee_id uuid,
  pricing_unit_usage jsonb,
  applied_taxes jsonb NOT NULL,
  presentation_breakdowns jsonb NOT NULL,
  pay_in_advance_event_id uuid,
  pay_in_advance_event_transaction_id text,
  succeeded_at timestamptz,
  failed_at timestamptz,
  refunded_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT fees_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT fees_invoice_fkey FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT fees_subscription_fkey FOREIGN KEY (organization_id, subscription_id) REFERENCES subscriptions (organization_id, id),
  CONSTRAINT fees_true_up_parent_fee_fkey FOREIGN KEY (organization_id, true_up_parent_fee_id) REFERENCES fees (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT fees_fee_type_check CHECK (fee_type IN ('subscription', 'charge', 'fixed_charge', 'add_on', 'credit', 'commitment', 'product')),
  CONSTRAINT fees_payment_status_check CHECK (payment_status IN ('pending', 'succeeded', 'failed', 'refunded')),
  CONSTRAINT fees_currency_check CHECK (currency <> ''),
  CONSTRAINT fees_grouped_by_check CHECK (jsonb_typeof(grouped_by) = 'object'),
  CONSTRAINT fees_applied_taxes_check CHECK (jsonb_typeof(applied_taxes) = 'array'),
  CONSTRAINT fees_presentation_breakdowns_check CHECK (jsonb_typeof(presentation_breakdowns) = 'array')
);

CREATE INDEX fees_invoice_idx ON fees (organization_id, invoice_id) WHERE invoice_id IS NOT NULL;
CREATE INDEX fees_subscription_idx ON fees (organization_id, subscription_id, from_datetime) WHERE subscription_id IS NOT NULL;
CREATE INDEX fees_charge_idx ON fees (organization_id, charge_id) WHERE charge_id IS NOT NULL;
CREATE INDEX fees_wallet_transaction_idx ON fees (organization_id, wallet_transaction_id) WHERE wallet_transaction_id IS NOT NULL;
CREATE INDEX fees_true_up_parent_fee_idx ON fees (organization_id, true_up_parent_fee_id) WHERE true_up_parent_fee_id IS NOT NULL;
CREATE INDEX fees_event_transaction_idx ON fees (organization_id, subscription_id, pay_in_advance_event_transaction_id)
  WHERE pay_in_advance_event_transaction_id IS NOT NULL;
-- The unbilled pay-in-advance fees regrouped by advance-charges invoices.
CREATE INDEX fees_unbilled_in_advance_idx ON fees (organization_id, created_at)
  WHERE invoice_id IS NULL AND pay_in_advance AND invoiceable AND payment_status = 'succeeded';

ALTER TABLE fees ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON fees
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Invoice-level applied taxes; tax_id is a snapshot (no foreign key).
CREATE TABLE invoice_applied_taxes (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  invoice_id uuid NOT NULL,
  tax_id uuid,
  tax_code text NOT NULL,
  tax_name text NOT NULL,
  tax_description text,
  tax_rate numeric NOT NULL,
  fees_amount bigint NOT NULL,
  taxable_base_amount bigint NOT NULL,
  amount bigint NOT NULL,
  precise_amount numeric NOT NULL,
  currency text NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT invoice_applied_taxes_taxable_base_amount_check CHECK (taxable_base_amount >= 0),
  CONSTRAINT invoice_applied_taxes_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT invoice_applied_taxes_invoice_fkey FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX invoice_applied_taxes_invoice_idx ON invoice_applied_taxes (organization_id, invoice_id);

ALTER TABLE invoice_applied_taxes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON invoice_applied_taxes
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Adjustments of draft fees (§12.3). They survive the refreshes that delete
-- and recreate the invoice's fees, so fee_id is a plain reference (no
-- foreign key); an adjustment is unique per fee. The (fixed) charges period
-- of the adjusted fee is kept to match the recreated fee; zero instants are
-- stored as 0001-01-01 UTC.
CREATE TABLE adjusted_fees (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  invoice_id uuid NOT NULL,
  fee_id uuid,
  subscription_id uuid,
  charge_id uuid,
  charge_filter_id uuid,
  fixed_charge_id uuid,
  fee_type text NOT NULL,
  grouped_by jsonb NOT NULL,
  charges_from_datetime timestamptz NOT NULL,
  charges_to_datetime timestamptz NOT NULL,
  fixed_charges_from_datetime timestamptz NOT NULL,
  fixed_charges_to_datetime timestamptz NOT NULL,
  mode text NOT NULL,
  invoice_display_name text,
  units numeric NOT NULL,
  unit_precise_amount numeric NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT adjusted_fees_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT adjusted_fees_invoice_fkey FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT adjusted_fees_subscription_fkey FOREIGN KEY (organization_id, subscription_id) REFERENCES subscriptions (organization_id, id),
  CONSTRAINT adjusted_fees_fee_type_check CHECK (fee_type IN ('subscription', 'charge', 'fixed_charge', 'add_on', 'credit', 'commitment', 'product')),
  CONSTRAINT adjusted_fees_mode_check CHECK (mode IN ('display_name', 'units', 'units_and_unit_price')),
  CONSTRAINT adjusted_fees_grouped_by_check CHECK (jsonb_typeof(grouped_by) = 'object')
);

CREATE UNIQUE INDEX adjusted_fees_fee_key ON adjusted_fees (organization_id, fee_id) WHERE fee_id IS NOT NULL;
CREATE INDEX adjusted_fees_invoice_idx ON adjusted_fees (organization_id, invoice_id);
CREATE INDEX adjusted_fees_subscription_idx ON adjusted_fees (organization_id, subscription_id) WHERE subscription_id IS NOT NULL;

ALTER TABLE adjusted_fees ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON adjusted_fees
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Settlements by payments and offset credit notes; their foreign keys to
-- payments and credit notes are in 90_fk_invoice.sql.
CREATE TABLE invoice_settlements (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  invoice_id uuid NOT NULL,
  settlement_type text NOT NULL,
  payment_id uuid,
  credit_note_id uuid,
  amount bigint NOT NULL,
  currency text NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT invoice_settlements_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT invoice_settlements_invoice_fkey FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT invoice_settlements_settlement_type_check CHECK (settlement_type IN ('payment', 'credit_note'))
);

CREATE INDEX invoice_settlements_invoice_idx ON invoice_settlements (organization_id, invoice_id);
CREATE INDEX invoice_settlements_payment_idx ON invoice_settlements (organization_id, payment_id) WHERE payment_id IS NOT NULL;
CREATE INDEX invoice_settlements_credit_note_idx ON invoice_settlements (organization_id, credit_note_id) WHERE credit_note_id IS NOT NULL;

ALTER TABLE invoice_settlements ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON invoice_settlements
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE invoice_error_details (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  invoice_id uuid NOT NULL,
  error_code text NOT NULL,
  details jsonb,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT invoice_error_details_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT invoice_error_details_invoice_fkey FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT invoice_error_details_error_code_check CHECK (error_code IN ('not_provided', 'tax_error', 'tax_voiding_error', 'invoice_generation_error'))
);

CREATE INDEX invoice_error_details_invoice_idx ON invoice_error_details (organization_id, invoice_id);

ALTER TABLE invoice_error_details ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON invoice_error_details
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Credits applied to invoices (§11.11): coupon applications, credit-note
-- consumptions and progressive-billing deductions; exactly one source is
-- set. The credit note foreign key is in 90_fk_invoice.sql.
CREATE TABLE invoice_credits (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  invoice_id uuid NOT NULL,
  applied_coupon_id uuid,
  credit_note_id uuid,
  progressive_billing_invoice_id uuid,
  amount bigint NOT NULL,
  currency text NOT NULL,
  before_taxes boolean NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT invoice_credits_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT invoice_credits_invoice_fkey FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT invoice_credits_applied_coupon_fkey FOREIGN KEY (organization_id, applied_coupon_id) REFERENCES applied_coupons (organization_id, id),
  CONSTRAINT invoice_credits_progressive_billing_invoice_fkey FOREIGN KEY (organization_id, progressive_billing_invoice_id) REFERENCES invoices (organization_id, id),
  CONSTRAINT invoice_credits_source_check CHECK (num_nonnulls(applied_coupon_id, credit_note_id, progressive_billing_invoice_id) = 1),
  CONSTRAINT invoice_credits_amount_check CHECK (amount >= 0),
  CONSTRAINT invoice_credits_currency_check CHECK (currency <> '')
);

CREATE INDEX invoice_credits_invoice_idx ON invoice_credits (organization_id, invoice_id, created_at);
CREATE INDEX invoice_credits_applied_coupon_idx ON invoice_credits (organization_id, applied_coupon_id) WHERE applied_coupon_id IS NOT NULL;
CREATE INDEX invoice_credits_credit_note_idx ON invoice_credits (organization_id, credit_note_id) WHERE credit_note_id IS NOT NULL;
CREATE INDEX invoice_credits_progressive_billing_invoice_idx ON invoice_credits (organization_id, progressive_billing_invoice_id)
  WHERE progressive_billing_invoice_id IS NOT NULL;

ALTER TABLE invoice_credits ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON invoice_credits
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
