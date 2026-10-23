-- Foreign keys from the wallet tables (14_wallet.sql) to the payment methods
-- of 15_payment.sql.

ALTER TABLE wallets
  ADD CONSTRAINT wallets_payment_method_fkey FOREIGN KEY (organization_id, payment_method_id) REFERENCES payment_methods (organization_id, id);

ALTER TABLE recurring_transaction_rules
  ADD CONSTRAINT recurring_transaction_rules_payment_method_fkey FOREIGN KEY (organization_id, payment_method_id) REFERENCES payment_methods (organization_id, id);

ALTER TABLE wallet_transactions
  ADD CONSTRAINT wallet_transactions_payment_method_fkey FOREIGN KEY (organization_id, payment_method_id) REFERENCES payment_methods (organization_id, id);
