-- Coupons with their limitations, and applied coupons.

-- name: CreateCoupon :execrows
INSERT INTO coupons (
  organization_id, id, code, name, description, coupon_type, amount, amount_currency, percentage_rate,
  frequency, frequency_duration, reusable, expiration, expiration_at, status, terminated_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @code, @name, @description, @coupon_type, @amount, @amount_currency, @percentage_rate,
  @frequency, @frequency_duration, @reusable, @expiration, @expiration_at, @status, @terminated_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateCoupon :execrows
UPDATE coupons SET
  -- The key columns are assigned to themselves first so that the parameters
  -- follow the order of CreateCoupon (see repo_coupon.go).
  organization_id = @organization_id, id = @id, code = @code, name = @name, description = @description,
  coupon_type = @coupon_type, amount = @amount, amount_currency = @amount_currency, percentage_rate = @percentage_rate,
  frequency = @frequency, frequency_duration = @frequency_duration, reusable = @reusable,
  expiration = @expiration, expiration_at = @expiration_at, status = @status, terminated_at = @terminated_at,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetCoupon :one
SELECT sqlc.embed(c),
  ARRAY(SELECT p.plan_id FROM coupon_plans p
        WHERE p.organization_id = c.organization_id AND p.coupon_id = c.id ORDER BY p.position)::uuid[] AS plan_ids,
  ARRAY(SELECT m.billable_metric_id FROM coupon_billable_metrics m
        WHERE m.organization_id = c.organization_id AND m.coupon_id = c.id ORDER BY m.position)::uuid[] AS billable_metric_ids
FROM coupons c
WHERE c.organization_id = @organization_id AND c.id = @id;

-- name: GetCouponByCode :one
SELECT sqlc.embed(c),
  ARRAY(SELECT p.plan_id FROM coupon_plans p
        WHERE p.organization_id = c.organization_id AND p.coupon_id = c.id ORDER BY p.position)::uuid[] AS plan_ids,
  ARRAY(SELECT m.billable_metric_id FROM coupon_billable_metrics m
        WHERE m.organization_id = c.organization_id AND m.coupon_id = c.id ORDER BY m.position)::uuid[] AS billable_metric_ids
FROM coupons c
WHERE c.organization_id = @organization_id AND c.code = @code AND c.status = 'active';

-- name: ListCoupons :many
SELECT sqlc.embed(c),
  ARRAY(SELECT p.plan_id FROM coupon_plans p
        WHERE p.organization_id = c.organization_id AND p.coupon_id = c.id ORDER BY p.position)::uuid[] AS plan_ids,
  ARRAY(SELECT m.billable_metric_id FROM coupon_billable_metrics m
        WHERE m.organization_id = c.organization_id AND m.coupon_id = c.id ORDER BY m.position)::uuid[] AS billable_metric_ids
FROM coupons c
WHERE c.organization_id = @organization_id
  AND (sqlc.narg(status)::text IS NULL OR c.status = sqlc.narg(status))
  AND (sqlc.narg(search)::text IS NULL OR c.code ILIKE sqlc.narg(search) OR c.name ILIKE sqlc.narg(search))
  AND (sqlc.narg(after)::uuid IS NULL OR c.id > sqlc.narg(after))
ORDER BY c.id
LIMIT @lim;

-- name: ListExpiredCoupons :many
-- Across organizations: the periodic termination job.
SELECT sqlc.embed(c),
  ARRAY(SELECT p.plan_id FROM coupon_plans p
        WHERE p.organization_id = c.organization_id AND p.coupon_id = c.id ORDER BY p.position)::uuid[] AS plan_ids,
  ARRAY(SELECT m.billable_metric_id FROM coupon_billable_metrics m
        WHERE m.organization_id = c.organization_id AND m.coupon_id = c.id ORDER BY m.position)::uuid[] AS billable_metric_ids
FROM coupons c
WHERE c.status = 'active' AND c.expiration = 'time_limit' AND c.expiration_at <= @now::timestamptz
  AND (sqlc.narg(after)::uuid IS NULL OR c.id > sqlc.narg(after))
ORDER BY c.id
LIMIT @lim;

-- name: DeleteCouponPlans :exec
DELETE FROM coupon_plans WHERE organization_id = @organization_id AND coupon_id = @coupon_id;

-- name: InsertCouponPlans :exec
INSERT INTO coupon_plans (organization_id, coupon_id, plan_id, position)
SELECT @organization_id, @coupon_id, p.id, p.ord
FROM unnest(@plan_ids::uuid[]) WITH ORDINALITY AS p(id, ord);

-- name: DeleteCouponBillableMetrics :exec
DELETE FROM coupon_billable_metrics WHERE organization_id = @organization_id AND coupon_id = @coupon_id;

-- name: InsertCouponBillableMetrics :exec
INSERT INTO coupon_billable_metrics (organization_id, coupon_id, billable_metric_id, position)
SELECT @organization_id, @coupon_id, m.id, m.ord
FROM unnest(@billable_metric_ids::uuid[]) WITH ORDINALITY AS m(id, ord);

-- name: CreateAppliedCoupon :execrows
INSERT INTO applied_coupons (
  organization_id, id, coupon_id, customer_id, amount, amount_currency, percentage_rate,
  frequency, frequency_duration, frequency_duration_remaining, remaining_amount, status, terminated_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @coupon_id, @customer_id, @amount, @amount_currency, @percentage_rate,
  @frequency, @frequency_duration, @frequency_duration_remaining, @remaining_amount, @status, @terminated_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateAppliedCoupon :execrows
UPDATE applied_coupons SET
  organization_id = @organization_id, id = @id, coupon_id = @coupon_id, customer_id = @customer_id,
  amount = @amount, amount_currency = @amount_currency, percentage_rate = @percentage_rate,
  frequency = @frequency, frequency_duration = @frequency_duration, frequency_duration_remaining = @frequency_duration_remaining,
  remaining_amount = @remaining_amount, status = @status, terminated_at = @terminated_at,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetAppliedCoupon :one
SELECT * FROM applied_coupons WHERE organization_id = @organization_id AND id = @id;

-- name: ListActiveAppliedCouponsByCustomer :many
SELECT * FROM applied_coupons
WHERE organization_id = @organization_id AND customer_id = @customer_id AND status = 'active'
ORDER BY created_at, id;

-- name: ListActiveAppliedCouponsByCoupon :many
SELECT * FROM applied_coupons
WHERE organization_id = @organization_id AND coupon_id = @coupon_id AND status = 'active'
ORDER BY id;

-- name: CountAppliedCouponsByCouponAndCustomer :one
SELECT count(*) FROM applied_coupons
WHERE organization_id = @organization_id AND coupon_id = @coupon_id AND customer_id = @customer_id;

-- name: ListAppliedCoupons :many
SELECT * FROM applied_coupons
WHERE organization_id = @organization_id
  AND (sqlc.narg(customer_id)::uuid IS NULL OR customer_id = sqlc.narg(customer_id))
  AND (sqlc.narg(coupon_id)::uuid IS NULL OR coupon_id = sqlc.narg(coupon_id))
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;
