-- Foreign keys of the subscription tables (10_subscription.sql) to tables of
-- later files: payment methods (15) and invoices (12).

ALTER TABLE subscriptions
  ADD CONSTRAINT subscriptions_payment_method_fkey FOREIGN KEY (organization_id, payment_method_id) REFERENCES payment_methods (organization_id, id);

ALTER TABLE applied_usage_thresholds
  ADD CONSTRAINT applied_usage_thresholds_invoice_fkey FOREIGN KEY (organization_id, invoice_id) REFERENCES invoices (organization_id, id) ON DELETE CASCADE;
