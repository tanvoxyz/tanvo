-- The custom sections printed on invoices (domain §11.9).

-- name: CreateAppliedInvoiceCustomSection :execrows
INSERT INTO applied_invoice_custom_sections (
  organization_id, id, invoice_id, code, name, display_name, details, created_at
) VALUES (
  @organization_id, @id, @invoice_id, @code, @name, @display_name, @details, @created_at
)
ON CONFLICT DO NOTHING;

-- name: ListAppliedInvoiceCustomSectionsByInvoice :many
SELECT * FROM applied_invoice_custom_sections
WHERE organization_id = @organization_id AND invoice_id = @invoice_id
ORDER BY id;

-- name: DeleteAppliedInvoiceCustomSectionsByInvoice :exec
DELETE FROM applied_invoice_custom_sections
WHERE organization_id = @organization_id AND invoice_id = @invoice_id;
