-- Foreign keys of the invoice tables (12_invoice.sql) to tables of later
-- files: credit notes, wallet transactions, payments and payment methods.

ALTER TABLE invoices
  ADD CONSTRAINT invoices_payment_method_fkey FOREIGN KEY (organization_id, payment_method_id) REFERENCES payment_methods (organization_id, id);

ALTER TABLE fees
  ADD CONSTRAINT fees_wallet_transaction_fkey FOREIGN KEY (organization_id, wallet_transaction_id) REFERENCES wallet_transactions (organization_id, id);

ALTER TABLE invoice_settlements
  ADD CONSTRAINT invoice_settlements_payment_fkey FOREIGN KEY (organization_id, payment_id) REFERENCES payments (organization_id, id),
  ADD CONSTRAINT invoice_settlements_credit_note_fkey FOREIGN KEY (organization_id, credit_note_id) REFERENCES credit_notes (organization_id, id);

ALTER TABLE invoice_credits
  ADD CONSTRAINT invoice_credits_credit_note_fkey FOREIGN KEY (organization_id, credit_note_id) REFERENCES credit_notes (organization_id, id);
