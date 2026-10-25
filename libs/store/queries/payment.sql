-- Payments, receipts, refunds, payment requests, methods, provider customers,
-- intents and inbound webhooks (domain §15).

-- name: CreatePayment :execrows
INSERT INTO payments (
  organization_id, id, payable_type, invoice_id, payment_request_id, customer_id, payment_kind, status,
  provider_status, amount, currency, reference, paid_at, integration_id, provider_payment_id,
  provider_payment_method_id, provider_payment_data, provider_method_details, payment_method_id,
  created_at, updated_at, error_code
) VALUES (
  @organization_id, @id, @payable_type, @invoice_id, @payment_request_id, @customer_id, @payment_kind, @status,
  @provider_status, @amount, @currency, @reference, @paid_at, @integration_id, @provider_payment_id,
  @provider_payment_method_id, @provider_payment_data, @provider_method_details, @payment_method_id,
  @created_at, @updated_at, @error_code
)
ON CONFLICT DO NOTHING;

-- name: DeletePayment :execrows
DELETE FROM payments WHERE organization_id = @organization_id AND id = @id;

-- name: UpdatePayment :execrows
UPDATE payments SET
  organization_id = @organization_id, id = @id, payable_type = @payable_type, invoice_id = @invoice_id,
  payment_request_id = @payment_request_id, customer_id = @customer_id, payment_kind = @payment_kind, status = @status,
  provider_status = @provider_status, amount = @amount, currency = @currency, reference = @reference, paid_at = @paid_at,
  integration_id = @integration_id, provider_payment_id = @provider_payment_id,
  provider_payment_method_id = @provider_payment_method_id, provider_payment_data = @provider_payment_data,
  provider_method_details = @provider_method_details, payment_method_id = @payment_method_id,
  created_at = @created_at, updated_at = @updated_at, error_code = @error_code
WHERE organization_id = @organization_id AND id = @id;

-- name: GetPayment :one
SELECT * FROM payments WHERE organization_id = @organization_id AND id = @id;

-- name: GetPaymentByProviderPaymentID :one
SELECT * FROM payments
WHERE organization_id = @organization_id AND integration_id = @integration_id AND provider_payment_id = @provider_payment_id
ORDER BY id
LIMIT 1;

-- name: ListPaymentsByInvoice :many
SELECT * FROM payments
WHERE organization_id = @organization_id AND invoice_id = @invoice_id
ORDER BY id;

-- name: ListAbandonedPayments :many
SELECT * FROM payments
WHERE organization_id = @organization_id AND payment_kind = 'provider' AND payable_type = 'invoice'
  AND status = 'processing' AND provider_status = 'requires_action'
  AND updated_at BETWEEN @from_at::timestamptz AND @to_at::timestamptz
ORDER BY id;

-- name: ListPaymentsByPaymentRequest :many
SELECT * FROM payments
WHERE organization_id = @organization_id AND payment_request_id = @payment_request_id
ORDER BY id;

-- name: ListPayments :many
-- The payment list (payment.Filter). @invoice_id matches the invoice's own
-- payments and those of the payment requests including it; @visible drops
-- the payments of generating invoices; @search is a LIKE pattern matched
-- against the provider payment ID, the reference, the invoice number
-- (unless @invoice_id is set) and the customer's names, external ID and
-- email (unless @customer_id is set), @search_id the payment ID; @recent
-- lists by created_at, newest first, then by id ascending
-- (base_query.rb:111-112); its cursor is (@after_at, @after).
SELECT p.* FROM payments p
WHERE p.organization_id = @organization_id
  AND (sqlc.narg(customer_id)::uuid IS NULL OR p.customer_id = sqlc.narg(customer_id))
  AND (sqlc.narg(status)::text IS NULL OR p.status = sqlc.narg(status))
  AND (sqlc.narg(currency)::text IS NULL OR p.currency = sqlc.narg(currency))
  AND (sqlc.narg(invoice_id)::uuid IS NULL OR p.invoice_id = sqlc.narg(invoice_id)
       OR EXISTS (SELECT 1 FROM payment_request_invoices l
                  WHERE l.organization_id = p.organization_id AND l.payment_request_id = p.payment_request_id
                    AND l.invoice_id = sqlc.narg(invoice_id)))
  AND (NOT @visible::boolean OR p.invoice_id IS NULL OR EXISTS (
       SELECT 1 FROM invoices i WHERE i.organization_id = p.organization_id AND i.id = p.invoice_id
         AND i.status NOT IN ('generating', 'deleted')))
  AND (sqlc.narg(search)::text IS NULL
       OR p.provider_payment_id ILIKE sqlc.narg(search)
       OR p.reference ILIKE sqlc.narg(search)
       OR (sqlc.narg(search_id)::uuid IS NOT NULL AND p.id = sqlc.narg(search_id))
       OR (sqlc.narg(invoice_id)::uuid IS NULL AND EXISTS (
            SELECT 1 FROM invoices i WHERE i.organization_id = p.organization_id AND i.id = p.invoice_id
              AND i.number ILIKE sqlc.narg(search)))
       OR (sqlc.narg(customer_id)::uuid IS NULL AND EXISTS (
            SELECT 1 FROM customers c WHERE c.organization_id = p.organization_id AND c.id = p.customer_id
              AND c.deleted_at IS NULL
              AND (c.name ILIKE sqlc.narg(search) OR c.firstname ILIKE sqlc.narg(search)
                   OR c.lastname ILIKE sqlc.narg(search) OR c.external_id ILIKE sqlc.narg(search)
                   OR c.email ILIKE sqlc.narg(search)))))
  AND (sqlc.narg(after)::uuid IS NULL
       OR (@recent::boolean AND (p.created_at < sqlc.narg(after_at)::timestamptz
                                 OR (p.created_at = sqlc.narg(after_at)::timestamptz AND p.id > sqlc.narg(after))))
       OR (NOT @recent::boolean AND p.id > sqlc.narg(after)))
ORDER BY CASE WHEN @recent::boolean THEN p.created_at END DESC, p.id
LIMIT @lim;

-- name: CreatePaymentReceipt :execrows
INSERT INTO payment_receipts (
  organization_id, id, payment_id, billing_entity_id, number, file, xml_file, created_at, updated_at
) VALUES (
  @organization_id, @id, @payment_id, @billing_entity_id, @number, @file, @xml_file, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdatePaymentReceipt :execrows
UPDATE payment_receipts SET
  organization_id = @organization_id, id = @id, payment_id = @payment_id, billing_entity_id = @billing_entity_id,
  number = @number, file = @file, xml_file = @xml_file, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetPaymentReceipt :one
SELECT * FROM payment_receipts WHERE organization_id = @organization_id AND id = @id;

-- name: GetPaymentReceiptByPayment :one
SELECT * FROM payment_receipts WHERE organization_id = @organization_id AND payment_id = @payment_id;

-- name: ListPaymentReceipts :many
-- The receipt list (payment.ReceiptFilter). @invoice_id matches the
-- receipts of the invoice's payments and of the payments of the payment
-- requests including it; @recent lists by created_at, newest first, then
-- by id ascending (base_query.rb:111-112); its cursor is (@after_at,
-- @after).
SELECT r.* FROM payment_receipts r
WHERE r.organization_id = @organization_id
  AND (sqlc.narg(invoice_id)::uuid IS NULL OR EXISTS (
       SELECT 1 FROM payments p WHERE p.organization_id = r.organization_id AND p.id = r.payment_id
         AND (p.invoice_id = sqlc.narg(invoice_id)
              OR EXISTS (SELECT 1 FROM payment_request_invoices l
                         WHERE l.organization_id = p.organization_id AND l.payment_request_id = p.payment_request_id
                           AND l.invoice_id = sqlc.narg(invoice_id)))))
  AND (sqlc.narg(after)::uuid IS NULL
       OR (@recent::boolean AND (r.created_at < sqlc.narg(after_at)::timestamptz
                                 OR (r.created_at = sqlc.narg(after_at)::timestamptz AND r.id > sqlc.narg(after))))
       OR (NOT @recent::boolean AND r.id > sqlc.narg(after)))
ORDER BY CASE WHEN @recent::boolean THEN r.created_at END DESC, r.id
LIMIT @lim;

-- name: CreateRefund :execrows
INSERT INTO refunds (
  organization_id, id, payment_id, reason, credit_note_id, subscription_id, integration_id,
  payment_provider_customer_id, amount, currency, status, provider_refund_id, created_at, updated_at
) VALUES (
  @organization_id, @id, @payment_id, @reason, @credit_note_id, @subscription_id, @integration_id,
  @payment_provider_customer_id, @amount, @currency, @status, @provider_refund_id, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateRefund :execrows
UPDATE refunds SET
  organization_id = @organization_id, id = @id, payment_id = @payment_id, reason = @reason,
  credit_note_id = @credit_note_id, subscription_id = @subscription_id, integration_id = @integration_id,
  payment_provider_customer_id = @payment_provider_customer_id, amount = @amount, currency = @currency,
  status = @status, provider_refund_id = @provider_refund_id, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetRefund :one
SELECT * FROM refunds WHERE organization_id = @organization_id AND id = @id;

-- name: GetRefundByProviderRefundID :one
SELECT * FROM refunds
WHERE organization_id = @organization_id AND integration_id = @integration_id AND provider_refund_id = @provider_refund_id
ORDER BY id
LIMIT 1;

-- name: ListRefundsByCreditNote :many
SELECT * FROM refunds
WHERE organization_id = @organization_id AND credit_note_id = @credit_note_id
ORDER BY id;

-- name: ListRefundsByPayment :many
SELECT * FROM refunds
WHERE organization_id = @organization_id AND payment_id = @payment_id
ORDER BY id;

-- name: CreatePaymentRequest :execrows
INSERT INTO payment_requests (
  organization_id, id, customer_id, amount, currency, email, payment_status, payment_attempts,
  dunning_campaign_id, payment_method_type, payment_method_id, ready_for_payment_processing,
  created_at, updated_at
) VALUES (
  @organization_id, @id, @customer_id, @amount, @currency, @email, @payment_status, @payment_attempts,
  @dunning_campaign_id, @payment_method_type, @payment_method_id, @ready_for_payment_processing,
  @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdatePaymentRequest :execrows
UPDATE payment_requests SET
  organization_id = @organization_id, id = @id, customer_id = @customer_id, amount = @amount, currency = @currency,
  email = @email, payment_status = @payment_status, payment_attempts = @payment_attempts,
  dunning_campaign_id = @dunning_campaign_id, payment_method_type = @payment_method_type,
  payment_method_id = @payment_method_id, ready_for_payment_processing = @ready_for_payment_processing,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetPaymentRequest :one
SELECT sqlc.embed(r),
  ARRAY(SELECT i.invoice_id FROM payment_request_invoices i
        WHERE i.organization_id = r.organization_id AND i.payment_request_id = r.id ORDER BY i.position)::uuid[] AS invoice_ids
FROM payment_requests r
WHERE r.organization_id = @organization_id AND r.id = @id;

-- name: ListPaymentRequests :many
-- The payment request list (payment.RequestFilter). @billing_entity_ids
-- keeps the requests including an invoice of one of the billing entities;
-- @recent lists by created_at, newest first, then by id ascending
-- (base_query.rb:111-112); its cursor is (@after_at, @after).
SELECT sqlc.embed(r),
  ARRAY(SELECT i.invoice_id FROM payment_request_invoices i
        WHERE i.organization_id = r.organization_id AND i.payment_request_id = r.id ORDER BY i.position)::uuid[] AS invoice_ids
FROM payment_requests r
WHERE r.organization_id = @organization_id
  AND (sqlc.narg(customer_id)::uuid IS NULL OR r.customer_id = sqlc.narg(customer_id))
  AND (sqlc.narg(payment_status)::text IS NULL OR r.payment_status = sqlc.narg(payment_status))
  AND (sqlc.narg(currency)::text IS NULL OR r.currency = sqlc.narg(currency))
  AND (cardinality(@billing_entity_ids::uuid[]) = 0 OR EXISTS (
       SELECT 1 FROM payment_request_invoices l
       JOIN invoices i ON i.organization_id = l.organization_id AND i.id = l.invoice_id
       WHERE l.organization_id = r.organization_id AND l.payment_request_id = r.id
         AND i.billing_entity_id = ANY(@billing_entity_ids::uuid[])))
  AND (sqlc.narg(after)::uuid IS NULL
       OR (@recent::boolean AND (r.created_at < sqlc.narg(after_at)::timestamptz
                                 OR (r.created_at = sqlc.narg(after_at)::timestamptz AND r.id > sqlc.narg(after))))
       OR (NOT @recent::boolean AND r.id > sqlc.narg(after)))
ORDER BY CASE WHEN @recent::boolean THEN r.created_at END DESC, r.id
LIMIT @lim;

-- name: ListPaymentRequestsByInvoice :many
SELECT sqlc.embed(r),
  ARRAY(SELECT i.invoice_id FROM payment_request_invoices i
        WHERE i.organization_id = r.organization_id AND i.payment_request_id = r.id ORDER BY i.position)::uuid[] AS invoice_ids
FROM payment_requests r
WHERE r.organization_id = @organization_id
  AND EXISTS (SELECT 1 FROM payment_request_invoices l
              WHERE l.organization_id = r.organization_id AND l.payment_request_id = r.id AND l.invoice_id = @invoice_id)
ORDER BY r.id;

-- name: DeletePaymentRequestInvoices :exec
DELETE FROM payment_request_invoices WHERE organization_id = @organization_id AND payment_request_id = @payment_request_id;

-- name: InsertPaymentRequestInvoices :exec
INSERT INTO payment_request_invoices (organization_id, payment_request_id, invoice_id, position)
SELECT @organization_id, @payment_request_id, i.id, i.ord
FROM unnest(@invoice_ids::uuid[]) WITH ORDINALITY AS i(id, ord);

-- name: CreatePaymentMethod :execrows
INSERT INTO payment_methods (
  organization_id, id, customer_id, integration_id, payment_provider_customer_id, provider_method_id,
  details, is_default, deleted_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @customer_id, @integration_id, @payment_provider_customer_id, @provider_method_id,
  @details, @is_default, @deleted_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdatePaymentMethod :execrows
UPDATE payment_methods SET
  organization_id = @organization_id, id = @id, customer_id = @customer_id, integration_id = @integration_id,
  payment_provider_customer_id = @payment_provider_customer_id, provider_method_id = @provider_method_id,
  details = @details, is_default = @is_default, deleted_at = @deleted_at, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetPaymentMethod :one
SELECT * FROM payment_methods WHERE organization_id = @organization_id AND id = @id;

-- name: GetDefaultPaymentMethod :one
SELECT * FROM payment_methods
WHERE organization_id = @organization_id AND customer_id = @customer_id AND is_default AND deleted_at IS NULL;

-- name: GetPaymentMethodByProviderMethodID :one
SELECT * FROM payment_methods
WHERE organization_id = @organization_id AND integration_id = @integration_id AND provider_method_id = @provider_method_id
  AND deleted_at IS NULL
ORDER BY id
LIMIT 1;

-- name: ListPaymentMethodsByCustomer :many
SELECT * FROM payment_methods
WHERE organization_id = @organization_id AND customer_id = @customer_id AND deleted_at IS NULL
ORDER BY id;

-- name: CreatePaymentProviderCustomer :execrows
INSERT INTO payment_provider_customers (
  organization_id, id, customer_id, integration_id, provider_customer_id, sync_with_provider,
  provider_payment_methods, is_default, deleted_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @customer_id, @integration_id, @provider_customer_id, @sync_with_provider,
  @provider_payment_methods, @is_default, @deleted_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdatePaymentProviderCustomer :execrows
UPDATE payment_provider_customers SET
  organization_id = @organization_id, id = @id, customer_id = @customer_id, integration_id = @integration_id,
  provider_customer_id = @provider_customer_id, sync_with_provider = @sync_with_provider,
  provider_payment_methods = @provider_payment_methods, is_default = @is_default, deleted_at = @deleted_at,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetPaymentProviderCustomer :one
SELECT * FROM payment_provider_customers WHERE organization_id = @organization_id AND id = @id;

-- name: GetPaymentProviderCustomerByCustomer :one
SELECT * FROM payment_provider_customers
WHERE organization_id = @organization_id AND customer_id = @customer_id AND integration_id = @integration_id
  AND deleted_at IS NULL;

-- name: GetPaymentProviderCustomerByProviderCustomerID :one
SELECT * FROM payment_provider_customers
WHERE organization_id = @organization_id AND integration_id = @integration_id
  AND provider_customer_id = @provider_customer_id AND deleted_at IS NULL
ORDER BY id
LIMIT 1;

-- name: ListPaymentProviderCustomersByCustomer :many
SELECT * FROM payment_provider_customers
WHERE organization_id = @organization_id AND customer_id = @customer_id AND deleted_at IS NULL
ORDER BY id;

-- name: ListPaymentProviderCustomersByIntegration :many
SELECT * FROM payment_provider_customers
WHERE organization_id = @organization_id AND integration_id = @integration_id AND deleted_at IS NULL
ORDER BY id;

-- name: CreatePaymentIntent :execrows
INSERT INTO payment_intents (
  organization_id, id, invoice_id, status, payment_url, provider_session_id, expires_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @invoice_id, @status, @payment_url, @provider_session_id, @expires_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdatePaymentIntent :execrows
UPDATE payment_intents SET
  organization_id = @organization_id, id = @id, invoice_id = @invoice_id, status = @status, payment_url = @payment_url,
  provider_session_id = @provider_session_id, expires_at = @expires_at, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetActivePaymentIntentByInvoice :one
SELECT * FROM payment_intents
WHERE organization_id = @organization_id AND invoice_id = @invoice_id AND status = 'active';

-- name: ListPaymentIntentsAwaitingExpiration :many
SELECT * FROM payment_intents
WHERE organization_id = @organization_id AND status = 'active' AND expires_at <= @now
ORDER BY id;

-- name: ListActivePaymentIntents :many
SELECT * FROM payment_intents
WHERE organization_id = @organization_id AND status = 'active'
ORDER BY id;

-- name: PaymentIntentsExist :one
SELECT EXISTS (SELECT 1 FROM payment_intents WHERE organization_id = @organization_id)::boolean;

-- name: CreateInboundWebhook :execrows
INSERT INTO inbound_webhooks (
  organization_id, id, source, integration_id, code, event_type, payload, signature, status, attempts,
  processing_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @source, @integration_id, @code, @event_type, @payload, @signature, @status, @attempts,
  @processing_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateInboundWebhook :execrows
UPDATE inbound_webhooks SET
  organization_id = @organization_id, id = @id, source = @source, integration_id = @integration_id, code = @code,
  event_type = @event_type, payload = @payload, signature = @signature, status = @status, attempts = @attempts,
  processing_at = @processing_at, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetInboundWebhook :one
SELECT * FROM inbound_webhooks WHERE organization_id = @organization_id AND id = @id;

-- name: ListRetriableInboundWebhooks :many
-- Mirrors payment.InboundWebhook.NextRetry: a never-attempted pending
-- webhook is due at reception, any other unfinished one retry_interval
-- after its last attempt (or reception), as long as that falls within
-- retry_window of reception.
SELECT * FROM inbound_webhooks
WHERE organization_id = @organization_id
  AND status <> 'succeeded'
  AND (CASE
    WHEN status = 'pending' AND processing_at IS NULL THEN created_at
    ELSE COALESCE(processing_at, created_at) + sqlc.arg(retry_interval)::interval
  END) <= created_at + sqlc.arg(retry_window)::interval
  AND (CASE
    WHEN status = 'pending' AND processing_at IS NULL THEN created_at
    ELSE COALESCE(processing_at, created_at) + sqlc.arg(retry_interval)::interval
  END) <= @now::timestamptz
ORDER BY id;

-- name: DeleteExpiredInboundWebhooks :execrows
DELETE FROM inbound_webhooks WHERE organization_id = @organization_id AND updated_at <= @cutoff;
