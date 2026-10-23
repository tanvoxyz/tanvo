-- Quotes, quote versions, order forms and orders.

-- name: CreateQuote :execrows
INSERT INTO quotes (
  organization_id, id, number, sequential_id, customer_id, order_type, subscription_id, created_at, updated_at
) VALUES (
  @organization_id, @id, @number, @sequential_id, @customer_id, @order_type, @subscription_id, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateQuote :execrows
UPDATE quotes SET
  organization_id = @organization_id, id = @id, number = @number, sequential_id = @sequential_id,
  customer_id = @customer_id, order_type = @order_type, subscription_id = @subscription_id,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetQuote :one
SELECT sqlc.embed(q),
  ARRAY(SELECT o.membership_id FROM quote_owners o
        WHERE o.organization_id = q.organization_id AND o.quote_id = q.id ORDER BY o.position)::uuid[] AS owner_ids
FROM quotes q
WHERE q.organization_id = @organization_id AND q.id = @id;

-- name: GetQuoteByNumber :one
SELECT sqlc.embed(q),
  ARRAY(SELECT o.membership_id FROM quote_owners o
        WHERE o.organization_id = q.organization_id AND o.quote_id = q.id ORDER BY o.position)::uuid[] AS owner_ids
FROM quotes q
WHERE q.organization_id = @organization_id AND q.number = @number;

-- name: NextQuoteSequence :one
SELECT (COALESCE(MAX(sequential_id), 0) + 1)::integer AS next
FROM quotes
WHERE organization_id = @organization_id AND created_at >= @year_start AND created_at < @year_end;

-- name: ListQuotes :many
SELECT sqlc.embed(q),
  ARRAY(SELECT o.membership_id FROM quote_owners o
        WHERE o.organization_id = q.organization_id AND o.quote_id = q.id ORDER BY o.position)::uuid[] AS owner_ids
FROM quotes q
WHERE q.organization_id = @organization_id
  AND (sqlc.narg(customer_id)::uuid IS NULL OR q.customer_id = sqlc.narg(customer_id))
  AND (cardinality(@order_types::text[]) = 0 OR q.order_type = ANY(@order_types::text[]))
  AND (sqlc.narg(owner_id)::uuid IS NULL OR EXISTS (
        SELECT 1 FROM quote_owners o
        WHERE o.organization_id = q.organization_id AND o.quote_id = q.id AND o.membership_id = sqlc.narg(owner_id)))
  AND (sqlc.narg(after)::uuid IS NULL OR q.id > sqlc.narg(after))
ORDER BY q.id
LIMIT @lim;

-- name: DeleteQuoteOwners :exec
DELETE FROM quote_owners WHERE organization_id = @organization_id AND quote_id = @quote_id;

-- name: InsertQuoteOwners :exec
INSERT INTO quote_owners (organization_id, quote_id, membership_id, position)
SELECT @organization_id, @quote_id, m.id, m.ord
FROM unnest(@membership_ids::uuid[]) WITH ORDINALITY AS m(id, ord);

-- name: CreateQuoteVersion :execrows
INSERT INTO quote_versions (
  organization_id, id, quote_id, version, status, void_reason, content, currency, billing_entity_id,
  approved_at, voided_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @quote_id, @version, @status, @void_reason, @content, @currency, @billing_entity_id,
  @approved_at, @voided_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateQuoteVersion :execrows
UPDATE quote_versions SET
  organization_id = @organization_id, id = @id, quote_id = @quote_id, version = @version, status = @status,
  void_reason = @void_reason, content = @content, currency = @currency, billing_entity_id = @billing_entity_id,
  approved_at = @approved_at, voided_at = @voided_at, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetQuoteVersion :one
SELECT * FROM quote_versions WHERE organization_id = @organization_id AND id = @id;

-- name: ListQuoteVersionsByQuote :many
SELECT * FROM quote_versions
WHERE organization_id = @organization_id AND quote_id = @quote_id
ORDER BY version DESC;

-- name: GetCurrentQuoteVersion :one
SELECT * FROM quote_versions
WHERE organization_id = @organization_id AND quote_id = @quote_id
ORDER BY version DESC
LIMIT 1;

-- name: CreateOrderForm :execrows
INSERT INTO order_forms (
  organization_id, id, number, sequential_id, customer_id, quote_version_id, status, void_reason,
  expires_at, signed_at, signed_by, signed_document, voided_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @number, @sequential_id, @customer_id, @quote_version_id, @status, @void_reason,
  @expires_at, @signed_at, @signed_by, @signed_document, @voided_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateOrderForm :execrows
UPDATE order_forms SET
  organization_id = @organization_id, id = @id, number = @number, sequential_id = @sequential_id,
  customer_id = @customer_id, quote_version_id = @quote_version_id, status = @status, void_reason = @void_reason,
  expires_at = @expires_at, signed_at = @signed_at, signed_by = @signed_by, signed_document = @signed_document,
  voided_at = @voided_at, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetOrderForm :one
SELECT * FROM order_forms WHERE organization_id = @organization_id AND id = @id;

-- name: GetOrderFormByNumber :one
SELECT * FROM order_forms WHERE organization_id = @organization_id AND number = @number;

-- name: GetOrderFormByVersion :one
SELECT * FROM order_forms WHERE organization_id = @organization_id AND quote_version_id = @quote_version_id;

-- name: NextOrderFormSequence :one
SELECT (COALESCE(MAX(sequential_id), 0) + 1)::integer AS next
FROM order_forms
WHERE organization_id = @organization_id AND created_at >= @year_start AND created_at < @year_end;

-- name: ListOrderForms :many
SELECT * FROM order_forms
WHERE organization_id = @organization_id
  AND (sqlc.narg(customer_id)::uuid IS NULL OR customer_id = sqlc.narg(customer_id))
  AND (cardinality(@statuses::text[]) = 0 OR status = ANY(@statuses::text[]))
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;

-- name: ListExpiringOrderForms :many
-- Cross-organization: the expiry job scans every organization.
SELECT * FROM order_forms
WHERE status = 'generated' AND expires_at IS NOT NULL AND expires_at < @before
ORDER BY id
LIMIT @lim;

-- name: CreateOrder :execrows
INSERT INTO orders (
  organization_id, id, number, sequential_id, customer_id, order_form_id, status, execution_mode,
  execute_at, executed_at, execution_record, created_at, updated_at
) VALUES (
  @organization_id, @id, @number, @sequential_id, @customer_id, @order_form_id, @status, @execution_mode,
  @execute_at, @executed_at, @execution_record, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateOrder :execrows
UPDATE orders SET
  organization_id = @organization_id, id = @id, number = @number, sequential_id = @sequential_id,
  customer_id = @customer_id, order_form_id = @order_form_id, status = @status, execution_mode = @execution_mode,
  execute_at = @execute_at, executed_at = @executed_at, execution_record = @execution_record,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetOrder :one
SELECT * FROM orders WHERE organization_id = @organization_id AND id = @id;

-- name: GetOrderByNumber :one
SELECT * FROM orders WHERE organization_id = @organization_id AND number = @number;

-- name: NextOrderSequence :one
SELECT (COALESCE(MAX(sequential_id), 0) + 1)::integer AS next
FROM orders
WHERE organization_id = @organization_id AND created_at >= @year_start AND created_at < @year_end;

-- name: ListOrders :many
SELECT * FROM orders
WHERE organization_id = @organization_id
  AND (sqlc.narg(customer_id)::uuid IS NULL OR customer_id = sqlc.narg(customer_id))
  AND (cardinality(@statuses::text[]) = 0 OR status = ANY(@statuses::text[]))
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;

-- name: ListExecutableOrders :many
-- Cross-organization: the execution job scans every organization.
SELECT * FROM orders
WHERE status = 'created' AND execute_at IS NOT NULL AND execute_at <= @now
ORDER BY id
LIMIT @lim;
