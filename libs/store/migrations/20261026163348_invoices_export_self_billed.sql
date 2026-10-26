-- Drop view "exports_invoices"
DROP VIEW exports_invoices;
-- Create view "exports_invoices"
CREATE VIEW exports_invoices WITH (security_invoker=true) AS
SELECT organization_id,
    id,
    customer_id,
    billing_entity_id,
    sequential_id,
    billing_entity_sequential_id,
    number,
    invoice_type,
    self_billed,
    status,
    payment_status,
    tax_status,
    issuing_date,
    payment_due_date,
    net_payment_term_days,
    payment_overdue,
    payment_dispute_lost_at,
    currency,
    fees_amount,
    taxes_amount,
    progressive_billing_credit_amount,
    coupons_amount,
    credit_notes_amount,
    sub_total_excluding_taxes_amount,
    sub_total_including_taxes_amount,
    total_amount,
    total_paid_amount,
    (total_amount - total_paid_amount) AS total_due_amount,
    prepaid_credit_amount,
    prepaid_granted_credit_amount,
    prepaid_purchased_credit_amount,
    voided_invoice_id,
    purchase_order_number,
    metadata,
    ( SELECT json_agg(json_build_object('id', ed.id, 'error_code', ed.error_code, 'details', ed.details) ORDER BY ed.created_at, ed.id) AS json_agg
           FROM invoice_error_details ed
          WHERE ((ed.organization_id = i.organization_id) AND (ed.invoice_id = i.id))) AS error_details,
    created_at,
    updated_at,
    finalized_at,
    voided_at
   FROM invoices i
  WHERE ((status = ANY (ARRAY['draft'::text, 'finalized'::text, 'voided'::text, 'failed'::text, 'deleted'::text])) OR ((status = 'open'::text) AND (tax_status = 'pending'::text)));
