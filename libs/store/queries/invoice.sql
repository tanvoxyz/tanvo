-- Invoices, invoice subscriptions, fees, applied taxes, adjusted fees,
-- settlements and error details.

-- name: CreateInvoice :execrows
INSERT INTO invoices (
  organization_id, id, customer_id, billing_entity_id, invoice_type, status, payment_status, tax_status,
  number, sequential_id, billing_entity_sequential_id, currency,
  fees_amount, progressive_billing_credit_amount, coupons_amount, sub_total_excluding_taxes_amount,
  taxes_amount, taxes_rate, sub_total_including_taxes_amount, credit_notes_amount, prepaid_credit_amount,
  prepaid_granted_credit_amount, prepaid_purchased_credit_amount, total_amount, total_paid_amount, offsetted_amount,
  issuing_date, payment_due_date, expected_finalization_date, applied_grace_period_days, net_payment_term_days,
  payment_overdue, payment_dispute_lost_at, self_billed, voided_invoice_id, skip_automatic_payment,
  payment_method_type, payment_method_id, metadata, file, xml_file, ready_for_payment_processing,
  refresh_requested, finalized_at, voided_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @customer_id, @billing_entity_id, @invoice_type, @status, @payment_status, @tax_status,
  @number, @sequential_id, @billing_entity_sequential_id, @currency,
  @fees_amount, @progressive_billing_credit_amount, @coupons_amount, @sub_total_excluding_taxes_amount,
  @taxes_amount, @taxes_rate, @sub_total_including_taxes_amount, @credit_notes_amount, @prepaid_credit_amount,
  @prepaid_granted_credit_amount, @prepaid_purchased_credit_amount, @total_amount, @total_paid_amount, @offsetted_amount,
  @issuing_date, @payment_due_date, @expected_finalization_date, @applied_grace_period_days, @net_payment_term_days,
  @payment_overdue, @payment_dispute_lost_at, @self_billed, @voided_invoice_id, @skip_automatic_payment,
  @payment_method_type, @payment_method_id, @metadata, @file, @xml_file, @ready_for_payment_processing,
  @refresh_requested, @finalized_at, @voided_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateInvoice :execrows
UPDATE invoices SET
  organization_id = @organization_id, id = @id, customer_id = @customer_id, billing_entity_id = @billing_entity_id,
  invoice_type = @invoice_type, status = @status, payment_status = @payment_status, tax_status = @tax_status,
  number = @number, sequential_id = @sequential_id, billing_entity_sequential_id = @billing_entity_sequential_id,
  currency = @currency,
  fees_amount = @fees_amount, progressive_billing_credit_amount = @progressive_billing_credit_amount,
  coupons_amount = @coupons_amount, sub_total_excluding_taxes_amount = @sub_total_excluding_taxes_amount,
  taxes_amount = @taxes_amount, taxes_rate = @taxes_rate, sub_total_including_taxes_amount = @sub_total_including_taxes_amount,
  credit_notes_amount = @credit_notes_amount, prepaid_credit_amount = @prepaid_credit_amount,
  prepaid_granted_credit_amount = @prepaid_granted_credit_amount, prepaid_purchased_credit_amount = @prepaid_purchased_credit_amount,
  total_amount = @total_amount, total_paid_amount = @total_paid_amount, offsetted_amount = @offsetted_amount,
  issuing_date = @issuing_date, payment_due_date = @payment_due_date, expected_finalization_date = @expected_finalization_date,
  applied_grace_period_days = @applied_grace_period_days, net_payment_term_days = @net_payment_term_days,
  payment_overdue = @payment_overdue, payment_dispute_lost_at = @payment_dispute_lost_at, self_billed = @self_billed,
  voided_invoice_id = @voided_invoice_id, skip_automatic_payment = @skip_automatic_payment,
  payment_method_type = @payment_method_type, payment_method_id = @payment_method_id, metadata = @metadata,
  file = @file, xml_file = @xml_file, ready_for_payment_processing = @ready_for_payment_processing,
  refresh_requested = @refresh_requested, finalized_at = @finalized_at, voided_at = @voided_at,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetInvoice :one
SELECT * FROM invoices WHERE organization_id = @organization_id AND id = @id;

-- name: GetInvoiceByNumber :one
SELECT * FROM invoices WHERE organization_id = @organization_id AND number = @number ORDER BY id LIMIT 1;

-- name: ListInvoices :many
SELECT * FROM invoices
WHERE organization_id = @organization_id
  AND (@include_generating::boolean OR status <> 'generating')
  AND (sqlc.narg(customer_id)::uuid IS NULL OR customer_id = sqlc.narg(customer_id))
  AND (sqlc.narg(billing_entity_id)::uuid IS NULL OR billing_entity_id = sqlc.narg(billing_entity_id))
  AND (cardinality(@statuses::text[]) = 0 OR status = ANY(@statuses::text[]))
  AND (cardinality(@payment_statuses::text[]) = 0 OR payment_status = ANY(@payment_statuses::text[]))
  AND (cardinality(@types::text[]) = 0 OR invoice_type = ANY(@types::text[]))
  AND (cardinality(@ids::uuid[]) = 0 OR id = ANY(@ids::uuid[]))
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;

-- name: ListInvoicesByIDs :many
SELECT * FROM invoices WHERE organization_id = @organization_id AND id = ANY(@ids::uuid[]) ORDER BY id;

-- name: ListInvoiceDraftsToFinalize :many
SELECT * FROM invoices
WHERE organization_id = @organization_id AND status = 'draft'
  AND (expected_finalization_date IS NULL OR expected_finalization_date <= @date::date)
ORDER BY id;

-- name: ListInvoicesRefreshRequested :many
SELECT * FROM invoices
WHERE organization_id = @organization_id AND status = 'draft' AND refresh_requested
ORDER BY id;

-- name: ListInvoiceOverdueCandidates :many
SELECT * FROM invoices
WHERE organization_id = @organization_id AND status = 'finalized' AND NOT payment_overdue
  AND payment_status <> 'succeeded' AND payment_due_date < @date::date
ORDER BY id;

-- name: MaxInvoiceSequentialID :one
SELECT COALESCE(MAX(sequential_id), 0)::bigint FROM invoices
WHERE organization_id = @organization_id AND customer_id = @customer_id AND billing_entity_id = @billing_entity_id;

-- name: MaxInvoiceBillingEntitySequentialID :one
SELECT COALESCE(MAX(billing_entity_sequential_id), 0)::bigint FROM invoices
WHERE organization_id = @organization_id AND billing_entity_id = @billing_entity_id AND NOT self_billed;

-- name: GetInvoiceStatus :one
SELECT status FROM invoices WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteDraftInvoice :execrows
DELETE FROM invoices WHERE organization_id = @organization_id AND id = @id AND status = 'draft';

-- name: CreateInvoiceSubscription :execrows
INSERT INTO invoice_subscriptions (
  organization_id, id, invoice_id, subscription_id, invoicing_reason,
  from_datetime, to_datetime, charges_from_datetime, charges_to_datetime,
  fixed_charges_from_datetime, fixed_charges_to_datetime, billing_timestamp, recurring, created_at
) VALUES (
  @organization_id, @id, @invoice_id, @subscription_id, @invoicing_reason,
  @from_datetime, @to_datetime, @charges_from_datetime, @charges_to_datetime,
  @fixed_charges_from_datetime, @fixed_charges_to_datetime, @billing_timestamp, @recurring, @created_at
)
ON CONFLICT DO NOTHING;

-- name: ListInvoiceSubscriptionsByInvoice :many
SELECT * FROM invoice_subscriptions WHERE organization_id = @organization_id AND invoice_id = @invoice_id ORDER BY id;

-- name: ListInvoiceSubscriptionsBySubscription :many
SELECT * FROM invoice_subscriptions WHERE organization_id = @organization_id AND subscription_id = @subscription_id ORDER BY id;

-- name: FindInvoiceSubscription :one
SELECT * FROM invoice_subscriptions
WHERE organization_id = @organization_id AND subscription_id = @subscription_id
  AND invoicing_reason = @invoicing_reason AND from_datetime = @from_datetime
ORDER BY id
LIMIT 1;

-- name: LastInvoiceSubscription :one
SELECT * FROM invoice_subscriptions
WHERE organization_id = @organization_id AND subscription_id = @subscription_id
ORDER BY from_datetime DESC, billing_timestamp DESC, id DESC
LIMIT 1;

-- name: DeleteInvoiceSubscriptionsByInvoice :exec
DELETE FROM invoice_subscriptions WHERE organization_id = @organization_id AND invoice_id = @invoice_id;

-- name: CreateFee :execrows
INSERT INTO fees (
  organization_id, id, invoice_id, subscription_id, fee_type, charge_id, charge_filter_id, fixed_charge_id,
  add_on_id, commitment_id, wallet_transaction_id, grouped_by, invoice_display_name, description,
  amount, precise_amount, unit_amount, precise_unit_amount, units, total_aggregated_units, events_count,
  amount_details, taxes_amount, taxes_precise_amount, taxes_rate, taxes_base_rate, precise_coupons_amount,
  precise_credit_notes_amount, currency, pay_in_advance, invoiceable, payment_status,
  from_datetime, to_datetime, charges_from_datetime, charges_to_datetime, billing_timestamp, charges_duration_days,
  true_up_parent_fee_id, pricing_unit_usage, applied_taxes, pay_in_advance_event_id,
  pay_in_advance_event_transaction_id, succeeded_at, failed_at, refunded_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @invoice_id, @subscription_id, @fee_type, @charge_id, @charge_filter_id, @fixed_charge_id,
  @add_on_id, @commitment_id, @wallet_transaction_id, @grouped_by, @invoice_display_name, @description,
  @amount, @precise_amount, @unit_amount, @precise_unit_amount, @units, @total_aggregated_units, @events_count,
  @amount_details, @taxes_amount, @taxes_precise_amount, @taxes_rate, @taxes_base_rate, @precise_coupons_amount,
  @precise_credit_notes_amount, @currency, @pay_in_advance, @invoiceable, @payment_status,
  @from_datetime, @to_datetime, @charges_from_datetime, @charges_to_datetime, @billing_timestamp, @charges_duration_days,
  @true_up_parent_fee_id, @pricing_unit_usage, @applied_taxes, @pay_in_advance_event_id,
  @pay_in_advance_event_transaction_id, @succeeded_at, @failed_at, @refunded_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateFee :execrows
UPDATE fees SET
  organization_id = @organization_id, id = @id, invoice_id = @invoice_id, subscription_id = @subscription_id,
  fee_type = @fee_type, charge_id = @charge_id, charge_filter_id = @charge_filter_id, fixed_charge_id = @fixed_charge_id,
  add_on_id = @add_on_id, commitment_id = @commitment_id, wallet_transaction_id = @wallet_transaction_id,
  grouped_by = @grouped_by, invoice_display_name = @invoice_display_name, description = @description,
  amount = @amount, precise_amount = @precise_amount, unit_amount = @unit_amount, precise_unit_amount = @precise_unit_amount,
  units = @units, total_aggregated_units = @total_aggregated_units, events_count = @events_count,
  amount_details = @amount_details, taxes_amount = @taxes_amount, taxes_precise_amount = @taxes_precise_amount,
  taxes_rate = @taxes_rate, taxes_base_rate = @taxes_base_rate, precise_coupons_amount = @precise_coupons_amount,
  precise_credit_notes_amount = @precise_credit_notes_amount, currency = @currency, pay_in_advance = @pay_in_advance,
  invoiceable = @invoiceable, payment_status = @payment_status,
  from_datetime = @from_datetime, to_datetime = @to_datetime, charges_from_datetime = @charges_from_datetime,
  charges_to_datetime = @charges_to_datetime, billing_timestamp = @billing_timestamp,
  charges_duration_days = @charges_duration_days, true_up_parent_fee_id = @true_up_parent_fee_id,
  pricing_unit_usage = @pricing_unit_usage, applied_taxes = @applied_taxes,
  pay_in_advance_event_id = @pay_in_advance_event_id,
  pay_in_advance_event_transaction_id = @pay_in_advance_event_transaction_id,
  succeeded_at = @succeeded_at, failed_at = @failed_at, refunded_at = @refunded_at,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetFee :one
SELECT * FROM fees WHERE organization_id = @organization_id AND id = @id;

-- name: ListFees :many
SELECT * FROM fees
WHERE organization_id = @organization_id
  AND (sqlc.narg(invoice_id)::uuid IS NULL OR invoice_id = sqlc.narg(invoice_id))
  AND (sqlc.narg(subscription_id)::uuid IS NULL OR subscription_id = sqlc.narg(subscription_id))
  AND (sqlc.narg(charge_id)::uuid IS NULL OR charge_id = sqlc.narg(charge_id))
  AND (cardinality(@types::text[]) = 0 OR fee_type = ANY(@types::text[]))
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;

-- name: ListFeesByInvoice :many
SELECT * FROM fees WHERE organization_id = @organization_id AND invoice_id = @invoice_id ORDER BY id;

-- name: ListFeesBySubscriptionPeriod :many
SELECT * FROM fees
WHERE organization_id = @organization_id AND subscription_id = @subscription_id AND from_datetime = @from_datetime
ORDER BY id;

-- name: ListFeesUnbilledInAdvance :many
SELECT * FROM fees
WHERE organization_id = @organization_id AND invoice_id IS NULL AND pay_in_advance AND invoiceable
  AND payment_status = 'succeeded' AND created_at < @before
ORDER BY id;

-- name: ListFeesByCharge :many
SELECT * FROM fees WHERE organization_id = @organization_id AND charge_id = @charge_id ORDER BY id;

-- name: ListFeesByEventTransaction :many
SELECT * FROM fees
WHERE organization_id = @organization_id AND subscription_id = @subscription_id
  AND pay_in_advance_event_transaction_id = @transaction_id
ORDER BY id;

-- name: DeleteFeesByInvoice :exec
DELETE FROM fees WHERE organization_id = @organization_id AND invoice_id = @invoice_id;

-- name: DeleteInvoiceAppliedTaxes :exec
DELETE FROM invoice_applied_taxes WHERE organization_id = @organization_id AND invoice_id = @invoice_id;

-- name: CreateInvoiceAppliedTax :exec
INSERT INTO invoice_applied_taxes (
  organization_id, id, invoice_id, tax_id, tax_code, tax_name, tax_description, tax_rate,
  fees_amount, amount, precise_amount, currency, created_at
) VALUES (
  @organization_id, @id, @invoice_id, @tax_id, @tax_code, @tax_name, @tax_description, @tax_rate,
  @fees_amount, @amount, @precise_amount, @currency, @created_at
);

-- name: ListInvoiceAppliedTaxes :many
SELECT * FROM invoice_applied_taxes WHERE organization_id = @organization_id AND invoice_id = @invoice_id ORDER BY id;

-- name: CreateAdjustedFee :execrows
INSERT INTO adjusted_fees (
  organization_id, id, invoice_id, fee_id, subscription_id, charge_id, charge_filter_id, fixed_charge_id,
  fee_type, grouped_by, mode, invoice_display_name, units, unit_precise_amount, created_at
) VALUES (
  @organization_id, @id, @invoice_id, @fee_id, @subscription_id, @charge_id, @charge_filter_id, @fixed_charge_id,
  @fee_type, @grouped_by, @mode, @invoice_display_name, @units, @unit_precise_amount, @created_at
)
ON CONFLICT DO NOTHING;

-- name: ListAdjustedFeesByInvoice :many
SELECT * FROM adjusted_fees WHERE organization_id = @organization_id AND invoice_id = @invoice_id ORDER BY id;

-- name: DeleteAdjustedFee :execrows
DELETE FROM adjusted_fees WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteAdjustedFeesByInvoice :exec
DELETE FROM adjusted_fees WHERE organization_id = @organization_id AND invoice_id = @invoice_id;

-- name: CreateInvoiceSettlement :execrows
INSERT INTO invoice_settlements (
  organization_id, id, invoice_id, settlement_type, payment_id, credit_note_id, amount, currency, created_at
) VALUES (
  @organization_id, @id, @invoice_id, @settlement_type, @payment_id, @credit_note_id, @amount, @currency, @created_at
)
ON CONFLICT DO NOTHING;

-- name: ListInvoiceSettlementsByInvoice :many
SELECT * FROM invoice_settlements WHERE organization_id = @organization_id AND invoice_id = @invoice_id ORDER BY id;

-- name: CreateInvoiceErrorDetail :execrows
INSERT INTO invoice_error_details (organization_id, id, invoice_id, error_code, details, created_at)
VALUES (@organization_id, @id, @invoice_id, @error_code, @details, @created_at)
ON CONFLICT DO NOTHING;

-- name: ListInvoiceErrorDetailsByInvoice :many
SELECT * FROM invoice_error_details WHERE organization_id = @organization_id AND invoice_id = @invoice_id ORDER BY id;

-- name: DeleteInvoiceErrorDetailsByInvoice :exec
DELETE FROM invoice_error_details
WHERE organization_id = @organization_id AND invoice_id = @invoice_id
  AND (@error_code::text = '' OR error_code = @error_code::text);
