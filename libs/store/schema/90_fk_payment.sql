-- Foreign keys of the payment tables (15_payment.sql) to the provider
-- connections, which are integrations (16_integration.sql).

ALTER TABLE payment_provider_customers
  ADD CONSTRAINT payment_provider_customers_integration_fkey FOREIGN KEY (organization_id, integration_id) REFERENCES integrations (organization_id, id);

ALTER TABLE payment_methods
  ADD CONSTRAINT payment_methods_integration_fkey FOREIGN KEY (organization_id, integration_id) REFERENCES integrations (organization_id, id);

ALTER TABLE payments
  ADD CONSTRAINT payments_integration_fkey FOREIGN KEY (organization_id, integration_id) REFERENCES integrations (organization_id, id);

ALTER TABLE refunds
  ADD CONSTRAINT refunds_integration_fkey FOREIGN KEY (organization_id, integration_id) REFERENCES integrations (organization_id, id);

ALTER TABLE inbound_webhooks
  ADD CONSTRAINT inbound_webhooks_integration_fkey FOREIGN KEY (organization_id, integration_id) REFERENCES integrations (organization_id, id);
