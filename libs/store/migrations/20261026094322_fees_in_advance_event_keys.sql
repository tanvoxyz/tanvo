-- atlas:txmode none

-- No release of Tanvo predates these indexes, so no database holds two
-- in-advance fees of one event that they would reject.
-- atlas:nolint MF101
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS "fees_in_advance_charge_event_key" ON "fees" ("organization_id", "subscription_id", "pay_in_advance_event_transaction_id", "charge_id") WHERE (pay_in_advance AND (pay_in_advance_event_transaction_id IS NOT NULL) AND (charge_filter_id IS NULL) AND (original_fee_id IS NULL) AND (deleted_at IS NULL));
-- atlas:nolint MF101
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS "fees_in_advance_charge_filter_event_key" ON "fees" ("organization_id", "subscription_id", "pay_in_advance_event_transaction_id", "charge_id", "charge_filter_id") WHERE (pay_in_advance AND (pay_in_advance_event_transaction_id IS NOT NULL) AND (charge_filter_id IS NOT NULL) AND (original_fee_id IS NULL) AND (deleted_at IS NULL));
-- atlas:nolint MF101
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS "fees_in_advance_rate_card_event_key" ON "fees" ("organization_id", "subscription_id", "pay_in_advance_event_transaction_id", "subscription_rate_card_id") WHERE (pay_in_advance AND (pay_in_advance_event_transaction_id IS NOT NULL) AND (charge_id IS NULL) AND (subscription_rate_card_id IS NOT NULL) AND (original_fee_id IS NULL) AND (deleted_at IS NULL));
