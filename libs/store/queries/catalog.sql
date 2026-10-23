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
SELECT * FROM pricing_units
WHERE organization_id = @organization_id
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
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
WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteBillableMetric :execrows
DELETE FROM billable_metrics WHERE organization_id = @organization_id AND id = @id;

-- name: GetBillableMetric :one
SELECT * FROM billable_metrics WHERE organization_id = @organization_id AND id = @id;

-- name: GetBillableMetricByCode :one
SELECT * FROM billable_metrics WHERE organization_id = @organization_id AND code = @code;

-- name: ListBillableMetricsByIDs :many
SELECT * FROM billable_metrics
WHERE organization_id = @organization_id AND id = ANY(@ids::uuid[])
ORDER BY id;

-- name: ListBillableMetricsByCodes :many
SELECT * FROM billable_metrics
WHERE organization_id = @organization_id AND code = ANY(@codes::text[])
ORDER BY id;

-- name: BillableMetricInUse :one
SELECT EXISTS (
  SELECT 1 FROM charges c
  WHERE c.organization_id = @organization_id AND c.billable_metric_id = @billable_metric_id
) AS in_use;

-- name: ListBillableMetrics :many
SELECT * FROM billable_metrics m
WHERE m.organization_id = @organization_id
  AND (cardinality(@aggregation_types::text[]) = 0 OR m.aggregation_type = ANY(@aggregation_types::text[]))
  AND (sqlc.narg(recurring)::boolean IS NULL OR m.recurring = sqlc.narg(recurring))
  AND (sqlc.narg(plan_id)::uuid IS NULL OR EXISTS (
    SELECT 1 FROM charges c
    WHERE c.organization_id = m.organization_id AND c.plan_id = sqlc.narg(plan_id) AND c.billable_metric_id = m.id
  ))
  AND (sqlc.narg(after)::uuid IS NULL OR m.id > sqlc.narg(after))
ORDER BY m.id
LIMIT @lim;

-- name: ListBillableMetricFilters :many
SELECT * FROM billable_metric_filters
WHERE organization_id = @organization_id AND billable_metric_id = ANY(@billable_metric_ids::uuid[])
ORDER BY billable_metric_id, position;

-- name: DeleteBillableMetricFilters :exec
DELETE FROM billable_metric_filters WHERE organization_id = @organization_id AND billable_metric_id = @billable_metric_id;

-- name: InsertBillableMetricFilter :exec
INSERT INTO billable_metric_filters (organization_id, id, billable_metric_id, key, filter_values, position)
VALUES (@organization_id, @id, @billable_metric_id, @key, @filter_values, @position);

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

-- name: DeletePlan :execrows
DELETE FROM plans WHERE organization_id = @organization_id AND id = @id;

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
WHERE p.organization_id = @organization_id AND p.code = @code AND p.parent_id IS NULL;

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
ORDER BY p.id;

-- name: ListPlans :many
SELECT sqlc.embed(p),
  ARRAY(SELECT t.tax_id FROM plan_taxes t
        WHERE t.organization_id = p.organization_id AND t.plan_id = p.id ORDER BY t.position)::uuid[] AS tax_ids
FROM plans p
WHERE p.organization_id = @organization_id
  AND (@include_children::boolean OR p.parent_id IS NULL)
  AND (cardinality(@statuses::text[]) = 0 OR p.status = ANY(@statuses::text[]))
  AND (sqlc.narg(after)::uuid IS NULL OR p.id > sqlc.narg(after))
ORDER BY p.id
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
WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteAddOn :execrows
DELETE FROM add_ons WHERE organization_id = @organization_id AND id = @id;

-- name: GetAddOn :one
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
WHERE a.organization_id = @organization_id AND a.code = @code;

-- name: ListAddOnsByIDs :many
SELECT sqlc.embed(a),
  ARRAY(SELECT t.tax_id FROM add_on_taxes t
        WHERE t.organization_id = a.organization_id AND t.add_on_id = a.id ORDER BY t.position)::uuid[] AS tax_ids
FROM add_ons a
WHERE a.organization_id = @organization_id AND a.id = ANY(@ids::uuid[])
ORDER BY a.id;

-- name: ListAddOns :many
SELECT sqlc.embed(a),
  ARRAY(SELECT t.tax_id FROM add_on_taxes t
        WHERE t.organization_id = a.organization_id AND t.add_on_id = a.id ORDER BY t.position)::uuid[] AS tax_ids
FROM add_ons a
WHERE a.organization_id = @organization_id
  AND (sqlc.narg(after)::uuid IS NULL OR a.id > sqlc.narg(after))
ORDER BY a.id
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
WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteCharge :execrows
DELETE FROM charges WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteChargesByPlan :execrows
DELETE FROM charges WHERE organization_id = @organization_id AND plan_id = @plan_id;

-- name: DeleteChargesByMetric :execrows
DELETE FROM charges WHERE organization_id = @organization_id AND billable_metric_id = @billable_metric_id;

-- name: GetCharge :one
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
WHERE c.organization_id = @organization_id AND c.plan_id = @plan_id AND c.code = @code
ORDER BY c.id
LIMIT 1;

-- name: ListChargesByPlan :many
SELECT sqlc.embed(c),
  ARRAY(SELECT t.tax_id FROM charge_taxes t
        WHERE t.organization_id = c.organization_id AND t.charge_id = c.id ORDER BY t.position)::uuid[] AS tax_ids
FROM charges c
WHERE c.organization_id = @organization_id AND c.plan_id = @plan_id
ORDER BY c.id;

-- name: ListChargesByMetric :many
SELECT sqlc.embed(c),
  ARRAY(SELECT t.tax_id FROM charge_taxes t
        WHERE t.organization_id = c.organization_id AND t.charge_id = c.id ORDER BY t.position)::uuid[] AS tax_ids
FROM charges c
WHERE c.organization_id = @organization_id AND c.billable_metric_id = @billable_metric_id
ORDER BY c.id;

-- name: ListChargeChildren :many
SELECT sqlc.embed(c),
  ARRAY(SELECT t.tax_id FROM charge_taxes t
        WHERE t.organization_id = c.organization_id AND t.charge_id = c.id ORDER BY t.position)::uuid[] AS tax_ids
FROM charges c
WHERE c.organization_id = @organization_id AND c.parent_id = @parent_id
ORDER BY c.id;

-- name: DeleteChargeTaxes :exec
DELETE FROM charge_taxes WHERE organization_id = @organization_id AND charge_id = @charge_id;

-- name: InsertChargeTaxes :exec
INSERT INTO charge_taxes (organization_id, charge_id, tax_id, position)
SELECT @organization_id, @charge_id, t.id, t.ord
FROM unnest(@tax_ids::uuid[]) WITH ORDINALITY AS t(id, ord);

-- name: ListChargeFiltersByCharges :many
SELECT * FROM charge_filters
WHERE organization_id = @organization_id AND charge_id = ANY(@charge_ids::uuid[])
ORDER BY charge_id, position;

-- name: ListChargeFilters :many
SELECT * FROM charge_filters
WHERE organization_id = @organization_id AND charge_id = @charge_id
ORDER BY created_at, id;

-- name: GetChargeFilter :one
SELECT * FROM charge_filters WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteChargeFilters :exec
DELETE FROM charge_filters WHERE organization_id = @organization_id AND charge_id = @charge_id;

-- name: InsertChargeFilter :exec
INSERT INTO charge_filters (
  organization_id, id, charge_id, filter_values, properties, invoice_display_name, code, position, created_at
) VALUES (
  @organization_id, @id, @charge_id, @filter_values, @properties, @invoice_display_name, @code, @position, @created_at
);

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
WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteFixedCharge :execrows
DELETE FROM fixed_charges WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteFixedChargesByPlan :execrows
DELETE FROM fixed_charges WHERE organization_id = @organization_id AND plan_id = @plan_id;

-- name: DeleteFixedChargesByAddOn :execrows
DELETE FROM fixed_charges WHERE organization_id = @organization_id AND add_on_id = @add_on_id;

-- name: GetFixedCharge :one
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
WHERE f.organization_id = @organization_id AND f.plan_id = @plan_id AND f.code = @code
ORDER BY f.id
LIMIT 1;

-- name: ListFixedChargesByPlan :many
SELECT sqlc.embed(f),
  ARRAY(SELECT t.tax_id FROM fixed_charge_taxes t
        WHERE t.organization_id = f.organization_id AND t.fixed_charge_id = f.id ORDER BY t.position)::uuid[] AS tax_ids
FROM fixed_charges f
WHERE f.organization_id = @organization_id AND f.plan_id = @plan_id
ORDER BY f.id;

-- name: ListFixedChargesByAddOn :many
SELECT sqlc.embed(f),
  ARRAY(SELECT t.tax_id FROM fixed_charge_taxes t
        WHERE t.organization_id = f.organization_id AND t.fixed_charge_id = f.id ORDER BY t.position)::uuid[] AS tax_ids
FROM fixed_charges f
WHERE f.organization_id = @organization_id AND f.add_on_id = @add_on_id
ORDER BY f.id;

-- name: ListFixedChargeChildren :many
SELECT sqlc.embed(f),
  ARRAY(SELECT t.tax_id FROM fixed_charge_taxes t
        WHERE t.organization_id = f.organization_id AND t.fixed_charge_id = f.id ORDER BY t.position)::uuid[] AS tax_ids
FROM fixed_charges f
WHERE f.organization_id = @organization_id AND f.parent_id = @parent_id
ORDER BY f.id;

-- name: DeleteFixedChargeTaxes :exec
DELETE FROM fixed_charge_taxes WHERE organization_id = @organization_id AND fixed_charge_id = @fixed_charge_id;

-- name: InsertFixedChargeTaxes :exec
INSERT INTO fixed_charge_taxes (organization_id, fixed_charge_id, tax_id, position)
SELECT @organization_id, @fixed_charge_id, t.id, t.ord
FROM unnest(@tax_ids::uuid[]) WITH ORDINALITY AS t(id, ord);
