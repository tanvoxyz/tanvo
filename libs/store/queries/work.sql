-- Across organizations: the organizations with due work of a kind, for the
-- clock jobs that dispatch per-organization jobs (organization.Work). Each
-- condition is a superset of the per-organization job's rule.

-- name: ListOrganizationsWithActiveSubscriptions :many
SELECT DISTINCT organization_id FROM subscriptions WHERE status = 'active' ORDER BY organization_id;

-- name: ListOrganizationsWithPendingSubscriptions :many
SELECT DISTINCT organization_id FROM subscriptions
WHERE status = 'pending' AND subscription_at <= @until
ORDER BY organization_id;

-- name: ListOrganizationsWithEndingSubscriptions :many
SELECT DISTINCT organization_id FROM subscriptions
WHERE status = 'active' AND ending_at BETWEEN @since AND @until
ORDER BY organization_id;

-- name: ListOrganizationsWithDraftInvoices :many
SELECT DISTINCT organization_id FROM invoices
WHERE status = 'draft' AND (NOT @refresh_requested::boolean OR refresh_requested)
ORDER BY organization_id;

-- name: ListOrganizationsWithLifetimeUsagesToRecalculate :many
SELECT DISTINCT organization_id FROM lifetime_usages
WHERE recalculate_invoiced_usage
ORDER BY organization_id;

-- name: ListOrganizationsWithCustomersAwaitingWalletRefresh :many
SELECT DISTINCT c.organization_id FROM customers c
WHERE c.awaiting_wallet_refresh AND c.deleted_at IS NULL
  AND EXISTS (SELECT 1 FROM wallets w
              WHERE w.organization_id = c.organization_id AND w.customer_id = c.id AND w.status = 'active')
ORDER BY c.organization_id;

-- name: ListOrganizationsWithExpiredWallets :many
SELECT w.organization_id FROM wallets w WHERE w.status = 'active' AND w.expiration_at <= @until::timestamptz
UNION
SELECT r.organization_id FROM recurring_transaction_rules r WHERE r.status = 'active' AND r.expiration_at <= @until::timestamptz
ORDER BY organization_id;

-- name: ListOrganizationsWithIntervalRules :many
SELECT DISTINCT organization_id FROM recurring_transaction_rules
WHERE status = 'active' AND "trigger" = 'interval'
ORDER BY organization_id;

-- name: ListOrganizationsWithExpiredCoupons :many
SELECT DISTINCT organization_id FROM coupons
WHERE status = 'active' AND expiration = 'time_limit' AND expiration_at <= @until
ORDER BY organization_id;

-- name: ListOrganizationsWithOverdueInvoices :many
SELECT DISTINCT organization_id FROM invoices
WHERE status = 'finalized' AND NOT payment_overdue AND payment_status <> 'succeeded'
  AND payment_dispute_lost_at IS NULL AND payment_due_date <= @date::date
ORDER BY organization_id;

-- name: ListOrganizationsWithAwaitingBillingSegments :many
SELECT DISTINCT organization_id FROM billing_segments
WHERE status IN ('pending', 'processing')
ORDER BY organization_id;

-- name: ListOrganizationsWithStuckGeneratingInvoices :many
SELECT DISTINCT i.organization_id FROM invoices i
WHERE i.status = 'generating' AND i.invoice_type = 'subscription' AND i.created_at < @before::timestamptz
  AND NOT EXISTS (
    SELECT 1 FROM invoice_error_details d
    WHERE d.organization_id = i.organization_id AND d.invoice_id = i.id AND d.error_code = 'invoice_generation_error'
  )
ORDER BY i.organization_id;

-- name: ListOrganizationsWithTaxRetriedInvoices :many
SELECT DISTINCT i.organization_id FROM invoices i
WHERE i.status = 'failed'
  AND EXISTS (
    SELECT 1 FROM invoice_error_details d
    WHERE d.organization_id = i.organization_id AND d.invoice_id = i.id AND d.error_code = 'tax_error'
      AND d.details ? 'tax_error_message' AND d.details ->> 'tax_error_message' ILIKE '%' || @message::text || '%'
  )
ORDER BY i.organization_id;

-- name: ListOrganizationsWithEndingAtSubscriptions :many
SELECT DISTINCT organization_id FROM subscriptions
WHERE status = 'active' AND ending_at >= @since::timestamptz
ORDER BY organization_id;

-- name: ListOrganizationsWithExpirableSubscriptions :many
SELECT DISTINCT s.organization_id FROM subscriptions s
JOIN subscription_activation_rules a ON a.organization_id = s.organization_id AND a.subscription_id = s.id
WHERE s.status = 'incomplete' AND a.status = 'pending' AND a.expires_at <= @until::timestamptz
ORDER BY s.organization_id;

-- name: ListOrganizationsWithExpiringOrderForms :many
SELECT DISTINCT organization_id FROM order_forms
WHERE status = 'generated' AND expires_at <= @until::timestamptz
ORDER BY organization_id;

-- name: ListOrganizationsWithExecutableOrders :many
SELECT DISTINCT organization_id FROM orders
WHERE status = 'created' AND execute_at <= @until::timestamptz
ORDER BY organization_id;

-- name: ListOrganizationsWithDailyUsages :many
SELECT DISTINCT organization_id FROM subscriptions
WHERE status = 'active' AND NOT skip_daily_usage
ORDER BY organization_id;

-- name: ListOrganizationsWithEventsToValidate :many
SELECT o.id FROM organizations o
WHERE EXISTS (SELECT 1 FROM webhook_endpoints w WHERE w.organization_id = o.id)
  AND EXISTS (SELECT 1 FROM events e WHERE e.organization_id = o.id
              AND e.created_at >= @from_at::timestamptz AND e.created_at < @to_at::timestamptz)
ORDER BY o.id;

-- name: ListOrganizationsWithExpiredRecordDeletions :many
SELECT DISTINCT organization_id FROM record_deletions
WHERE deleted_at < @cutoff::timestamptz
ORDER BY organization_id;

-- name: ListOrganizationsWithDunningCustomers :many
SELECT DISTINCT i.organization_id FROM invoices i
JOIN customers c ON c.id = i.customer_id
WHERE i.payment_overdue AND NOT i.self_billed
  AND NOT c.exclude_from_dunning_campaign AND c.deleted_at IS NULL
ORDER BY i.organization_id;

-- name: ListOrganizationsWithInboundWebhooks :many
SELECT DISTINCT organization_id FROM inbound_webhooks
WHERE status <> 'succeeded' AND created_at >= @since::timestamptz
ORDER BY organization_id;

-- name: ListOrganizationsWithExpiredInboundWebhooks :many
SELECT DISTINCT organization_id FROM inbound_webhooks
WHERE updated_at <= @cutoff::timestamptz
ORDER BY organization_id;

-- name: ListOrganizationsWithAbandonedPayments :many
SELECT DISTINCT organization_id FROM payments
WHERE payment_kind = 'provider' AND payable_type = 'invoice' AND status = 'processing'
  AND provider_status = 'requires_action'
  AND updated_at BETWEEN @from_at::timestamptz AND @to_at::timestamptz
ORDER BY organization_id;
