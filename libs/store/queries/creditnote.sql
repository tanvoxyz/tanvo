-- Credit notes, items and applied taxes.

-- name: CreateCreditNote :execrows
INSERT INTO credit_notes (
  organization_id, id, invoice_id, customer_id, number, sequential_id, status, credit_status, refund_status,
  reason, description, credit_amount, refund_amount, offset_amount, balance_amount,
  sub_total_excluding_taxes_amount, coupons_adjustment_amount, precise_coupons_adjustment_amount,
  taxes_amount, precise_taxes_amount, taxes_rate, total_amount, currency, issuing_date, metadata,
  file, xml_file, refunded_at, voided_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @invoice_id, @customer_id, @number, @sequential_id, @status, @credit_status, @refund_status,
  @reason, @description, @credit_amount, @refund_amount, @offset_amount, @balance_amount,
  @sub_total_excluding_taxes_amount, @coupons_adjustment_amount, @precise_coupons_adjustment_amount,
  @taxes_amount, @precise_taxes_amount, @taxes_rate, @total_amount, @currency, @issuing_date, @metadata,
  @file, @xml_file, @refunded_at, @voided_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateCreditNote :execrows
UPDATE credit_notes SET
  organization_id = @organization_id, id = @id, invoice_id = @invoice_id, customer_id = @customer_id,
  number = @number, sequential_id = @sequential_id, status = @status, credit_status = @credit_status,
  refund_status = @refund_status, reason = @reason, description = @description,
  credit_amount = @credit_amount, refund_amount = @refund_amount, offset_amount = @offset_amount,
  balance_amount = @balance_amount, sub_total_excluding_taxes_amount = @sub_total_excluding_taxes_amount,
  coupons_adjustment_amount = @coupons_adjustment_amount,
  precise_coupons_adjustment_amount = @precise_coupons_adjustment_amount,
  taxes_amount = @taxes_amount, precise_taxes_amount = @precise_taxes_amount, taxes_rate = @taxes_rate,
  total_amount = @total_amount, currency = @currency, issuing_date = @issuing_date, metadata = @metadata,
  file = @file, xml_file = @xml_file, refunded_at = @refunded_at, voided_at = @voided_at,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetCreditNote :one
SELECT * FROM credit_notes WHERE organization_id = @organization_id AND id = @id;

-- name: ListCreditNotes :many
-- The credit note list of the API (creditnote.Filter). @recent lists the
-- newest first (id descending: UUIDv7 order is creation order).
SELECT cn.* FROM credit_notes cn
WHERE cn.organization_id = @organization_id
  AND (sqlc.narg(customer_id)::uuid IS NULL OR cn.customer_id = sqlc.narg(customer_id))
  AND (sqlc.narg(customer_external_id)::text IS NULL OR EXISTS (
       SELECT 1 FROM customers c WHERE c.organization_id = cn.organization_id AND c.id = cn.customer_id
         AND c.external_id = sqlc.narg(customer_external_id)))
  AND (sqlc.narg(invoice_id)::uuid IS NULL OR cn.invoice_id = sqlc.narg(invoice_id))
  AND (sqlc.narg(invoice_number)::text IS NULL AND cardinality(@billing_entity_ids::uuid[]) = 0
       AND sqlc.narg(self_billed)::boolean IS NULL AND sqlc.narg(purchase_order_number)::text IS NULL
       OR EXISTS (
         SELECT 1 FROM invoices i WHERE i.organization_id = cn.organization_id AND i.id = cn.invoice_id
           AND (sqlc.narg(invoice_number)::text IS NULL OR i.number = sqlc.narg(invoice_number))
           AND (cardinality(@billing_entity_ids::uuid[]) = 0 OR i.billing_entity_id = ANY(@billing_entity_ids::uuid[]))
           AND (sqlc.narg(self_billed)::boolean IS NULL OR i.self_billed = sqlc.narg(self_billed))
           AND (sqlc.narg(purchase_order_number)::text IS NULL
                OR lower(i.purchase_order_number) = lower(sqlc.narg(purchase_order_number)))))
  AND (sqlc.narg(currency)::text IS NULL OR cn.currency = sqlc.narg(currency))
  AND (cardinality(@statuses::text[]) = 0 OR cn.status = ANY(@statuses::text[]))
  AND (cardinality(@credit_statuses::text[]) = 0 OR COALESCE(cn.credit_status, '') = ANY(@credit_statuses::text[]))
  AND (cardinality(@refund_statuses::text[]) = 0 OR COALESCE(cn.refund_status, '') = ANY(@refund_statuses::text[]))
  AND (cardinality(@reasons::text[]) = 0 OR cn.reason = ANY(@reasons::text[]))
  AND (cardinality(@types::text[]) = 0
       OR 'credit' = ANY(@types::text[]) AND cn.credit_amount > 0
       OR 'refund' = ANY(@types::text[]) AND cn.refund_amount > 0
       OR 'offset' = ANY(@types::text[]) AND cn.offset_amount > 0)
  AND (sqlc.narg(issuing_date_from)::date IS NULL OR cn.issuing_date >= sqlc.narg(issuing_date_from))
  AND (sqlc.narg(issuing_date_to)::date IS NULL OR cn.issuing_date <= sqlc.narg(issuing_date_to))
  AND (sqlc.narg(amount_from)::bigint IS NULL OR cn.total_amount >= sqlc.narg(amount_from))
  AND (sqlc.narg(amount_to)::bigint IS NULL OR cn.total_amount <= sqlc.narg(amount_to))
  AND (sqlc.narg(search)::text IS NULL
       OR cn.id::text ILIKE sqlc.narg(search) OR cn.number ILIKE sqlc.narg(search)
       OR sqlc.narg(customer_id)::uuid IS NULL AND EXISTS (
         SELECT 1 FROM customers c WHERE c.organization_id = cn.organization_id AND c.id = cn.customer_id
           AND (c.name ILIKE sqlc.narg(search) OR c.firstname ILIKE sqlc.narg(search)
                OR c.lastname ILIKE sqlc.narg(search) OR c.external_id ILIKE sqlc.narg(search)
                OR c.email ILIKE sqlc.narg(search))))
  AND (sqlc.narg(after)::uuid IS NULL
       OR (@recent::boolean AND cn.id < sqlc.narg(after)) OR (NOT @recent::boolean AND cn.id > sqlc.narg(after)))
ORDER BY CASE WHEN @recent::boolean THEN cn.id END DESC, cn.id
LIMIT @lim;

-- name: ListCreditNotesByIDs :many
SELECT * FROM credit_notes WHERE organization_id = @organization_id AND id = ANY(@ids::uuid[]) ORDER BY id;

-- name: ListCreditNotesByInvoice :many
SELECT * FROM credit_notes WHERE organization_id = @organization_id AND invoice_id = @invoice_id ORDER BY id;

-- name: ListAvailableCreditNotes :many
SELECT * FROM credit_notes
WHERE organization_id = @organization_id AND customer_id = @customer_id AND currency = @currency
  AND status = 'finalized' AND credit_status = 'available' AND balance_amount > 0
ORDER BY created_at, id;

-- name: ListCreditNoteItems :many
SELECT * FROM credit_note_items
WHERE organization_id = @organization_id AND credit_note_id = ANY(@credit_note_ids::uuid[])
ORDER BY credit_note_id, position;

-- name: ListCreditNoteAppliedTaxes :many
SELECT * FROM credit_note_applied_taxes
WHERE organization_id = @organization_id AND credit_note_id = ANY(@credit_note_ids::uuid[])
ORDER BY credit_note_id, position;

-- name: DeleteCreditNoteItems :exec
DELETE FROM credit_note_items WHERE organization_id = @organization_id AND credit_note_id = @credit_note_id;

-- name: DetachCreditNoteItemFees :exec
UPDATE credit_note_items i SET fee_id = NULL
FROM credit_notes c
WHERE i.organization_id = @organization_id AND c.organization_id = i.organization_id
  AND c.id = i.credit_note_id AND c.invoice_id = @invoice_id;

-- name: InsertCreditNoteItem :exec
INSERT INTO credit_note_items (organization_id, credit_note_id, position, id, fee_id, amount, precise_amount)
VALUES (@organization_id, @credit_note_id, @position, @id, @fee_id, @amount, @precise_amount);

-- name: DeleteCreditNoteAppliedTaxes :exec
DELETE FROM credit_note_applied_taxes WHERE organization_id = @organization_id AND credit_note_id = @credit_note_id;

-- name: InsertCreditNoteAppliedTax :exec
INSERT INTO credit_note_applied_taxes (
  organization_id, credit_note_id, position, id, tax_id, tax_code, tax_name, tax_description, tax_rate,
  base_amount, amount, precise_amount
) VALUES (
  @organization_id, @credit_note_id, @position, @id, @tax_id, @tax_code, @tax_name, @tax_description, @tax_rate,
  @base_amount, @amount, @precise_amount
);

-- name: CreditedByFee :many
SELECT i.fee_id, SUM(i.amount)::bigint AS amount
FROM credit_note_items i
JOIN credit_notes c ON c.organization_id = i.organization_id AND c.id = i.credit_note_id
WHERE i.organization_id = @organization_id AND i.fee_id = ANY(@fee_ids::uuid[])
  AND c.credit_status IS DISTINCT FROM 'voided' AND c.status <> 'deleted'
GROUP BY i.fee_id;

-- name: MaxCreditNoteSequentialID :one
SELECT COALESCE(MAX(sequential_id), 0)::bigint FROM credit_notes
WHERE organization_id = @organization_id AND invoice_id = @invoice_id;

-- name: GetCreditNoteStatus :one
SELECT status FROM credit_notes WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteDraftCreditNote :execrows
DELETE FROM credit_notes WHERE organization_id = @organization_id AND id = @id AND status = 'draft';
