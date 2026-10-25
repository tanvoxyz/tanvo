-- Credit notes, their items and applied taxes (domain §13).
--
-- A credit note belongs to its invoice: only drafts are deleted, and the
-- credit notes of a draft invoice (the automatic ones of progressive
-- billing, §13.2) go with it. Items and applied taxes are child rows keyed
-- by their position in the credit note's lists (their IDs are carried as
-- values); applied taxes snapshot the tax (tax_id has no foreign key, see
-- 12_invoice.sql).

CREATE TABLE credit_notes (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  invoice_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  number text NOT NULL,
  sequential_id bigint NOT NULL,
  status text NOT NULL,
  credit_status text,
  refund_status text,
  reason text NOT NULL,
  description text,
  credit_amount bigint NOT NULL,
  refund_amount bigint NOT NULL,
  offset_amount bigint NOT NULL,
  balance_amount bigint NOT NULL,
  sub_total_excluding_taxes_amount bigint NOT NULL,
  coupons_adjustment_amount bigint NOT NULL,
  precise_coupons_adjustment_amount numeric NOT NULL,
  taxes_amount bigint NOT NULL,
  precise_taxes_amount numeric NOT NULL,
  taxes_rate numeric NOT NULL,
  total_amount bigint NOT NULL,
  currency text NOT NULL,
  issuing_date date,
  -- The item metadata (lago Metadata::ItemMetadata): NULL when there is none,
  -- an array, possibly empty, when it exists (an emptied item stays).
  metadata jsonb,
  file text,
  xml_file text,
  refunded_at timestamptz,
  voided_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT credit_notes_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT credit_notes_invoice_fkey FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT credit_notes_customer_fkey FOREIGN KEY (organization_id, customer_id) REFERENCES customers (organization_id, id),
  CONSTRAINT credit_notes_status_check CHECK (status IN ('draft', 'finalized', 'deleted')),
  CONSTRAINT credit_notes_credit_status_check CHECK (credit_status IN ('available', 'consumed', 'voided')),
  CONSTRAINT credit_notes_refund_status_check CHECK (refund_status IN ('pending', 'succeeded', 'failed')),
  CONSTRAINT credit_notes_reason_check CHECK (reason IN ('duplicated_charge', 'product_unsatisfactory', 'order_change', 'order_cancellation', 'fraudulent_charge', 'other')),
  CONSTRAINT credit_notes_currency_check CHECK (currency <> '')
);

-- Numbers are unique per organization (an empty number is not numbered).
CREATE UNIQUE INDEX credit_notes_number_key ON credit_notes (organization_id, number) WHERE number <> '';
CREATE INDEX credit_notes_invoice_idx ON credit_notes (organization_id, invoice_id, sequential_id);
CREATE INDEX credit_notes_customer_idx ON credit_notes (organization_id, customer_id);
-- The credits applied to new invoices (creditnote.Repository.ListAvailable).
CREATE INDEX credit_notes_available_idx ON credit_notes (organization_id, customer_id, currency, created_at)
  WHERE status = 'finalized' AND credit_status = 'available' AND balance_amount > 0;

ALTER TABLE credit_notes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON credit_notes
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE credit_note_items (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  credit_note_id uuid NOT NULL,
  position integer NOT NULL,
  -- fee_id is NULL only while the draft invoice of the credit note is
  -- refreshed, between the deletion of its fees and the move of its items to
  -- the regenerated ones.
  fee_id uuid,
  amount bigint NOT NULL,
  precise_amount numeric NOT NULL,
  PRIMARY KEY (organization_id, credit_note_id, position),
  CONSTRAINT credit_note_items_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT credit_note_items_credit_note_fkey FOREIGN KEY (organization_id, credit_note_id) REFERENCES credit_notes (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT credit_note_items_fee_fkey FOREIGN KEY (organization_id, fee_id) REFERENCES fees (organization_id, id)
);

CREATE INDEX credit_note_items_fee_idx ON credit_note_items (organization_id, fee_id);

ALTER TABLE credit_note_items ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON credit_note_items
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE credit_note_applied_taxes (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  credit_note_id uuid NOT NULL,
  position integer NOT NULL,
  tax_id uuid,
  tax_code text NOT NULL,
  tax_name text NOT NULL,
  tax_description text,
  tax_rate numeric NOT NULL,
  base_amount bigint NOT NULL,
  amount bigint NOT NULL,
  precise_amount numeric NOT NULL,
  PRIMARY KEY (organization_id, credit_note_id, position),
  CONSTRAINT credit_note_applied_taxes_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT credit_note_applied_taxes_credit_note_fkey FOREIGN KEY (organization_id, credit_note_id) REFERENCES credit_notes (organization_id, id) ON DELETE CASCADE
);

ALTER TABLE credit_note_applied_taxes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON credit_note_applied_taxes
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Errors recorded on credit notes: a tax provider's refusal of the credit
-- note's report (lago error_details owned by a CreditNote).
CREATE TABLE credit_note_error_details (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  credit_note_id uuid NOT NULL,
  error_code text NOT NULL,
  details jsonb,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT credit_note_error_details_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT credit_note_error_details_credit_note_fkey FOREIGN KEY (organization_id, credit_note_id) REFERENCES credit_notes (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT credit_note_error_details_error_code_check CHECK (error_code IN ('tax_error'))
);

CREATE INDEX credit_note_error_details_credit_note_idx ON credit_note_error_details (organization_id, credit_note_id);

ALTER TABLE credit_note_error_details ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON credit_note_error_details
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
