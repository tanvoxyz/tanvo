-- Quotes, quote versions, order forms and orders.

-- name: CreateQuote :execrows
INSERT INTO quotes (
  organization_id, id, number, sequential_id, customer_id, order_type, subscription_id, images, created_at, updated_at
) VALUES (
  @organization_id, @id, @number, @sequential_id, @customer_id, @order_type, @subscription_id, @images, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateQuote :execrows
UPDATE quotes SET
  organization_id = @organization_id, id = @id, number = @number, sequential_id = @sequential_id,
  customer_id = @customer_id, order_type = @order_type, subscription_id = @subscription_id, images = @images,
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
WHERE organization_id = @organization_id;

-- name: ListQuotes :many
SELECT sqlc.embed(q),
  ARRAY(SELECT o.membership_id FROM quote_owners o
        WHERE o.organization_id = q.organization_id AND o.quote_id = q.id ORDER BY o.position)::uuid[] AS owner_ids
FROM quotes q
-- Filters of quotes_query.rb:9-17; by created_at, newest first, then by id
-- (quotes_query.rb:20), the cursor being (@after_at, @after).
WHERE q.organization_id = @organization_id
  AND (cardinality(@customer_ids::uuid[]) = 0 OR q.customer_id = ANY(@customer_ids::uuid[]))
  AND (cardinality(@external_customer_ids::text[]) = 0 OR EXISTS (
        SELECT 1 FROM customers c
        WHERE c.organization_id = q.organization_id AND c.id = q.customer_id
          AND c.external_id = ANY(@external_customer_ids::text[])))
  AND (cardinality(@numbers::text[]) = 0 OR q.number = ANY(@numbers::text[]))
  AND (cardinality(@statuses::text[]) = 0 OR (
        SELECT v.status FROM quote_versions v
        WHERE v.organization_id = q.organization_id AND v.quote_id = q.id
        ORDER BY v.version DESC LIMIT 1) = ANY(@statuses::text[]))
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR q.created_at >= sqlc.narg(from_at))
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR q.created_at <= sqlc.narg(to_at))
  AND (cardinality(@owner_ids::uuid[]) = 0 OR EXISTS (
        SELECT 1 FROM quote_owners o
        WHERE o.organization_id = q.organization_id AND o.quote_id = q.id AND o.membership_id = ANY(@owner_ids::uuid[])))
  AND (cardinality(@order_types::text[]) = 0 OR q.order_type = ANY(@order_types::text[]))
  AND (sqlc.narg(after)::uuid IS NULL
       OR q.created_at < sqlc.narg(after_at)::timestamptz
       OR (q.created_at = sqlc.narg(after_at)::timestamptz AND q.id > sqlc.narg(after)))
ORDER BY q.created_at DESC, q.id
LIMIT @lim;

-- name: DeleteQuoteOwners :exec
DELETE FROM quote_owners WHERE organization_id = @organization_id AND quote_id = @quote_id;

-- name: InsertQuoteOwners :exec
INSERT INTO quote_owners (organization_id, quote_id, membership_id, position)
SELECT @organization_id, @quote_id, m.id, m.ord
FROM unnest(@membership_ids::uuid[]) WITH ORDINALITY AS m(id, ord);

-- name: CreateQuoteVersion :execrows
INSERT INTO quote_versions (
  organization_id, id, quote_id, version, status, void_reason, content, billing_items, mention_variables,
  currency, billing_entity_id, approved_at, voided_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @quote_id, @version, @status, @void_reason, @content, @billing_items, @mention_variables,
  @currency, @billing_entity_id, @approved_at, @voided_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateQuoteVersion :execrows
UPDATE quote_versions SET
  organization_id = @organization_id, id = @id, quote_id = @quote_id, version = @version, status = @status,
  void_reason = @void_reason, content = @content, billing_items = @billing_items,
  mention_variables = @mention_variables, currency = @currency, billing_entity_id = @billing_entity_id,
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
  expires_at, signed_at, signed_document, voided_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @number, @sequential_id, @customer_id, @quote_version_id, @status, @void_reason,
  @expires_at, @signed_at, @signed_document, @voided_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateOrderForm :execrows
UPDATE order_forms SET
  organization_id = @organization_id, id = @id, number = @number, sequential_id = @sequential_id,
  customer_id = @customer_id, quote_version_id = @quote_version_id, status = @status, void_reason = @void_reason,
  expires_at = @expires_at, signed_at = @signed_at, signed_document = @signed_document,
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
WHERE organization_id = @organization_id;

-- name: ListOrderForms :many
-- Filters of order_forms_query.rb:19-31; by created_at, newest first, then
-- by id, the cursor being (@after_at, @after).
SELECT f.* FROM order_forms f
WHERE f.organization_id = @organization_id
  AND (sqlc.narg(customer_id)::uuid IS NULL OR f.customer_id = sqlc.narg(customer_id))
  AND (cardinality(@statuses::text[]) = 0 OR f.status = ANY(@statuses::text[]))
  AND (sqlc.narg(number)::text IS NULL OR f.number = sqlc.narg(number))
  AND (sqlc.narg(search)::text IS NULL OR f.number ILIKE sqlc.narg(search))
  AND (sqlc.narg(created_from)::timestamptz IS NULL OR f.created_at >= sqlc.narg(created_from))
  AND (sqlc.narg(created_to)::timestamptz IS NULL OR f.created_at <= sqlc.narg(created_to))
  AND (sqlc.narg(expires_from)::timestamptz IS NULL OR f.expires_at >= sqlc.narg(expires_from))
  AND (sqlc.narg(expires_to)::timestamptz IS NULL OR f.expires_at <= sqlc.narg(expires_to))
  AND ((sqlc.narg(quote_number)::text IS NULL AND sqlc.narg(owner_id)::uuid IS NULL) OR EXISTS (
        SELECT 1 FROM quote_versions v JOIN quotes q ON q.organization_id = v.organization_id AND q.id = v.quote_id
        WHERE v.organization_id = f.organization_id AND v.id = f.quote_version_id
          AND (sqlc.narg(quote_number)::text IS NULL OR q.number = sqlc.narg(quote_number))
          AND (sqlc.narg(owner_id)::uuid IS NULL OR EXISTS (
                SELECT 1 FROM quote_owners o
                WHERE o.organization_id = q.organization_id AND o.quote_id = q.id AND o.membership_id = sqlc.narg(owner_id)))))
  AND (sqlc.narg(after)::uuid IS NULL
       OR f.created_at < sqlc.narg(after_at)::timestamptz
       OR (f.created_at = sqlc.narg(after_at)::timestamptz AND f.id > sqlc.narg(after)))
ORDER BY f.created_at DESC, f.id
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
WHERE organization_id = @organization_id;

-- name: ListOrders :many
-- Filters of orders_query.rb:20-32; by created_at, newest first, then by
-- id, the cursor being (@after_at, @after).
SELECT o.* FROM orders o
WHERE o.organization_id = @organization_id
  AND (sqlc.narg(customer_id)::uuid IS NULL OR o.customer_id = sqlc.narg(customer_id))
  AND (cardinality(@statuses::text[]) = 0 OR o.status = ANY(@statuses::text[]))
  AND (cardinality(@execution_modes::text[]) = 0 OR o.execution_mode = ANY(@execution_modes::text[]))
  AND (sqlc.narg(number)::text IS NULL OR o.number = sqlc.narg(number))
  AND (sqlc.narg(search)::text IS NULL OR o.number ILIKE sqlc.narg(search))
  AND (sqlc.narg(executed_from)::timestamptz IS NULL OR o.executed_at >= sqlc.narg(executed_from))
  AND (sqlc.narg(executed_to)::timestamptz IS NULL OR o.executed_at <= sqlc.narg(executed_to))
  AND ((sqlc.narg(order_form_number)::text IS NULL AND sqlc.narg(quote_number)::text IS NULL
        AND sqlc.narg(owner_id)::uuid IS NULL AND cardinality(@order_types::text[]) = 0) OR EXISTS (
        SELECT 1 FROM order_forms f
        JOIN quote_versions v ON v.organization_id = f.organization_id AND v.id = f.quote_version_id
        JOIN quotes q ON q.organization_id = v.organization_id AND q.id = v.quote_id
        WHERE f.organization_id = o.organization_id AND f.id = o.order_form_id
          AND (sqlc.narg(order_form_number)::text IS NULL OR f.number = sqlc.narg(order_form_number))
          AND (sqlc.narg(quote_number)::text IS NULL OR q.number = sqlc.narg(quote_number))
          AND (cardinality(@order_types::text[]) = 0 OR q.order_type = ANY(@order_types::text[]))
          AND (sqlc.narg(owner_id)::uuid IS NULL OR EXISTS (
                SELECT 1 FROM quote_owners qo
                WHERE qo.organization_id = q.organization_id AND qo.quote_id = q.id AND qo.membership_id = sqlc.narg(owner_id)))))
  AND (sqlc.narg(after)::uuid IS NULL
       OR o.created_at < sqlc.narg(after_at)::timestamptz
       OR (o.created_at = sqlc.narg(after_at)::timestamptz AND o.id > sqlc.narg(after)))
ORDER BY o.created_at DESC, o.id
LIMIT @lim;

-- name: ListExecutableOrders :many
-- Cross-organization: the execution job scans every organization.
SELECT * FROM orders
WHERE status = 'created' AND execute_at IS NOT NULL AND execute_at <= @now
ORDER BY id
LIMIT @lim;
