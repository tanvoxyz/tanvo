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
  payment_method_type, payment_method_id, purchase_order_number, x402_connection_id, x402_payment_token, metadata, file, xml_file, ready_for_payment_processing,
  refresh_requested, finalized_at, voided_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @customer_id, @billing_entity_id, @invoice_type, @status, @payment_status, @tax_status,
  @number, @sequential_id, @billing_entity_sequential_id, @currency,
  @fees_amount, @progressive_billing_credit_amount, @coupons_amount, @sub_total_excluding_taxes_amount,
  @taxes_amount, @taxes_rate, @sub_total_including_taxes_amount, @credit_notes_amount, @prepaid_credit_amount,
  @prepaid_granted_credit_amount, @prepaid_purchased_credit_amount, @total_amount, @total_paid_amount, @offsetted_amount,
  @issuing_date, @payment_due_date, @expected_finalization_date, @applied_grace_period_days, @net_payment_term_days,
  @payment_overdue, @payment_dispute_lost_at, @self_billed, @voided_invoice_id, @skip_automatic_payment,
  @payment_method_type, @payment_method_id, @purchase_order_number, @x402_connection_id, @x402_payment_token, @metadata, @file, @xml_file, @ready_for_payment_processing,
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
  payment_method_type = @payment_method_type, payment_method_id = @payment_method_id,
  purchase_order_number = @purchase_order_number, x402_connection_id = @x402_connection_id, x402_payment_token = @x402_payment_token, metadata = @metadata,
  file = @file, xml_file = @xml_file, ready_for_payment_processing = @ready_for_payment_processing,
  refresh_requested = @refresh_requested, finalized_at = @finalized_at, voided_at = @voided_at,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetInvoice :one
SELECT * FROM invoices WHERE organization_id = @organization_id AND id = @id;

-- name: GetInvoiceByNumber :one
SELECT * FROM invoices WHERE organization_id = @organization_id AND number = @number ORDER BY id LIMIT 1;

-- name: GetRegeneratedInvoice :one
SELECT * FROM invoices WHERE organization_id = @organization_id AND voided_invoice_id = @voided_invoice_id ORDER BY id LIMIT 1;

-- name: ListInvoices :many
-- The invoice list of the API (invoice.Filter). @recent lists by issuing
-- date, latest first (NULL first), then id descending; the cursor is then
-- (@after_issuing_date, @after) with 'infinity' standing for NULL.
SELECT * FROM invoices i
WHERE i.organization_id = @organization_id
  AND (i.status IN ('draft', 'finalized', 'voided', 'failed')
       OR (@include_unlisted::boolean AND i.status IN ('open', 'closed'))
       OR (@include_generating::boolean AND i.status = 'generating'))
  AND (sqlc.narg(customer_id)::uuid IS NULL OR i.customer_id = sqlc.narg(customer_id))
  AND (sqlc.narg(customer_external_id)::text IS NULL OR EXISTS (
       SELECT 1 FROM customers c
       WHERE c.organization_id = i.organization_id AND c.id = i.customer_id AND c.external_id = sqlc.narg(customer_external_id)))
  AND (sqlc.narg(billing_entity_id)::uuid IS NULL OR i.billing_entity_id = sqlc.narg(billing_entity_id))
  AND (cardinality(@billing_entity_ids::uuid[]) = 0 OR i.billing_entity_id = ANY(@billing_entity_ids::uuid[]))
  AND (sqlc.narg(currency)::text IS NULL OR i.currency = sqlc.narg(currency))
  AND (cardinality(@statuses::text[]) = 0 OR i.status = ANY(@statuses::text[]))
  AND (cardinality(@payment_statuses::text[]) = 0 OR i.payment_status = ANY(@payment_statuses::text[]))
  AND (cardinality(@types::text[]) = 0 OR i.invoice_type = ANY(@types::text[]))
  AND (cardinality(@ids::uuid[]) = 0 OR i.id = ANY(@ids::uuid[]))
  AND (cardinality(@subscription_ids::uuid[]) = 0 OR EXISTS (
       SELECT 1 FROM invoice_subscriptions s
       WHERE s.organization_id = i.organization_id AND s.invoice_id = i.id AND s.subscription_id = ANY(@subscription_ids::uuid[])))
  AND (sqlc.narg(issuing_date_from)::date IS NULL OR i.issuing_date >= sqlc.narg(issuing_date_from))
  AND (sqlc.narg(issuing_date_to)::date IS NULL OR i.issuing_date <= sqlc.narg(issuing_date_to))
  AND (sqlc.narg(amount_from)::bigint IS NULL OR i.total_amount >= sqlc.narg(amount_from))
  AND (sqlc.narg(amount_to)::bigint IS NULL OR i.total_amount <= sqlc.narg(amount_to))
  AND (sqlc.narg(payment_dispute_lost)::boolean IS NULL OR (i.payment_dispute_lost_at IS NOT NULL) = sqlc.narg(payment_dispute_lost))
  AND (sqlc.narg(payment_overdue)::boolean IS NULL OR i.payment_overdue = sqlc.narg(payment_overdue))
  AND (sqlc.narg(partially_paid)::boolean IS NULL
       OR (i.total_amount > i.total_paid_amount AND i.total_paid_amount > 0) = sqlc.narg(partially_paid))
  AND (sqlc.narg(positive_due_amount)::boolean IS NULL
       OR (i.total_amount - i.total_paid_amount > 0) = sqlc.narg(positive_due_amount))
  AND (sqlc.narg(self_billed)::boolean IS NULL OR i.self_billed = sqlc.narg(self_billed))
  AND (sqlc.narg(purchase_order_number)::text IS NULL
       OR lower(i.purchase_order_number) = lower(sqlc.narg(purchase_order_number)))
  AND (cardinality(@settlements::text[]) = 0 OR EXISTS (
       SELECT 1 FROM invoice_settlements st
       WHERE st.organization_id = i.organization_id AND st.invoice_id = i.id AND st.settlement_type = ANY(@settlements::text[])))
  AND i.metadata @> @metadata_present::jsonb
  AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(i.metadata) m WHERE m->>'key' = ANY(@metadata_absent::text[]))
  AND (sqlc.narg(search)::text IS NULL
       OR (sqlc.narg(search_id)::uuid IS NOT NULL AND i.id = sqlc.narg(search_id))
       OR (NOT @search_customers::boolean AND i.number ILIKE sqlc.narg(search))
       OR (@search_customers::boolean AND concat_ws(' ', i.number, i.purchase_order_number,
             (SELECT concat_ws(' ', c.name, c.firstname, c.lastname, c.legal_name, c.external_id, c.email)
              FROM customers c WHERE c.organization_id = i.organization_id AND c.id = i.customer_id)) ILIKE sqlc.narg(search)))
  AND (sqlc.narg(after)::uuid IS NULL
       OR (NOT @recent::boolean AND i.id > sqlc.narg(after))
       OR (@recent::boolean AND (COALESCE(i.issuing_date, 'infinity'::date), i.id)
           < (COALESCE(sqlc.narg(after_issuing_date)::date, 'infinity'::date), sqlc.narg(after)::uuid)))
ORDER BY CASE WHEN @recent::boolean THEN COALESCE(i.issuing_date, 'infinity'::date) END DESC,
  CASE WHEN @recent::boolean THEN i.id END DESC, i.id
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

-- name: RequestInvoiceRefreshByMetric :execrows
-- Flags the drafts billing a subscription whose plan charges the metric
-- (lago Invoice.draft.joins(plans: :billable_metrics)).
UPDATE invoices i SET refresh_requested = true
WHERE i.organization_id = @organization_id AND i.status = 'draft'
  AND EXISTS (
    SELECT 1 FROM invoice_subscriptions isub
    JOIN subscriptions s ON s.organization_id = isub.organization_id AND s.id = isub.subscription_id
    JOIN charges c ON c.organization_id = s.organization_id AND c.plan_id = s.plan_id AND c.deleted_at IS NULL
    WHERE isub.organization_id = i.organization_id AND isub.invoice_id = i.id
      AND c.billable_metric_id = @billable_metric_id
  );

-- name: ListInvoiceOverdueCandidates :many
SELECT * FROM invoices
WHERE organization_id = @organization_id AND status = 'finalized' AND NOT payment_overdue
  AND payment_status <> 'succeeded' AND payment_dispute_lost_at IS NULL AND payment_due_date < @date::date
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
  fixed_charges_from_datetime, fixed_charges_to_datetime, billing_timestamp, recurring, regenerated_invoice_id, created_at
) VALUES (
  @organization_id, @id, @invoice_id, @subscription_id, @invoicing_reason,
  @from_datetime, @to_datetime, @charges_from_datetime, @charges_to_datetime,
  @fixed_charges_from_datetime, @fixed_charges_to_datetime, @billing_timestamp, @recurring, @regenerated_invoice_id, @created_at
)
ON CONFLICT DO NOTHING;

-- name: ListInvoiceSubscriptionsByInvoice :many
SELECT * FROM invoice_subscriptions WHERE organization_id = @organization_id AND invoice_id = @invoice_id ORDER BY id;

-- name: ListInvoiceSubscriptionsBySubscription :many
SELECT * FROM invoice_subscriptions WHERE organization_id = @organization_id AND subscription_id = @subscription_id ORDER BY id;

-- name: FindInvoiceSubscriptionByCharges :one
SELECT * FROM invoice_subscriptions
WHERE organization_id = @organization_id AND subscription_id = @subscription_id AND recurring
  AND charges_from_datetime = @from_datetime AND charges_to_datetime = @to_datetime AND regenerated_invoice_id IS NULL
ORDER BY id
LIMIT 1;

-- name: FindInvoiceSubscriptionByFixedCharges :one
SELECT * FROM invoice_subscriptions
WHERE organization_id = @organization_id AND subscription_id = @subscription_id AND recurring
  AND fixed_charges_from_datetime = @from_datetime AND fixed_charges_to_datetime = @to_datetime AND regenerated_invoice_id IS NULL
ORDER BY id
LIMIT 1;

-- name: FindInvoiceSubscriptionByReason :one
SELECT * FROM invoice_subscriptions
WHERE organization_id = @organization_id AND subscription_id = @subscription_id AND invoicing_reason = @invoicing_reason AND regenerated_invoice_id IS NULL
ORDER BY id
LIMIT 1;

-- name: LastInvoiceSubscription :one
SELECT * FROM invoice_subscriptions
WHERE organization_id = @organization_id AND subscription_id = @subscription_id
ORDER BY from_datetime DESC, billing_timestamp DESC, id DESC
LIMIT 1;

-- name: DeleteInvoiceSubscriptionsByInvoice :exec
DELETE FROM invoice_subscriptions WHERE organization_id = @organization_id AND invoice_id = @invoice_id;

-- name: MarkInvoiceSubscriptionsRegenerated :exec
UPDATE invoice_subscriptions SET regenerated_invoice_id = @regenerated_invoice_id
WHERE organization_id = @organization_id AND invoice_id = @invoice_id;

-- name: CreateFee :execrows
INSERT INTO fees (
  organization_id, id, invoice_id, subscription_id, fee_type, charge_id, charge_filter_id, fixed_charge_id,
  add_on_id, commitment_id, wallet_transaction_id, subscription_rate_card_id, product_id, product_filter_id,
  rate_card_rate_id, rate_phase_id, grouped_by, invoice_display_name, description,
  amount, precise_amount, unit_amount, precise_unit_amount, units, total_aggregated_units, events_count,
  amount_details, taxes_amount, taxes_precise_amount, taxes_rate, taxes_base_rate, precise_coupons_amount,
  precise_credit_notes_amount, currency, pay_in_advance, invoiceable, payment_status,
  from_datetime, to_datetime, charges_from_datetime, charges_to_datetime, billing_timestamp, charges_duration_days,
  fixed_charges_from_datetime, fixed_charges_to_datetime, fixed_charges_duration_days,
  true_up_parent_fee_id, original_fee_id, pricing_unit_usage, applied_taxes, presentation_breakdowns, pay_in_advance_event_id,
  pay_in_advance_event_transaction_id, succeeded_at, failed_at, refunded_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @invoice_id, @subscription_id, @fee_type, @charge_id, @charge_filter_id, @fixed_charge_id,
  @add_on_id, @commitment_id, @wallet_transaction_id, @subscription_rate_card_id, @product_id, @product_filter_id,
  @rate_card_rate_id, @rate_phase_id, @grouped_by, @invoice_display_name, @description,
  @amount, @precise_amount, @unit_amount, @precise_unit_amount, @units, @total_aggregated_units, @events_count,
  @amount_details, @taxes_amount, @taxes_precise_amount, @taxes_rate, @taxes_base_rate, @precise_coupons_amount,
  @precise_credit_notes_amount, @currency, @pay_in_advance, @invoiceable, @payment_status,
  @from_datetime, @to_datetime, @charges_from_datetime, @charges_to_datetime, @billing_timestamp, @charges_duration_days,
  @fixed_charges_from_datetime, @fixed_charges_to_datetime, @fixed_charges_duration_days,
  @true_up_parent_fee_id, @original_fee_id, @pricing_unit_usage, @applied_taxes, @presentation_breakdowns, @pay_in_advance_event_id,
  @pay_in_advance_event_transaction_id, @succeeded_at, @failed_at, @refunded_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateFee :execrows
UPDATE fees SET
  organization_id = @organization_id, id = @id, invoice_id = @invoice_id, subscription_id = @subscription_id,
  fee_type = @fee_type, charge_id = @charge_id, charge_filter_id = @charge_filter_id, fixed_charge_id = @fixed_charge_id,
  add_on_id = @add_on_id, commitment_id = @commitment_id, wallet_transaction_id = @wallet_transaction_id,
  subscription_rate_card_id = @subscription_rate_card_id, product_id = @product_id,
  product_filter_id = @product_filter_id, rate_card_rate_id = @rate_card_rate_id, rate_phase_id = @rate_phase_id,
  grouped_by = @grouped_by, invoice_display_name = @invoice_display_name, description = @description,
  amount = @amount, precise_amount = @precise_amount, unit_amount = @unit_amount, precise_unit_amount = @precise_unit_amount,
  units = @units, total_aggregated_units = @total_aggregated_units, events_count = @events_count,
  amount_details = @amount_details, taxes_amount = @taxes_amount, taxes_precise_amount = @taxes_precise_amount,
  taxes_rate = @taxes_rate, taxes_base_rate = @taxes_base_rate, precise_coupons_amount = @precise_coupons_amount,
  precise_credit_notes_amount = @precise_credit_notes_amount, currency = @currency, pay_in_advance = @pay_in_advance,
  invoiceable = @invoiceable, payment_status = @payment_status,
  from_datetime = @from_datetime, to_datetime = @to_datetime, charges_from_datetime = @charges_from_datetime,
  charges_to_datetime = @charges_to_datetime, billing_timestamp = @billing_timestamp,
  charges_duration_days = @charges_duration_days,
  fixed_charges_from_datetime = @fixed_charges_from_datetime, fixed_charges_to_datetime = @fixed_charges_to_datetime,
  fixed_charges_duration_days = @fixed_charges_duration_days,
  true_up_parent_fee_id = @true_up_parent_fee_id, original_fee_id = @original_fee_id,
  pricing_unit_usage = @pricing_unit_usage, applied_taxes = @applied_taxes,
  presentation_breakdowns = @presentation_breakdowns,
  pay_in_advance_event_id = @pay_in_advance_event_id,
  pay_in_advance_event_transaction_id = @pay_in_advance_event_transaction_id,
  succeeded_at = @succeeded_at, failed_at = @failed_at, refunded_at = @refunded_at,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetFee :one
SELECT * FROM fees WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: ListFees :many
-- The customer of a fee is the customer of its invoice, else of its
-- subscription (lago Fee.from_customer).
SELECT * FROM fees f
WHERE f.organization_id = @organization_id AND f.deleted_at IS NULL
  AND (sqlc.narg(invoice_id)::uuid IS NULL OR f.invoice_id = sqlc.narg(invoice_id))
  AND (sqlc.narg(subscription_id)::uuid IS NULL OR f.subscription_id = sqlc.narg(subscription_id))
  AND (sqlc.narg(charge_id)::uuid IS NULL OR f.charge_id = sqlc.narg(charge_id))
  AND (cardinality(@types::text[]) = 0 OR f.fee_type = ANY(@types::text[]))
  AND (cardinality(@payment_statuses::text[]) = 0 OR f.payment_status = ANY(@payment_statuses::text[]))
  AND (sqlc.narg(currency)::text IS NULL OR f.currency = sqlc.narg(currency))
  AND (sqlc.narg(event_transaction_id)::text IS NULL OR f.pay_in_advance_event_transaction_id = sqlc.narg(event_transaction_id))
  AND ((sqlc.narg(customer_id)::uuid IS NULL AND sqlc.narg(customer_external_id)::text IS NULL) OR EXISTS (
    SELECT 1 FROM customers c
    WHERE c.organization_id = f.organization_id
      AND c.id = CASE WHEN f.invoice_id IS NOT NULL
        THEN (SELECT i.customer_id FROM invoices i WHERE i.organization_id = f.organization_id AND i.id = f.invoice_id)
        ELSE (SELECT s.customer_id FROM subscriptions s WHERE s.organization_id = f.organization_id AND s.id = f.subscription_id)
      END
      AND (sqlc.narg(customer_id)::uuid IS NULL OR c.id = sqlc.narg(customer_id))
      AND (sqlc.narg(customer_external_id)::text IS NULL OR (c.external_id = sqlc.narg(customer_external_id) AND c.deleted_at IS NULL))
  ))
  AND (sqlc.narg(subscription_external_id)::text IS NULL OR EXISTS (
    SELECT 1 FROM subscriptions s
    WHERE s.organization_id = f.organization_id AND s.id = f.subscription_id AND s.external_id = sqlc.narg(subscription_external_id)
  ))
  AND (sqlc.narg(billable_metric_code)::text IS NULL OR EXISTS (
    SELECT 1 FROM charges ch JOIN billable_metrics bm ON bm.organization_id = ch.organization_id AND bm.id = ch.billable_metric_id
    WHERE ch.organization_id = f.organization_id AND ch.id = f.charge_id AND bm.code = sqlc.narg(billable_metric_code)
  ))
  AND (sqlc.narg(created_from)::timestamptz IS NULL OR f.created_at >= sqlc.narg(created_from))
  AND (sqlc.narg(created_to)::timestamptz IS NULL OR f.created_at <= sqlc.narg(created_to))
  AND (sqlc.narg(succeeded_from)::timestamptz IS NULL OR f.succeeded_at >= sqlc.narg(succeeded_from))
  AND (sqlc.narg(succeeded_to)::timestamptz IS NULL OR f.succeeded_at <= sqlc.narg(succeeded_to))
  AND (sqlc.narg(failed_from)::timestamptz IS NULL OR f.failed_at >= sqlc.narg(failed_from))
  AND (sqlc.narg(failed_to)::timestamptz IS NULL OR f.failed_at <= sqlc.narg(failed_to))
  AND (sqlc.narg(refunded_from)::timestamptz IS NULL OR f.refunded_at >= sqlc.narg(refunded_from))
  AND (sqlc.narg(refunded_to)::timestamptz IS NULL OR f.refunded_at <= sqlc.narg(refunded_to))
  -- Oldest first, then by id (fees_query.rb:33); the cursor is
  -- (@after_at, @after).
  AND (sqlc.narg(after)::uuid IS NULL
       OR f.created_at > sqlc.narg(after_at)::timestamptz
       OR (f.created_at = sqlc.narg(after_at)::timestamptz AND f.id > sqlc.narg(after)))
ORDER BY f.created_at, f.id
LIMIT @lim;

-- name: DiscardUninvoicedFee :execrows
UPDATE fees SET deleted_at = @deleted_at, updated_at = @deleted_at
WHERE organization_id = @organization_id AND id = @id AND invoice_id IS NULL AND deleted_at IS NULL;

-- name: ListFeesByInvoice :many
SELECT * FROM fees WHERE organization_id = @organization_id AND invoice_id = @invoice_id AND deleted_at IS NULL ORDER BY id;

-- name: ListFeesBySubscriptionPeriod :many
SELECT * FROM fees
WHERE organization_id = @organization_id AND subscription_id = @subscription_id AND from_datetime = @from_datetime
  AND deleted_at IS NULL
ORDER BY id;

-- name: ListFeesUnbilledInAdvance :many
SELECT * FROM fees
WHERE organization_id = @organization_id AND invoice_id IS NULL AND pay_in_advance AND invoiceable
  AND payment_status = 'succeeded' AND created_at < @before AND deleted_at IS NULL
ORDER BY id;

-- name: ListFeesByCharge :many
SELECT * FROM fees WHERE organization_id = @organization_id AND charge_id = @charge_id AND deleted_at IS NULL ORDER BY id;

-- name: ListFeesByEventTransaction :many
SELECT * FROM fees
WHERE organization_id = @organization_id AND subscription_id = @subscription_id
  AND pay_in_advance_event_transaction_id = @transaction_id AND deleted_at IS NULL
ORDER BY id;

-- name: DeleteFeesByInvoice :exec
DELETE FROM fees WHERE organization_id = @organization_id AND invoice_id = @invoice_id;

-- name: DeleteInvoiceAppliedTaxes :exec
DELETE FROM invoice_applied_taxes WHERE organization_id = @organization_id AND invoice_id = @invoice_id;

-- name: CreateInvoiceAppliedTax :exec
INSERT INTO invoice_applied_taxes (
  organization_id, id, invoice_id, tax_id, tax_code, tax_name, tax_description, tax_rate,
  fees_amount, taxable_base_amount, amount, precise_amount, currency, created_at
) VALUES (
  @organization_id, @id, @invoice_id, @tax_id, @tax_code, @tax_name, @tax_description, @tax_rate,
  @fees_amount, @taxable_base_amount, @amount, @precise_amount, @currency, @created_at
);

-- name: ListInvoiceAppliedTaxes :many
SELECT * FROM invoice_applied_taxes WHERE organization_id = @organization_id AND invoice_id = @invoice_id ORDER BY id;

-- name: CreateAdjustedFee :execrows
INSERT INTO adjusted_fees (
  organization_id, id, invoice_id, fee_id, subscription_id, charge_id, charge_filter_id, fixed_charge_id,
  fee_type, grouped_by, charges_from_datetime, charges_to_datetime, fixed_charges_from_datetime,
  fixed_charges_to_datetime, mode, invoice_display_name, units, unit_precise_amount, created_at
) VALUES (
  @organization_id, @id, @invoice_id, @fee_id, @subscription_id, @charge_id, @charge_filter_id, @fixed_charge_id,
  @fee_type, @grouped_by, @charges_from_datetime, @charges_to_datetime, @fixed_charges_from_datetime,
  @fixed_charges_to_datetime, @mode, @invoice_display_name, @units, @unit_precise_amount, @created_at
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

-- name: CreateInvoiceCredit :execrows
INSERT INTO invoice_credits (
  organization_id, id, invoice_id, applied_coupon_id, credit_note_id, progressive_billing_invoice_id,
  amount, currency, before_taxes, created_at
) VALUES (
  @organization_id, @id, @invoice_id, @applied_coupon_id, @credit_note_id, @progressive_billing_invoice_id,
  @amount, @currency, @before_taxes, @created_at
)
ON CONFLICT DO NOTHING;

-- name: ListInvoiceCreditsByInvoice :many
SELECT * FROM invoice_credits WHERE organization_id = @organization_id AND invoice_id = @invoice_id ORDER BY created_at, id;

-- name: ListInvoiceCreditsByAppliedCoupon :many
SELECT * FROM invoice_credits WHERE organization_id = @organization_id AND applied_coupon_id = @applied_coupon_id ORDER BY created_at, id;

-- name: ListInvoiceCreditsByCreditNote :many
SELECT * FROM invoice_credits WHERE organization_id = @organization_id AND credit_note_id = @credit_note_id ORDER BY created_at, id;

-- name: ListInvoiceCreditsByProgressiveBillingInvoice :many
SELECT * FROM invoice_credits
WHERE organization_id = @organization_id AND progressive_billing_invoice_id = @progressive_billing_invoice_id
ORDER BY created_at, id;

-- name: DeleteInvoiceCreditsByInvoice :exec
DELETE FROM invoice_credits WHERE organization_id = @organization_id AND invoice_id = @invoice_id;
