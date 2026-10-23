-- Foreign keys of the catalog feature tables (06_catalog_features.sql) to
-- subscriptions, which 10_subscription.sql creates later.

ALTER TABLE usage_thresholds
  ADD CONSTRAINT usage_thresholds_subscription_fkey FOREIGN KEY (organization_id, subscription_id) REFERENCES subscriptions (organization_id, id);

ALTER TABLE entitlements
  ADD CONSTRAINT entitlements_subscription_fkey FOREIGN KEY (organization_id, subscription_id) REFERENCES subscriptions (organization_id, id);

ALTER TABLE entitlement_removals
  ADD CONSTRAINT entitlement_removals_subscription_fkey FOREIGN KEY (organization_id, subscription_id) REFERENCES subscriptions (organization_id, id);
