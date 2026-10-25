-- Wallets, wallet transactions and consumptions, recurring transaction rules.

-- name: CreateWallet :execrows
INSERT INTO wallets (
  organization_id, id, customer_id, code, name, status, currency, rate_amount, priority, expiration_at,
  balance_amount, credits_balance, ongoing_balance_amount, credits_ongoing_balance,
  ongoing_usage_balance_amount, credits_ongoing_usage_balance, consumed_amount, consumed_credits,
  paid_top_up_min_amount, paid_top_up_max_amount, invoice_requires_successful_payment, allowed_fee_types,
  traceable, purchase_order_number, x402_enabled, billing_entity_id, payment_method_type, payment_method_id,
  last_balance_sync_at, last_consumed_credit_at, depleted_ongoing_balance, metadata,
  skip_invoice_custom_sections, invoice_custom_section_ids, terminated_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @customer_id, @code, @name, @status, @currency, @rate_amount, @priority, @expiration_at,
  @balance_amount, @credits_balance, @ongoing_balance_amount, @credits_ongoing_balance,
  @ongoing_usage_balance_amount, @credits_ongoing_usage_balance, @consumed_amount, @consumed_credits,
  @paid_top_up_min_amount, @paid_top_up_max_amount, @invoice_requires_successful_payment, @allowed_fee_types,
  @traceable, @purchase_order_number, @x402_enabled, @billing_entity_id, @payment_method_type, @payment_method_id,
  @last_balance_sync_at, @last_consumed_credit_at, @depleted_ongoing_balance, @metadata,
  @skip_invoice_custom_sections, @invoice_custom_section_ids, @terminated_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateWallet :execrows
UPDATE wallets SET
  organization_id = @organization_id, id = @id, customer_id = @customer_id, code = @code, name = @name,
  status = @status, currency = @currency, rate_amount = @rate_amount, priority = @priority, expiration_at = @expiration_at,
  balance_amount = @balance_amount, credits_balance = @credits_balance,
  ongoing_balance_amount = @ongoing_balance_amount, credits_ongoing_balance = @credits_ongoing_balance,
  ongoing_usage_balance_amount = @ongoing_usage_balance_amount, credits_ongoing_usage_balance = @credits_ongoing_usage_balance,
  consumed_amount = @consumed_amount, consumed_credits = @consumed_credits,
  paid_top_up_min_amount = @paid_top_up_min_amount, paid_top_up_max_amount = @paid_top_up_max_amount,
  invoice_requires_successful_payment = @invoice_requires_successful_payment, allowed_fee_types = @allowed_fee_types,
  traceable = @traceable, purchase_order_number = @purchase_order_number, x402_enabled = @x402_enabled,
  billing_entity_id = @billing_entity_id, payment_method_type = @payment_method_type,
  payment_method_id = @payment_method_id, last_balance_sync_at = @last_balance_sync_at,
  last_consumed_credit_at = @last_consumed_credit_at, depleted_ongoing_balance = @depleted_ongoing_balance,
  metadata = @metadata, skip_invoice_custom_sections = @skip_invoice_custom_sections,
  invoice_custom_section_ids = @invoice_custom_section_ids,
  terminated_at = @terminated_at, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteWalletBillableMetrics :exec
DELETE FROM wallet_billable_metrics WHERE organization_id = @organization_id AND wallet_id = @wallet_id;

-- name: InsertWalletBillableMetrics :exec
INSERT INTO wallet_billable_metrics (organization_id, wallet_id, billable_metric_id, position)
SELECT @organization_id, @wallet_id, m.id, m.ord
FROM unnest(@billable_metric_ids::uuid[]) WITH ORDINALITY AS m(id, ord);

-- name: GetWallet :one
SELECT sqlc.embed(w),
  ARRAY(SELECT m.billable_metric_id FROM wallet_billable_metrics m
        WHERE m.organization_id = w.organization_id AND m.wallet_id = w.id ORDER BY m.position)::uuid[] AS billable_metric_ids
FROM wallets w
WHERE w.organization_id = @organization_id AND w.id = @id;

-- name: GetWalletByCode :one
SELECT sqlc.embed(w),
  ARRAY(SELECT m.billable_metric_id FROM wallet_billable_metrics m
        WHERE m.organization_id = w.organization_id AND m.wallet_id = w.id ORDER BY m.position)::uuid[] AS billable_metric_ids
FROM wallets w
WHERE w.organization_id = @organization_id AND w.customer_id = @customer_id AND w.code = @code AND w.status = 'active';

-- name: ListWallets :many
SELECT sqlc.embed(w),
  ARRAY(SELECT m.billable_metric_id FROM wallet_billable_metrics m
        WHERE m.organization_id = w.organization_id AND m.wallet_id = w.id ORDER BY m.position)::uuid[] AS billable_metric_ids
FROM wallets w
WHERE w.organization_id = @organization_id
  AND (sqlc.narg(customer_id)::uuid IS NULL OR w.customer_id = sqlc.narg(customer_id))
  AND (sqlc.narg(status)::text IS NULL OR w.status = sqlc.narg(status))
  AND (sqlc.narg(after)::uuid IS NULL OR w.id > sqlc.narg(after))
ORDER BY w.id
LIMIT @lim;

-- name: ListWalletsRecent :many
-- ListWallets newest first, then by id (wallets_query.rb:18); the cursor
-- is (@after_at, @after).
SELECT sqlc.embed(w),
  ARRAY(SELECT m.billable_metric_id FROM wallet_billable_metrics m
        WHERE m.organization_id = w.organization_id AND m.wallet_id = w.id ORDER BY m.position)::uuid[] AS billable_metric_ids
FROM wallets w
WHERE w.organization_id = @organization_id
  AND (sqlc.narg(customer_id)::uuid IS NULL OR w.customer_id = sqlc.narg(customer_id))
  AND (sqlc.narg(status)::text IS NULL OR w.status = sqlc.narg(status))
  AND (sqlc.narg(after)::uuid IS NULL
       OR w.created_at < sqlc.narg(after_at)::timestamptz
       OR (w.created_at = sqlc.narg(after_at)::timestamptz AND w.id > sqlc.narg(after)))
ORDER BY w.created_at DESC, w.id
LIMIT @lim;

-- name: ListWalletsActiveForApplication :many
SELECT sqlc.embed(w),
  ARRAY(SELECT m.billable_metric_id FROM wallet_billable_metrics m
        WHERE m.organization_id = w.organization_id AND m.wallet_id = w.id ORDER BY m.position)::uuid[] AS billable_metric_ids
FROM wallets w
WHERE w.organization_id = @organization_id AND w.customer_id = @customer_id AND w.status = 'active'
ORDER BY w.priority, w.expiration_at NULLS LAST, w.created_at, w.id;

-- name: CountActiveWallets :one
SELECT count(*) FROM wallets
WHERE organization_id = @organization_id AND customer_id = @customer_id AND status = 'active';

-- name: ListExpiredWallets :many
SELECT sqlc.embed(w),
  ARRAY(SELECT m.billable_metric_id FROM wallet_billable_metrics m
        WHERE m.organization_id = w.organization_id AND m.wallet_id = w.id ORDER BY m.position)::uuid[] AS billable_metric_ids
FROM wallets w
WHERE w.organization_id = @organization_id AND w.status = 'active' AND w.expiration_at <= @now
ORDER BY w.id;

-- name: CreateWalletTransaction :execrows
INSERT INTO wallet_transactions (
  organization_id, id, wallet_id, transaction_type, transaction_status, status, source, currency,
  amount, credit_amount, remaining_amount, priority, name, invoice_id, credit_note_id,
  recurring_transaction_rule_id, metadata, payment_method_type, payment_method_id,
  invoice_requires_successful_payment, purchase_order_number, voided_invoice_id, billing_entity_id,
  skip_invoice_custom_sections, invoice_custom_section_ids, settled_at, failed_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @wallet_id, @transaction_type, @transaction_status, @status, @source, @currency,
  @amount, @credit_amount, @remaining_amount, @priority, @name, @invoice_id, @credit_note_id,
  @recurring_transaction_rule_id, @metadata, @payment_method_type, @payment_method_id,
  @invoice_requires_successful_payment, @purchase_order_number, @voided_invoice_id, @billing_entity_id,
  @skip_invoice_custom_sections, @invoice_custom_section_ids, @settled_at, @failed_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateWalletTransaction :execrows
UPDATE wallet_transactions SET
  organization_id = @organization_id, id = @id, wallet_id = @wallet_id, transaction_type = @transaction_type,
  transaction_status = @transaction_status, status = @status, source = @source, currency = @currency,
  amount = @amount, credit_amount = @credit_amount, remaining_amount = @remaining_amount, priority = @priority,
  name = @name, invoice_id = @invoice_id, credit_note_id = @credit_note_id,
  recurring_transaction_rule_id = @recurring_transaction_rule_id, metadata = @metadata,
  payment_method_type = @payment_method_type, payment_method_id = @payment_method_id,
  invoice_requires_successful_payment = @invoice_requires_successful_payment,
  purchase_order_number = @purchase_order_number, voided_invoice_id = @voided_invoice_id, billing_entity_id = @billing_entity_id,
  skip_invoice_custom_sections = @skip_invoice_custom_sections, invoice_custom_section_ids = @invoice_custom_section_ids,
  settled_at = @settled_at, failed_at = @failed_at, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetWalletTransaction :one
SELECT * FROM wallet_transactions WHERE organization_id = @organization_id AND id = @id;

-- name: ListWalletTransactions :many
SELECT * FROM wallet_transactions
WHERE organization_id = @organization_id
  AND (sqlc.narg(wallet_id)::uuid IS NULL OR wallet_id = sqlc.narg(wallet_id))
  AND (sqlc.narg(invoice_id)::uuid IS NULL OR invoice_id = sqlc.narg(invoice_id))
  AND (sqlc.narg(transaction_type)::text IS NULL OR transaction_type = sqlc.narg(transaction_type))
  AND (sqlc.narg(transaction_status)::text IS NULL OR transaction_status = sqlc.narg(transaction_status))
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;

-- name: ListWalletTransactionsRecent :many
-- ListWalletTransactions newest first, then by id
-- (wallet_transactions_query.rb:17); the cursor is (@after_at, @after).
SELECT * FROM wallet_transactions
WHERE organization_id = @organization_id
  AND (sqlc.narg(wallet_id)::uuid IS NULL OR wallet_id = sqlc.narg(wallet_id))
  AND (sqlc.narg(invoice_id)::uuid IS NULL OR invoice_id = sqlc.narg(invoice_id))
  AND (sqlc.narg(transaction_type)::text IS NULL OR transaction_type = sqlc.narg(transaction_type))
  AND (sqlc.narg(transaction_status)::text IS NULL OR transaction_status = sqlc.narg(transaction_status))
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(after)::uuid IS NULL
       OR created_at < sqlc.narg(after_at)::timestamptz
       OR (created_at = sqlc.narg(after_at)::timestamptz AND id > sqlc.narg(after)))
ORDER BY created_at DESC, id
LIMIT @lim;

-- name: ListWalletTransactionsByInvoice :many
SELECT * FROM wallet_transactions
WHERE organization_id = @organization_id AND invoice_id = @invoice_id
ORDER BY id;

-- name: GetPendingPurchaseWalletTransaction :one
SELECT * FROM wallet_transactions
WHERE organization_id = @organization_id AND recurring_transaction_rule_id = @recurring_transaction_rule_id
  AND transaction_status = 'purchased' AND status = 'pending'
ORDER BY id
LIMIT 1;

-- name: ListAvailableInboundWalletTransactions :many
SELECT * FROM wallet_transactions
WHERE organization_id = @organization_id AND wallet_id = @wallet_id
  AND transaction_type = 'inbound' AND status = 'settled' AND remaining_amount > 0
ORDER BY priority,
  CASE transaction_status WHEN 'granted' THEN 0 WHEN 'purchased' THEN 1 WHEN 'voided' THEN 2 ELSE 3 END,
  created_at, id;

-- name: CreateWalletTransactionConsumption :execrows
INSERT INTO wallet_transaction_consumptions (
  organization_id, id, inbound_transaction_id, outbound_transaction_id, consumed_amount, created_at, updated_at
) VALUES (
  @organization_id, @id, @inbound_transaction_id, @outbound_transaction_id, @consumed_amount, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: ListWalletTransactionConsumptionsByOutbound :many
SELECT * FROM wallet_transaction_consumptions
WHERE organization_id = @organization_id AND outbound_transaction_id = @outbound_transaction_id
ORDER BY id;

-- name: ListWalletTransactionConsumptionsByInbound :many
SELECT * FROM wallet_transaction_consumptions
WHERE organization_id = @organization_id AND inbound_transaction_id = @inbound_transaction_id
ORDER BY id;

-- name: CreateRecurringTransactionRule :execrows
INSERT INTO recurring_transaction_rules (
  organization_id, id, wallet_id, "trigger", "interval", threshold_credits, method, paid_credits, granted_credits,
  target_ongoing_balance, grants_target_top_up, started_at, expiration_at, status,
  invoice_requires_successful_payment, transaction_metadata, transaction_name, ignore_paid_top_up_limits,
  purchase_order_number, payment_method_type, payment_method_id, skip_invoice_custom_sections, invoice_custom_section_ids,
  terminated_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @wallet_id, @trigger, @interval, @threshold_credits, @method, @paid_credits, @granted_credits,
  @target_ongoing_balance, @grants_target_top_up, @started_at, @expiration_at, @status,
  @invoice_requires_successful_payment, @transaction_metadata, @transaction_name, @ignore_paid_top_up_limits,
  @purchase_order_number, @payment_method_type, @payment_method_id, @skip_invoice_custom_sections, @invoice_custom_section_ids,
  @terminated_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateRecurringTransactionRule :execrows
UPDATE recurring_transaction_rules SET
  organization_id = @organization_id, id = @id, wallet_id = @wallet_id, "trigger" = @trigger, "interval" = @interval,
  threshold_credits = @threshold_credits, method = @method, paid_credits = @paid_credits,
  granted_credits = @granted_credits, target_ongoing_balance = @target_ongoing_balance,
  grants_target_top_up = @grants_target_top_up, started_at = @started_at, expiration_at = @expiration_at,
  status = @status, invoice_requires_successful_payment = @invoice_requires_successful_payment,
  transaction_metadata = @transaction_metadata, transaction_name = @transaction_name,
  ignore_paid_top_up_limits = @ignore_paid_top_up_limits, purchase_order_number = @purchase_order_number,
  payment_method_type = @payment_method_type,
  payment_method_id = @payment_method_id, skip_invoice_custom_sections = @skip_invoice_custom_sections,
  invoice_custom_section_ids = @invoice_custom_section_ids,
  terminated_at = @terminated_at, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetRecurringTransactionRule :one
SELECT * FROM recurring_transaction_rules WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteRecurringTransactionRule :execrows
DELETE FROM recurring_transaction_rules WHERE organization_id = @organization_id AND id = @id;

-- name: ListRecurringTransactionRulesByWallet :many
SELECT * FROM recurring_transaction_rules
WHERE organization_id = @organization_id AND wallet_id = @wallet_id
ORDER BY id;

-- name: ListActiveIntervalRecurringTransactionRules :many
SELECT * FROM recurring_transaction_rules
WHERE organization_id = @organization_id AND "trigger" = 'interval' AND status = 'active'
  AND (expiration_at IS NULL OR expiration_at > @now)
ORDER BY id;

-- name: ListActiveThresholdRecurringTransactionRules :many
SELECT * FROM recurring_transaction_rules
WHERE organization_id = @organization_id AND wallet_id = @wallet_id AND "trigger" = 'threshold' AND status = 'active'
  AND (expiration_at IS NULL OR expiration_at > @now)
ORDER BY id;

-- name: ListRecurringTransactionRulesEligibleForTermination :many
SELECT * FROM recurring_transaction_rules
WHERE organization_id = @organization_id AND status = 'active' AND expiration_at <= @now
ORDER BY id;
