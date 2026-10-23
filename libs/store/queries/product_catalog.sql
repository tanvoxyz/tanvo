-- The product catalog (schema/26_product_catalog.sql). Rows are
-- discarded, never deleted: Get queries return discarded rows for history,
-- every other read sees kept rows only.

-- name: CreateProductCategory :execrows
INSERT INTO product_categories (
  organization_id, id, code, name, description, invoice_display_name, created_at, updated_at
) VALUES (
  @organization_id, @id, @code, @name, @description, @invoice_display_name, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateProductCategory :execrows
UPDATE product_categories SET
  code = @code, name = @name, description = @description, invoice_display_name = @invoice_display_name,
  updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DiscardProductCategory :execrows
UPDATE product_categories SET deleted_at = @at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: ListProductIDsOfCategory :many
SELECT id FROM products
WHERE organization_id = @organization_id AND product_category_id = @product_category_id AND deleted_at IS NULL
ORDER BY created_at, id;

-- name: GetProductCategory :one
SELECT * FROM product_categories WHERE organization_id = @organization_id AND id = @id;

-- name: GetProductCategoryByCode :one
SELECT * FROM product_categories WHERE organization_id = @organization_id AND code = @code AND deleted_at IS NULL;

-- name: ListProductCategories :many
-- Newest first, then by id; the cursor is (@after_at, @after).
SELECT * FROM product_categories
WHERE organization_id = @organization_id AND deleted_at IS NULL
  AND (sqlc.narg(search)::text IS NULL OR name ILIKE sqlc.narg(search) OR code ILIKE sqlc.narg(search))
  AND (sqlc.narg(after)::uuid IS NULL
       OR created_at < sqlc.narg(after_at)::timestamptz
       OR (created_at = sqlc.narg(after_at)::timestamptz AND id > sqlc.narg(after)))
ORDER BY created_at DESC, id
LIMIT @lim;

-- name: CreateProduct :execrows
INSERT INTO products (
  organization_id, id, product_category_id, billable_metric_id, product_type, code, name, description,
  invoice_display_name, created_at, updated_at
) VALUES (
  @organization_id, @id, @product_category_id, @billable_metric_id, @product_type, @code, @name, @description,
  @invoice_display_name, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateProduct :execrows
UPDATE products SET
  product_category_id = @product_category_id, code = @code, name = @name, description = @description,
  invoice_display_name = @invoice_display_name, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DiscardProduct :execrows
UPDATE products SET deleted_at = @at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DiscardProductFilterValuesOfProduct :exec
UPDATE product_filter_values v SET deleted_at = @at
FROM product_filters f
WHERE v.organization_id = @organization_id AND f.organization_id = v.organization_id AND f.id = v.product_filter_id
  AND f.product_id = @product_id AND f.deleted_at IS NULL AND v.deleted_at IS NULL;

-- name: DiscardProductFiltersOfProduct :exec
UPDATE product_filters SET deleted_at = @at
WHERE organization_id = @organization_id AND product_id = @product_id AND deleted_at IS NULL;

-- name: DiscardRatesOfProduct :exec
UPDATE rate_card_rates r SET deleted_at = @at
FROM rate_cards c
WHERE r.organization_id = @organization_id AND c.organization_id = r.organization_id AND c.id = r.rate_card_id
  AND c.product_id = @product_id AND c.deleted_at IS NULL AND r.deleted_at IS NULL;

-- name: DiscardRateCardsOfProduct :exec
UPDATE rate_cards SET deleted_at = @at
WHERE organization_id = @organization_id AND product_id = @product_id AND deleted_at IS NULL;

-- name: GetProduct :one
SELECT * FROM products WHERE organization_id = @organization_id AND id = @id;

-- name: GetProductByCode :one
SELECT * FROM products WHERE organization_id = @organization_id AND code = @code AND deleted_at IS NULL;

-- name: ListProducts :many
-- Newest first, then by id; the cursor is (@after_at, @after). The
-- category restriction keeps products of @category_ids, and uncategorized
-- ones when @without_category.
SELECT * FROM products
WHERE organization_id = @organization_id AND deleted_at IS NULL
  AND (sqlc.narg(search)::text IS NULL OR name ILIKE sqlc.narg(search) OR code ILIKE sqlc.narg(search))
  AND (sqlc.narg(product_type)::text IS NULL OR product_type = sqlc.narg(product_type))
  AND ((cardinality(@category_ids::uuid[]) = 0 AND NOT @without_category::boolean)
       OR product_category_id = ANY(@category_ids::uuid[])
       OR (@without_category::boolean AND product_category_id IS NULL))
  AND (sqlc.narg(after)::uuid IS NULL
       OR created_at < sqlc.narg(after_at)::timestamptz
       OR (created_at = sqlc.narg(after_at)::timestamptz AND id > sqlc.narg(after)))
ORDER BY created_at DESC, id
LIMIT @lim;

-- name: CountProducts :one
SELECT count(*) FROM products
WHERE organization_id = @organization_id AND deleted_at IS NULL
  AND (sqlc.narg(search)::text IS NULL OR name ILIKE sqlc.narg(search) OR code ILIKE sqlc.narg(search))
  AND (sqlc.narg(product_type)::text IS NULL OR product_type = sqlc.narg(product_type))
  AND ((cardinality(@category_ids::uuid[]) = 0 AND NOT @without_category::boolean)
       OR product_category_id = ANY(@category_ids::uuid[])
       OR (@without_category::boolean AND product_category_id IS NULL));

-- name: CreateProductFilter :execrows
INSERT INTO product_filters (
  organization_id, id, product_id, code, name, description, invoice_display_name, created_at, updated_at
) VALUES (
  @organization_id, @id, @product_id, @code, @name, @description, @invoice_display_name, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateProductFilter :execrows
UPDATE product_filters SET
  code = @code, name = @name, description = @description, invoice_display_name = @invoice_display_name,
  updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DiscardProductFilter :execrows
UPDATE product_filters SET deleted_at = @at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DiscardRatesOfProductFilter :exec
UPDATE rate_card_rates r SET deleted_at = @at
FROM rate_cards c
WHERE r.organization_id = @organization_id AND c.organization_id = r.organization_id AND c.id = r.rate_card_id
  AND c.product_filter_id = @product_filter_id AND c.deleted_at IS NULL AND r.deleted_at IS NULL;

-- name: DiscardRateCardsOfProductFilter :exec
UPDATE rate_cards SET deleted_at = @at
WHERE organization_id = @organization_id AND product_filter_id = @product_filter_id AND deleted_at IS NULL;

-- name: GetProductFilter :one
SELECT * FROM product_filters WHERE organization_id = @organization_id AND id = @id;

-- name: GetProductFilterByCode :one
SELECT * FROM product_filters
WHERE organization_id = @organization_id AND product_id = @product_id AND code = @code AND deleted_at IS NULL;

-- name: ListProductFilters :many
-- Oldest first, then by id (product_filter.rb:28); the cursor is
-- (@after_at, @after).
SELECT * FROM product_filters
WHERE organization_id = @organization_id AND product_id = @product_id AND deleted_at IS NULL
  AND (sqlc.narg(search)::text IS NULL OR name ILIKE sqlc.narg(search) OR code ILIKE sqlc.narg(search))
  AND (sqlc.narg(after)::uuid IS NULL
       OR created_at > sqlc.narg(after_at)::timestamptz
       OR (created_at = sqlc.narg(after_at)::timestamptz AND id > sqlc.narg(after)))
ORDER BY created_at, id
LIMIT @lim;

-- name: CountProductFilters :one
SELECT count(*) FROM product_filters
WHERE organization_id = @organization_id AND product_id = @product_id AND deleted_at IS NULL;

-- name: ProductFilterValuesReferenceMetric :one
SELECT EXISTS (
  SELECT 1 FROM product_filter_values v
  JOIN billable_metric_filters f ON f.organization_id = v.organization_id AND f.id = v.billable_metric_filter_id
  WHERE v.organization_id = @organization_id AND f.billable_metric_id = @billable_metric_id
    AND f.deleted_at IS NULL AND v.deleted_at IS NULL
)::boolean;

-- name: ListProductFilterValuesOfMetric :many
-- The distinct values that select a filter of the metric.
SELECT DISTINCT v.billable_metric_filter_id, v.value
FROM product_filter_values v
JOIN billable_metric_filters f ON f.organization_id = v.organization_id AND f.id = v.billable_metric_filter_id
WHERE v.organization_id = @organization_id AND f.billable_metric_id = @billable_metric_id
  AND f.deleted_at IS NULL AND v.deleted_at IS NULL
ORDER BY v.billable_metric_filter_id, v.value;

-- name: DiscardProductFilterValues :exec
UPDATE product_filter_values SET deleted_at = @at
WHERE organization_id = @organization_id AND product_filter_id = @product_filter_id AND deleted_at IS NULL;

-- name: InsertProductFilterValue :exec
INSERT INTO product_filter_values (organization_id, product_filter_id, position, billable_metric_filter_id, value)
VALUES (@organization_id, @product_filter_id, @position, @billable_metric_filter_id, @value);

-- name: ListProductFilterValues :many
-- The values of the filters, in their order, with their metric filter's
-- key.
SELECT v.product_filter_id, v.billable_metric_filter_id, v.value, COALESCE(m.key, '')::text AS key
FROM product_filter_values v
LEFT JOIN billable_metric_filters m ON m.organization_id = v.organization_id AND m.id = v.billable_metric_filter_id
WHERE v.organization_id = @organization_id AND v.product_filter_id = ANY(@filter_ids::uuid[]) AND v.deleted_at IS NULL
ORDER BY v.product_filter_id, v.position;

-- name: CreateRateCard :execrows
INSERT INTO rate_cards (
  organization_id, id, product_id, product_filter_id, code, name, description, currency, billing_timing,
  proration, display_on_invoice, regroup_paid_fees, pricing_unit_id, created_at, updated_at
) VALUES (
  @organization_id, @id, @product_id, @product_filter_id, @code, @name, @description, @currency, @billing_timing,
  @proration, @display_on_invoice, @regroup_paid_fees, @pricing_unit_id, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateRateCard :execrows
UPDATE rate_cards SET
  code = @code, name = @name, description = @description, currency = @currency, billing_timing = @billing_timing,
  proration = @proration, display_on_invoice = @display_on_invoice, regroup_paid_fees = @regroup_paid_fees,
  pricing_unit_id = @pricing_unit_id, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DiscardRateCard :execrows
UPDATE rate_cards SET deleted_at = @at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DiscardRatesOfRateCard :exec
UPDATE rate_card_rates SET deleted_at = @at
WHERE organization_id = @organization_id AND rate_card_id = @rate_card_id AND deleted_at IS NULL;

-- name: GetRateCard :one
SELECT c.*,
  ARRAY(SELECT t.tax_id FROM rate_card_taxes t
        WHERE t.organization_id = c.organization_id AND t.rate_card_id = c.id ORDER BY t.position)::uuid[] AS tax_ids
FROM rate_cards c WHERE c.organization_id = @organization_id AND c.id = @id;

-- name: GetRateCardByCode :one
SELECT c.*,
  ARRAY(SELECT t.tax_id FROM rate_card_taxes t
        WHERE t.organization_id = c.organization_id AND t.rate_card_id = c.id ORDER BY t.position)::uuid[] AS tax_ids
FROM rate_cards c WHERE c.organization_id = @organization_id AND c.code = @code AND c.deleted_at IS NULL;

-- name: ListRateCards :many
-- Newest first, then by id; the cursor is (@after_at, @after). The
-- category restriction keeps cards of the products of @category_ids, and
-- of uncategorized products when @without_category.
SELECT c.*,
  ARRAY(SELECT t.tax_id FROM rate_card_taxes t
        WHERE t.organization_id = c.organization_id AND t.rate_card_id = c.id ORDER BY t.position)::uuid[] AS tax_ids
FROM rate_cards c
WHERE c.organization_id = @organization_id AND c.deleted_at IS NULL
  AND (sqlc.narg(search)::text IS NULL OR c.name ILIKE sqlc.narg(search) OR c.code ILIKE sqlc.narg(search))
  AND (cardinality(@product_ids::uuid[]) = 0 OR c.product_id = ANY(@product_ids::uuid[]))
  AND (cardinality(@product_filter_ids::uuid[]) = 0 OR c.product_filter_id = ANY(@product_filter_ids::uuid[]))
  AND ((cardinality(@category_ids::uuid[]) = 0 AND NOT @without_category::boolean) OR EXISTS (
    SELECT 1 FROM products p
    WHERE p.organization_id = c.organization_id AND p.id = c.product_id
      AND (p.product_category_id = ANY(@category_ids::uuid[])
           OR (@without_category::boolean AND p.product_category_id IS NULL))
  ))
  AND (sqlc.narg(after)::uuid IS NULL
       OR c.created_at < sqlc.narg(after_at)::timestamptz
       OR (c.created_at = sqlc.narg(after_at)::timestamptz AND c.id > sqlc.narg(after)))
ORDER BY c.created_at DESC, c.id
LIMIT @lim;

-- name: DeleteRateCardTaxes :exec
DELETE FROM rate_card_taxes WHERE organization_id = @organization_id AND rate_card_id = @rate_card_id;

-- name: InsertRateCardTax :exec
INSERT INTO rate_card_taxes (organization_id, rate_card_id, tax_id, position)
VALUES (@organization_id, @rate_card_id, @tax_id, @position);

-- name: CreateRateCardRate :execrows
INSERT INTO rate_card_rates (
  organization_id, id, rate_card_id, code, effective_from, rate_model, properties, min_amount,
  billing_interval_count, billing_interval_unit, pricing_unit_conversion_rate, created_at, updated_at
) VALUES (
  @organization_id, @id, @rate_card_id, @code, @effective_from, @rate_model, @properties, @min_amount,
  @billing_interval_count, @billing_interval_unit, @pricing_unit_conversion_rate, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateRateCardRate :execrows
UPDATE rate_card_rates SET
  code = @code, effective_from = @effective_from, rate_model = @rate_model, properties = @properties,
  min_amount = @min_amount, billing_interval_count = @billing_interval_count,
  billing_interval_unit = @billing_interval_unit, pricing_unit_conversion_rate = @pricing_unit_conversion_rate,
  updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DiscardRateCardRate :execrows
UPDATE rate_card_rates SET deleted_at = @at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: GetRateCardRate :one
SELECT * FROM rate_card_rates WHERE organization_id = @organization_id AND id = @id;

-- name: ListRateCardRates :many
-- Latest effective first.
SELECT * FROM rate_card_rates
WHERE organization_id = @organization_id AND rate_card_id = @rate_card_id AND deleted_at IS NULL
ORDER BY effective_from DESC, id;

-- name: CreatePlanRateCard :execrows
INSERT INTO plan_rate_cards (organization_id, id, plan_id, rate_card_id, units, created_at, updated_at)
VALUES (@organization_id, @id, @plan_id, @rate_card_id, @units, @created_at, @updated_at)
ON CONFLICT DO NOTHING;

-- name: UpdatePlanRateCard :execrows
UPDATE plan_rate_cards SET units = @units, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DiscardPlanRateCard :execrows
UPDATE plan_rate_cards SET deleted_at = @at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DiscardPlanRateCardsOfPlan :many
UPDATE plan_rate_cards SET deleted_at = @at
WHERE organization_id = @organization_id AND plan_id = @plan_id AND deleted_at IS NULL
RETURNING id;

-- name: GetPlanRateCard :one
SELECT * FROM plan_rate_cards WHERE organization_id = @organization_id AND id = @id;

-- name: GetPlanRateCardByRateCard :one
SELECT * FROM plan_rate_cards
WHERE organization_id = @organization_id AND plan_id = @plan_id AND rate_card_id = @rate_card_id AND deleted_at IS NULL;

-- name: ListPlanRateCards :many
-- Oldest first, then by id; the cursor is (@after_at, @after). Every set
-- restriction holds.
SELECT p.* FROM plan_rate_cards p
JOIN rate_cards c ON c.organization_id = p.organization_id AND c.id = p.rate_card_id
JOIN products pr ON pr.organization_id = c.organization_id AND pr.id = c.product_id
WHERE p.organization_id = @organization_id AND p.deleted_at IS NULL AND c.deleted_at IS NULL AND pr.deleted_at IS NULL
  AND (sqlc.narg(plan_id)::uuid IS NULL OR p.plan_id = sqlc.narg(plan_id))
  AND (sqlc.narg(rate_card_id)::uuid IS NULL OR p.rate_card_id = sqlc.narg(rate_card_id))
  AND (sqlc.narg(product_id)::uuid IS NULL OR c.product_id = sqlc.narg(product_id))
  AND (sqlc.narg(product_filter_id)::uuid IS NULL OR c.product_filter_id = sqlc.narg(product_filter_id))
  AND (sqlc.narg(category_id)::uuid IS NULL OR pr.product_category_id = sqlc.narg(category_id))
  AND (sqlc.narg(after)::uuid IS NULL
       OR p.created_at > sqlc.narg(after_at)::timestamptz
       OR (p.created_at = sqlc.narg(after_at)::timestamptz AND p.id > sqlc.narg(after)))
ORDER BY p.created_at, p.id
LIMIT @lim;

-- name: ListPlanRateCardPlanIDs :many
SELECT DISTINCT p.plan_id FROM plan_rate_cards p
JOIN rate_cards c ON c.organization_id = p.organization_id AND c.id = p.rate_card_id
JOIN products pr ON pr.organization_id = c.organization_id AND pr.id = c.product_id
WHERE p.organization_id = @organization_id AND p.deleted_at IS NULL AND c.deleted_at IS NULL AND pr.deleted_at IS NULL
  AND (sqlc.narg(plan_id)::uuid IS NULL OR p.plan_id = sqlc.narg(plan_id))
  AND (sqlc.narg(rate_card_id)::uuid IS NULL OR p.rate_card_id = sqlc.narg(rate_card_id))
  AND (sqlc.narg(product_id)::uuid IS NULL OR c.product_id = sqlc.narg(product_id))
  AND (sqlc.narg(product_filter_id)::uuid IS NULL OR c.product_filter_id = sqlc.narg(product_filter_id))
  AND (sqlc.narg(category_id)::uuid IS NULL OR pr.product_category_id = sqlc.narg(category_id))
ORDER BY p.plan_id;

-- name: DiscardRatePhases :exec
-- Discards the kept phases of the plan rate card not in @keep.
UPDATE rate_phases SET deleted_at = @at
WHERE organization_id = @organization_id AND plan_rate_card_id = @plan_rate_card_id AND deleted_at IS NULL
  AND NOT (id = ANY(@keep::uuid[]));

-- name: ParkRatePhases :exec
-- Moves the kept phases @ids past every position, so that rewriting them
-- one by one never meets the unique position index.
UPDATE rate_phases SET position = position + 1000000
WHERE organization_id = @organization_id AND plan_rate_card_id = @plan_rate_card_id AND deleted_at IS NULL
  AND id = ANY(@ids::uuid[]);

-- name: UpdateRatePhase :execrows
UPDATE rate_phases SET
  code = @code, position = @position, name = @name, billing_interval_cycle_count = @billing_interval_cycle_count,
  override_rate_model = @override_rate_model, override_properties = @override_properties,
  override_min_amount = @override_min_amount, override_billing_interval_count = @override_billing_interval_count,
  override_billing_interval_unit = @override_billing_interval_unit,
  override_pricing_unit_conversion_rate = @override_pricing_unit_conversion_rate, updated_at = @updated_at
WHERE organization_id = @organization_id AND plan_rate_card_id = @plan_rate_card_id AND id = @id AND deleted_at IS NULL;

-- name: InsertRatePhase :execrows
INSERT INTO rate_phases (
  organization_id, id, plan_rate_card_id, code, position, name, billing_interval_cycle_count,
  override_rate_model, override_properties, override_min_amount, override_billing_interval_count,
  override_billing_interval_unit, override_pricing_unit_conversion_rate, created_at, updated_at
) VALUES (
  @organization_id, @id, @plan_rate_card_id, @code, @position, @name, @billing_interval_cycle_count,
  @override_rate_model, @override_properties, @override_min_amount, @override_billing_interval_count,
  @override_billing_interval_unit, @override_pricing_unit_conversion_rate, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: ListRatePhases :many
SELECT * FROM rate_phases
WHERE organization_id = @organization_id AND plan_rate_card_id = @plan_rate_card_id AND deleted_at IS NULL
ORDER BY position;
