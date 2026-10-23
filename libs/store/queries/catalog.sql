-- Catalog: pricing units, billable metrics, plans, add-ons, charges and fixed
-- charges, with their filters and tax lists.

-- Pricing units.

-- name: CreatePricingUnit :execrows
INSERT INTO pricing_units (
  organization_id, id, code, name, short_name, description, created_at, updated_at
) VALUES (
  @organization_id, @id, @code, @name, @short_name, @description, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdatePricingUnit :execrows
UPDATE pricing_units SET
  organization_id = @organization_id, id = @id, code = @code, name = @name, short_name = @short_name,
  description = @description, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetPricingUnit :one
SELECT * FROM pricing_units WHERE organization_id = @organization_id AND id = @id;

-- name: GetPricingUnitByCode :one
SELECT * FROM pricing_units WHERE organization_id = @organization_id AND code = @code;

-- name: ListPricingUnits :many
-- @by_name lists by name (byte order), then by created_at, newest first,
-- then by id (pricing_units_query.rb:8-11); its cursor is (@after_name,
-- @after_at, @after).
SELECT * FROM pricing_units
WHERE organization_id = @organization_id
  AND (sqlc.narg(search)::text IS NULL OR name ILIKE sqlc.narg(search) OR code ILIKE sqlc.narg(search))
  AND (sqlc.narg(after)::uuid IS NULL
       OR (@by_name::boolean AND (name COLLATE "C" > sqlc.narg(after_name)::text COLLATE "C"
                                  OR (name = sqlc.narg(after_name)::text
                                      AND (created_at < sqlc.narg(after_at)::timestamptz
                                           OR (created_at = sqlc.narg(after_at)::timestamptz AND id > sqlc.narg(after))))))
       OR (NOT @by_name::boolean AND id > sqlc.narg(after)))
ORDER BY CASE WHEN @by_name::boolean THEN name COLLATE "C" END,
  CASE WHEN @by_name::boolean THEN created_at END DESC, id
LIMIT @lim;

-- Billable metrics.

-- name: CreateBillableMetric :execrows
INSERT INTO billable_metrics (
  organization_id, id, code, name, description, aggregation_type, field_name, weighted_interval,
  recurring, expression, rounding_function, rounding_precision, created_at, updated_at
) VALUES (
  @organization_id, @id, @code, @name, @description, @aggregation_type, @field_name, @weighted_interval,
  @recurring, @expression, @rounding_function, @rounding_precision, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateBillableMetric :execrows
UPDATE billable_metrics SET
  organization_id = @organization_id, id = @id, code = @code, name = @name, description = @description,
  aggregation_type = @aggregation_type, field_name = @field_name, weighted_interval = @weighted_interval,
  recurring = @recurring, expression = @expression, rounding_function = @rounding_function,
  rounding_precision = @rounding_precision, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DiscardBillableMetric :execrows
-- Soft-deletes a kept metric (lago metric.discard!, destroy_service.rb:33).
UPDATE billable_metrics SET deleted_at = @deleted_at, updated_at = @deleted_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: GetBillableMetric :one
SELECT * FROM billable_metrics WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: GetBillableMetricWithDiscarded :one
SELECT * FROM billable_metrics WHERE organization_id = @organization_id AND id = @id;

-- name: GetBillableMetricByCode :one
SELECT * FROM billable_metrics WHERE organization_id = @organization_id AND code = @code AND deleted_at IS NULL;

-- name: ListBillableMetricsByIDs :many
SELECT * FROM billable_metrics
WHERE organization_id = @organization_id AND id = ANY(@ids::uuid[]) AND deleted_at IS NULL
ORDER BY id;

-- name: ListBillableMetricsByCodes :many
SELECT * FROM billable_metrics
WHERE organization_id = @organization_id AND code = ANY(@codes::text[]) AND deleted_at IS NULL
ORDER BY id;

-- name: BillableMetricInUse :one
SELECT EXISTS (
  SELECT 1 FROM charges c
  WHERE c.organization_id = @organization_id AND c.billable_metric_id = @billable_metric_id AND c.deleted_at IS NULL
) AS in_use;

-- name: ListBillableMetrics :many
-- @recent lists by created_at, newest first, then by id ascending
-- (base_query.rb:111-112); its cursor is (@after_at, @after).
SELECT * FROM billable_metrics m
WHERE m.organization_id = @organization_id AND m.deleted_at IS NULL
  AND (cardinality(@aggregation_types::text[]) = 0 OR m.aggregation_type = ANY(@aggregation_types::text[]))
  AND (sqlc.narg(recurring)::boolean IS NULL OR m.recurring = sqlc.narg(recurring))
  AND (sqlc.narg(plan_id)::uuid IS NULL OR EXISTS (
    SELECT 1 FROM charges c
    WHERE c.organization_id = m.organization_id AND c.plan_id = sqlc.narg(plan_id) AND c.billable_metric_id = m.id
      AND c.deleted_at IS NULL
  ))
  AND (sqlc.narg(search)::text IS NULL OR m.name ILIKE sqlc.narg(search) OR m.code ILIKE sqlc.narg(search))
  AND (sqlc.narg(after)::uuid IS NULL
       OR (@recent::boolean AND (m.created_at < sqlc.narg(after_at)::timestamptz
                                 OR (m.created_at = sqlc.narg(after_at)::timestamptz AND m.id > sqlc.narg(after))))
       OR (NOT @recent::boolean AND m.id > sqlc.narg(after)))
ORDER BY CASE WHEN @recent::boolean THEN m.created_at END DESC, m.id
LIMIT @lim;

-- name: ListBillableMetricFilters :many
SELECT * FROM billable_metric_filters
WHERE organization_id = @organization_id AND billable_metric_id = ANY(@billable_metric_ids::uuid[]) AND deleted_at IS NULL
ORDER BY billable_metric_id, position;

-- name: DiscardBillableMetricFilters :execrows
UPDATE billable_metric_filters SET deleted_at = @deleted_at
WHERE organization_id = @organization_id AND billable_metric_id = @billable_metric_id AND deleted_at IS NULL
  AND NOT (id = ANY(@kept_ids::uuid[]));

-- name: UpsertBillableMetricFilter :exec
INSERT INTO billable_metric_filters (organization_id, id, billable_metric_id, key, filter_values, position)
VALUES (@organization_id, @id, @billable_metric_id, @key, @filter_values, @position)
ON CONFLICT (organization_id, id) DO UPDATE SET key = EXCLUDED.key, filter_values = EXCLUDED.filter_values, position = EXCLUDED.position;

-- Plans.

-- name: CreatePlan :execrows
INSERT INTO plans (
  organization_id, id, code, name, invoice_display_name, description, billing_interval, amount, currency,
  pay_in_advance, trial_period_days, bill_charges_monthly, bill_fixed_charges_monthly, parent_id,
  metadata, status, created_at, updated_at
) VALUES (
  @organization_id, @id, @code, @name, @invoice_display_name, @description, @billing_interval, @amount, @currency,
  @pay_in_advance, @trial_period_days, @bill_charges_monthly, @bill_fixed_charges_monthly, @parent_id,
  @metadata, @status, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdatePlan :execrows
UPDATE plans SET
  organization_id = @organization_id, id = @id, code = @code, name = @name, invoice_display_name = @invoice_display_name,
  description = @description, billing_interval = @billing_interval, amount = @amount, currency = @currency,
  pay_in_advance = @pay_in_advance, trial_period_days = @trial_period_days, bill_charges_monthly = @bill_charges_monthly,
  bill_fixed_charges_monthly = @bill_fixed_charges_monthly, parent_id = @parent_id,
  metadata = @metadata, status = @status, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetPlan :one
SELECT sqlc.embed(p),
  ARRAY(SELECT t.tax_id FROM plan_taxes t
        WHERE t.organization_id = p.organization_id AND t.plan_id = p.id ORDER BY t.position)::uuid[] AS tax_ids
FROM plans p
WHERE p.organization_id = @organization_id AND p.id = @id;

-- name: GetPlanByCode :one
SELECT sqlc.embed(p),
  ARRAY(SELECT t.tax_id FROM plan_taxes t
        WHERE t.organization_id = p.organization_id AND t.plan_id = p.id ORDER BY t.position)::uuid[] AS tax_ids
FROM plans p
WHERE p.organization_id = @organization_id AND p.code = @code AND p.parent_id IS NULL
  AND p.status <> 'deleted';

-- name: ListPlansByIDs :many
SELECT sqlc.embed(p),
  ARRAY(SELECT t.tax_id FROM plan_taxes t
        WHERE t.organization_id = p.organization_id AND t.plan_id = p.id ORDER BY t.position)::uuid[] AS tax_ids
FROM plans p
WHERE p.organization_id = @organization_id AND p.id = ANY(@ids::uuid[])
ORDER BY p.id;

-- name: ListPlanChildren :many
SELECT sqlc.embed(p),
  ARRAY(SELECT t.tax_id FROM plan_taxes t
        WHERE t.organization_id = p.organization_id AND t.plan_id = p.id ORDER BY t.position)::uuid[] AS tax_ids
FROM plans p
WHERE p.organization_id = @organization_id AND p.parent_id = @parent_id
  AND p.status <> 'deleted'
ORDER BY p.id;

-- name: ListPlans :many
-- @by_name lists by name (byte order), then by created_at, newest first,
-- then by id (plans_query.rb:8-12); its cursor is (@after_name, @after_at,
-- @after).
SELECT sqlc.embed(p),
  ARRAY(SELECT t.tax_id FROM plan_taxes t
        WHERE t.organization_id = p.organization_id AND t.plan_id = p.id ORDER BY t.position)::uuid[] AS tax_ids
FROM plans p
WHERE p.organization_id = @organization_id
  AND (@include_children::boolean OR p.parent_id IS NULL)
  AND (CASE WHEN cardinality(@statuses::text[]) = 0 THEN p.status <> 'deleted'
       ELSE p.status = ANY(@statuses::text[]) END)
  AND (sqlc.narg(search)::text IS NULL OR p.name ILIKE sqlc.narg(search) OR p.code ILIKE sqlc.narg(search))
  AND (sqlc.narg(after)::uuid IS NULL
       OR (@by_name::boolean AND (p.name COLLATE "C" > sqlc.narg(after_name)::text COLLATE "C"
                                  OR (p.name = sqlc.narg(after_name)::text
                                      AND (p.created_at < sqlc.narg(after_at)::timestamptz
                                           OR (p.created_at = sqlc.narg(after_at)::timestamptz AND p.id > sqlc.narg(after))))))
       OR (NOT @by_name::boolean AND p.id > sqlc.narg(after)))
ORDER BY CASE WHEN @by_name::boolean THEN p.name COLLATE "C" END,
  CASE WHEN @by_name::boolean THEN p.created_at END DESC, p.id
LIMIT @lim;

-- name: DeletePlanTaxes :exec
DELETE FROM plan_taxes WHERE organization_id = @organization_id AND plan_id = @plan_id;

-- name: InsertPlanTaxes :exec
INSERT INTO plan_taxes (organization_id, plan_id, tax_id, position)
SELECT @organization_id, @plan_id, t.id, t.ord
FROM unnest(@tax_ids::uuid[]) WITH ORDINALITY AS t(id, ord);

-- Add-ons.

-- name: CreateAddOn :execrows
INSERT INTO add_ons (
  organization_id, id, code, name, invoice_display_name, description, amount, currency, created_at, updated_at
) VALUES (
  @organization_id, @id, @code, @name, @invoice_display_name, @description, @amount, @currency, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateAddOn :execrows
UPDATE add_ons SET
  organization_id = @organization_id, id = @id, code = @code, name = @name, invoice_display_name = @invoice_display_name,
  description = @description, amount = @amount, currency = @currency, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DiscardAddOn :execrows
UPDATE add_ons SET deleted_at = @deleted_at, updated_at = @deleted_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: GetAddOn :one
-- Kept or discarded: fees reference discarded rows.
SELECT sqlc.embed(a),
  ARRAY(SELECT t.tax_id FROM add_on_taxes t
        WHERE t.organization_id = a.organization_id AND t.add_on_id = a.id ORDER BY t.position)::uuid[] AS tax_ids
FROM add_ons a
WHERE a.organization_id = @organization_id AND a.id = @id;

-- name: GetAddOnByCode :one
SELECT sqlc.embed(a),
  ARRAY(SELECT t.tax_id FROM add_on_taxes t
        WHERE t.organization_id = a.organization_id AND t.add_on_id = a.id ORDER BY t.position)::uuid[] AS tax_ids
FROM add_ons a
WHERE a.organization_id = @organization_id AND a.code = @code AND a.deleted_at IS NULL;

-- name: ListAddOnsByIDs :many
SELECT sqlc.embed(a),
  ARRAY(SELECT t.tax_id FROM add_on_taxes t
        WHERE t.organization_id = a.organization_id AND t.add_on_id = a.id ORDER BY t.position)::uuid[] AS tax_ids
FROM add_ons a
WHERE a.organization_id = @organization_id AND a.id = ANY(@ids::uuid[]) AND a.deleted_at IS NULL
ORDER BY a.id;

-- name: ListAddOns :many
-- @recent lists by created_at, newest first, then by id ascending
-- (add_ons_query.rb, base_query.rb:111-112); its cursor is (@after_at,
-- @after).
SELECT sqlc.embed(a),
  ARRAY(SELECT t.tax_id FROM add_on_taxes t
        WHERE t.organization_id = a.organization_id AND t.add_on_id = a.id ORDER BY t.position)::uuid[] AS tax_ids
FROM add_ons a
WHERE a.organization_id = @organization_id AND a.deleted_at IS NULL
  AND (sqlc.narg(search)::text IS NULL OR a.name ILIKE sqlc.narg(search) OR a.code ILIKE sqlc.narg(search))
  AND (sqlc.narg(after)::uuid IS NULL
       OR (@recent::boolean AND (a.created_at < sqlc.narg(after_at)::timestamptz
                                 OR (a.created_at = sqlc.narg(after_at)::timestamptz AND a.id > sqlc.narg(after))))
       OR (NOT @recent::boolean AND a.id > sqlc.narg(after)))
ORDER BY CASE WHEN @recent::boolean THEN a.created_at END DESC, a.id
LIMIT @lim;

-- name: DeleteAddOnTaxes :exec
DELETE FROM add_on_taxes WHERE organization_id = @organization_id AND add_on_id = @add_on_id;

-- name: InsertAddOnTaxes :exec
INSERT INTO add_on_taxes (organization_id, add_on_id, tax_id, position)
SELECT @organization_id, @add_on_id, t.id, t.ord
FROM unnest(@tax_ids::uuid[]) WITH ORDINALITY AS t(id, ord);

-- Charges.

-- name: CreateCharge :execrows
INSERT INTO charges (
  organization_id, id, plan_id, parent_id, code, billable_metric_id, charge_model, properties,
  pay_in_advance, invoiceable, regroup_paid_fees, prorated, min_amount, invoice_display_name,
  applied_pricing_unit_id, conversion_rate, created_at, updated_at
) VALUES (
  @organization_id, @id, @plan_id, @parent_id, @code, @billable_metric_id, @charge_model, @properties,
  @pay_in_advance, @invoiceable, @regroup_paid_fees, @prorated, @min_amount, @invoice_display_name,
  @applied_pricing_unit_id, @conversion_rate, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateCharge :execrows
UPDATE charges SET
  organization_id = @organization_id, id = @id, plan_id = @plan_id, parent_id = @parent_id, code = @code,
  billable_metric_id = @billable_metric_id, charge_model = @charge_model, properties = @properties,
  pay_in_advance = @pay_in_advance, invoiceable = @invoiceable, regroup_paid_fees = @regroup_paid_fees,
  prorated = @prorated, min_amount = @min_amount, invoice_display_name = @invoice_display_name,
  applied_pricing_unit_id = @applied_pricing_unit_id, conversion_rate = @conversion_rate,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DiscardChargeFiltersOfCharges :exec
-- Discards the kept filters of the kept charges selected as in
-- DiscardCharges; run it first.
UPDATE charge_filters f SET deleted_at = @deleted_at
FROM charges c
WHERE f.organization_id = @organization_id AND f.deleted_at IS NULL
  AND c.organization_id = f.organization_id AND c.id = f.charge_id AND c.deleted_at IS NULL
  AND (sqlc.narg(id)::uuid IS NULL OR c.id = sqlc.narg(id))
  AND (sqlc.narg(plan_id)::uuid IS NULL OR c.plan_id = sqlc.narg(plan_id))
  AND (sqlc.narg(billable_metric_id)::uuid IS NULL OR c.billable_metric_id = sqlc.narg(billable_metric_id));

-- name: DiscardCharges :execrows
-- Discards the kept charges with the ID @id, of the plan @plan_id or of
-- the metric @billable_metric_id (one is set); @touch also sets
-- updated_at, as a single discard does (lago discard!, while update_all
-- leaves it).
UPDATE charges SET deleted_at = @deleted_at,
  updated_at = CASE WHEN @touch::boolean THEN @deleted_at ELSE updated_at END
WHERE organization_id = @organization_id AND deleted_at IS NULL
  AND (sqlc.narg(id)::uuid IS NULL OR id = sqlc.narg(id))
  AND (sqlc.narg(plan_id)::uuid IS NULL OR plan_id = sqlc.narg(plan_id))
  AND (sqlc.narg(billable_metric_id)::uuid IS NULL OR billable_metric_id = sqlc.narg(billable_metric_id));

-- name: GetCharge :one
-- Kept or discarded: fees reference discarded rows.
SELECT sqlc.embed(c),
  ARRAY(SELECT t.tax_id FROM charge_taxes t
        WHERE t.organization_id = c.organization_id AND t.charge_id = c.id ORDER BY t.position)::uuid[] AS tax_ids
FROM charges c
WHERE c.organization_id = @organization_id AND c.id = @id;

-- name: GetChargeByCode :one
SELECT sqlc.embed(c),
  ARRAY(SELECT t.tax_id FROM charge_taxes t
        WHERE t.organization_id = c.organization_id AND t.charge_id = c.id ORDER BY t.position)::uuid[] AS tax_ids
FROM charges c
WHERE c.organization_id = @organization_id AND c.plan_id = @plan_id AND c.code = @code AND c.deleted_at IS NULL
ORDER BY c.id
LIMIT 1;

-- name: ListChargesByPlan :many
SELECT sqlc.embed(c),
  ARRAY(SELECT t.tax_id FROM charge_taxes t
        WHERE t.organization_id = c.organization_id AND t.charge_id = c.id ORDER BY t.position)::uuid[] AS tax_ids
FROM charges c
WHERE c.organization_id = @organization_id AND c.plan_id = @plan_id AND c.deleted_at IS NULL
ORDER BY c.id;

-- name: ListChargesByMetric :many
SELECT sqlc.embed(c),
  ARRAY(SELECT t.tax_id FROM charge_taxes t
        WHERE t.organization_id = c.organization_id AND t.charge_id = c.id ORDER BY t.position)::uuid[] AS tax_ids
FROM charges c
WHERE c.organization_id = @organization_id AND c.billable_metric_id = @billable_metric_id AND c.deleted_at IS NULL
ORDER BY c.id;

-- name: ListChargeChildren :many
SELECT sqlc.embed(c),
  ARRAY(SELECT t.tax_id FROM charge_taxes t
        WHERE t.organization_id = c.organization_id AND t.charge_id = c.id ORDER BY t.position)::uuid[] AS tax_ids
FROM charges c
WHERE c.organization_id = @organization_id AND c.parent_id = @parent_id AND c.deleted_at IS NULL
ORDER BY c.id;

-- name: DeleteChargeTaxes :exec
DELETE FROM charge_taxes WHERE organization_id = @organization_id AND charge_id = @charge_id;

-- name: InsertChargeTaxes :exec
INSERT INTO charge_taxes (organization_id, charge_id, tax_id, position)
SELECT @organization_id, @charge_id, t.id, t.ord
FROM unnest(@tax_ids::uuid[]) WITH ORDINALITY AS t(id, ord);

-- name: ListChargeFiltersByCharges :many
SELECT * FROM charge_filters
WHERE organization_id = @organization_id AND charge_id = ANY(@charge_ids::uuid[]) AND deleted_at IS NULL
ORDER BY charge_id, position;

-- name: ListChargeFilters :many
SELECT * FROM charge_filters
WHERE organization_id = @organization_id AND charge_id = @charge_id AND deleted_at IS NULL
ORDER BY created_at, id;

-- name: GetChargeFilter :one
-- Kept or discarded: fees reference discarded filters.
SELECT * FROM charge_filters WHERE organization_id = @organization_id AND id = @id;

-- name: DiscardChargeFiltersExcept :exec
-- Discards the kept filters of the charge missing from @keep_ids (lago
-- ChargeFilters::CreateOrUpdateBatchService).
UPDATE charge_filters SET deleted_at = @deleted_at
WHERE organization_id = @organization_id AND charge_id = @charge_id AND deleted_at IS NULL
  AND NOT (id = ANY(@keep_ids::uuid[]));

-- name: UpsertChargeFilter :exec
INSERT INTO charge_filters (
  organization_id, id, charge_id, filter_values, properties, invoice_display_name, code, position, created_at
) VALUES (
  @organization_id, @id, @charge_id, @filter_values, @properties, @invoice_display_name, @code, @position, @created_at
)
ON CONFLICT (organization_id, id) DO UPDATE SET
  filter_values = EXCLUDED.filter_values, properties = EXCLUDED.properties,
  invoice_display_name = EXCLUDED.invoice_display_name, code = EXCLUDED.code, position = EXCLUDED.position
WHERE charge_filters.charge_id = EXCLUDED.charge_id AND charge_filters.deleted_at IS NULL;

-- Fixed charges.

-- name: CreateFixedCharge :execrows
INSERT INTO fixed_charges (
  organization_id, id, plan_id, parent_id, code, add_on_id, charge_model, properties, units,
  pay_in_advance, prorated, invoice_display_name, created_at, updated_at
) VALUES (
  @organization_id, @id, @plan_id, @parent_id, @code, @add_on_id, @charge_model, @properties, @units,
  @pay_in_advance, @prorated, @invoice_display_name, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateFixedCharge :execrows
UPDATE fixed_charges SET
  organization_id = @organization_id, id = @id, plan_id = @plan_id, parent_id = @parent_id, code = @code,
  add_on_id = @add_on_id, charge_model = @charge_model, properties = @properties, units = @units,
  pay_in_advance = @pay_in_advance, prorated = @prorated, invoice_display_name = @invoice_display_name,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DiscardFixedCharges :execrows
-- Discards the kept fixed charges with the ID @id, of the plan @plan_id or
-- of the add-on @add_on_id (one is set); @touch also sets updated_at, as a
-- single discard does.
UPDATE fixed_charges SET deleted_at = @deleted_at,
  updated_at = CASE WHEN @touch::boolean THEN @deleted_at ELSE updated_at END
WHERE organization_id = @organization_id AND deleted_at IS NULL
  AND (sqlc.narg(id)::uuid IS NULL OR id = sqlc.narg(id))
  AND (sqlc.narg(plan_id)::uuid IS NULL OR plan_id = sqlc.narg(plan_id))
  AND (sqlc.narg(add_on_id)::uuid IS NULL OR add_on_id = sqlc.narg(add_on_id));

-- name: GetFixedCharge :one
-- Kept or discarded: fees reference discarded rows.
SELECT sqlc.embed(f),
  ARRAY(SELECT t.tax_id FROM fixed_charge_taxes t
        WHERE t.organization_id = f.organization_id AND t.fixed_charge_id = f.id ORDER BY t.position)::uuid[] AS tax_ids
FROM fixed_charges f
WHERE f.organization_id = @organization_id AND f.id = @id;

-- name: GetFixedChargeByCode :one
SELECT sqlc.embed(f),
  ARRAY(SELECT t.tax_id FROM fixed_charge_taxes t
        WHERE t.organization_id = f.organization_id AND t.fixed_charge_id = f.id ORDER BY t.position)::uuid[] AS tax_ids
FROM fixed_charges f
WHERE f.organization_id = @organization_id AND f.plan_id = @plan_id AND f.code = @code AND f.deleted_at IS NULL
ORDER BY f.id
LIMIT 1;

-- name: ListFixedChargesByPlan :many
SELECT sqlc.embed(f),
  ARRAY(SELECT t.tax_id FROM fixed_charge_taxes t
        WHERE t.organization_id = f.organization_id AND t.fixed_charge_id = f.id ORDER BY t.position)::uuid[] AS tax_ids
FROM fixed_charges f
WHERE f.organization_id = @organization_id AND f.plan_id = @plan_id AND f.deleted_at IS NULL
ORDER BY f.id;

-- name: ListFixedChargesByAddOn :many
SELECT sqlc.embed(f),
  ARRAY(SELECT t.tax_id FROM fixed_charge_taxes t
        WHERE t.organization_id = f.organization_id AND t.fixed_charge_id = f.id ORDER BY t.position)::uuid[] AS tax_ids
FROM fixed_charges f
WHERE f.organization_id = @organization_id AND f.add_on_id = @add_on_id AND f.deleted_at IS NULL
ORDER BY f.id;

-- name: ListFixedChargeChildren :many
SELECT sqlc.embed(f),
  ARRAY(SELECT t.tax_id FROM fixed_charge_taxes t
        WHERE t.organization_id = f.organization_id AND t.fixed_charge_id = f.id ORDER BY t.position)::uuid[] AS tax_ids
FROM fixed_charges f
WHERE f.organization_id = @organization_id AND f.parent_id = @parent_id AND f.deleted_at IS NULL
ORDER BY f.id;

-- name: DeleteFixedChargeTaxes :exec
DELETE FROM fixed_charge_taxes WHERE organization_id = @organization_id AND fixed_charge_id = @fixed_charge_id;

-- name: InsertFixedChargeTaxes :exec
INSERT INTO fixed_charge_taxes (organization_id, fixed_charge_id, tax_id, position)
SELECT @organization_id, @fixed_charge_id, t.id, t.ord
FROM unnest(@tax_ids::uuid[]) WITH ORDINALITY AS t(id, ord);
