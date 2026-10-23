-- Coupons with their limitations, and applied coupons.

-- name: CreateCoupon :execrows
INSERT INTO coupons (
  organization_id, id, code, name, description, coupon_type, amount, amount_currency, percentage_rate,
  frequency, frequency_duration, reusable, expiration, expiration_at, status, terminated_at, deleted_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @code, @name, @description, @coupon_type, @amount, @amount_currency, @percentage_rate,
  @frequency, @frequency_duration, @reusable, @expiration, @expiration_at, @status, @terminated_at, @deleted_at, @created_at, @updated_at
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
  deleted_at = @deleted_at, created_at = @created_at, updated_at = @updated_at
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
WHERE c.organization_id = @organization_id AND c.code = @code AND c.deleted_at IS NULL;

-- name: ListCoupons :many
-- @recent lists by status, expiration and expiration_at (none last), then
-- by created_at, newest first, and by id (coupon.rb
-- order_by_status_and_expiration, base_query.rb:111-112); its cursor is
-- (@after_status, @after_expiration, @after_expiration_at, @after_at,
-- @after).
SELECT sqlc.embed(c),
  ARRAY(SELECT p.plan_id FROM coupon_plans p
        WHERE p.organization_id = c.organization_id AND p.coupon_id = c.id ORDER BY p.position)::uuid[] AS plan_ids,
  ARRAY(SELECT m.billable_metric_id FROM coupon_billable_metrics m
        WHERE m.organization_id = c.organization_id AND m.coupon_id = c.id ORDER BY m.position)::uuid[] AS billable_metric_ids
FROM coupons c
WHERE c.organization_id = @organization_id AND c.deleted_at IS NULL
  AND (sqlc.narg(status)::text IS NULL OR c.status = sqlc.narg(status))
  AND (sqlc.narg(search)::text IS NULL OR c.code ILIKE sqlc.narg(search) OR c.name ILIKE sqlc.narg(search))
  AND (sqlc.narg(after)::uuid IS NULL
       OR (@recent::boolean AND (
         c.status > sqlc.narg(after_status)::text
         OR (c.status = sqlc.narg(after_status)::text AND (
           c.expiration > sqlc.narg(after_expiration)::text
           OR (c.expiration = sqlc.narg(after_expiration)::text AND (
             (sqlc.narg(after_expiration_at)::timestamptz IS NOT NULL
              AND (c.expiration_at IS NULL OR c.expiration_at > sqlc.narg(after_expiration_at)::timestamptz))
             OR (c.expiration_at IS NOT DISTINCT FROM sqlc.narg(after_expiration_at)::timestamptz AND (
               c.created_at < sqlc.narg(after_at)::timestamptz
               OR (c.created_at = sqlc.narg(after_at)::timestamptz AND c.id > sqlc.narg(after))))))))))
       OR (NOT @recent::boolean AND c.id > sqlc.narg(after)))
ORDER BY
  CASE WHEN @recent::boolean THEN c.status END,
  CASE WHEN @recent::boolean THEN c.expiration END,
  CASE WHEN @recent::boolean THEN c.expiration_at END,
  CASE WHEN @recent::boolean THEN c.created_at END DESC,
  c.id
LIMIT @lim;

-- name: ListExpiredCoupons :many
-- Across organizations: the periodic termination job.
SELECT sqlc.embed(c),
  ARRAY(SELECT p.plan_id FROM coupon_plans p
        WHERE p.organization_id = c.organization_id AND p.coupon_id = c.id ORDER BY p.position)::uuid[] AS plan_ids,
  ARRAY(SELECT m.billable_metric_id FROM coupon_billable_metrics m
        WHERE m.organization_id = c.organization_id AND m.coupon_id = c.id ORDER BY m.position)::uuid[] AS billable_metric_ids
FROM coupons c
WHERE c.status = 'active' AND c.deleted_at IS NULL AND c.expiration = 'time_limit' AND c.expiration_at <= @now::timestamptz
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
-- @recent lists by created_at, newest first, then by id ascending
-- (base_query.rb:111-112); its cursor is (@after_at, @after).
SELECT a.* FROM applied_coupons a
WHERE a.organization_id = @organization_id
  AND (sqlc.narg(customer_id)::uuid IS NULL OR a.customer_id = sqlc.narg(customer_id))
  AND (sqlc.narg(coupon_id)::uuid IS NULL OR a.coupon_id = sqlc.narg(coupon_id))
  AND (sqlc.narg(status)::text IS NULL OR a.status = sqlc.narg(status))
  AND ((cardinality(@coupon_ids::uuid[]) = 0 AND cardinality(@coupon_codes::text[]) = 0)
       OR a.coupon_id = ANY(@coupon_ids::uuid[])
       OR EXISTS (
         SELECT 1 FROM coupons c
         WHERE c.organization_id = a.organization_id AND c.id = a.coupon_id AND c.code = ANY(@coupon_codes::text[])
       ))
  AND (NOT @kept_customers::boolean OR EXISTS (
    SELECT 1 FROM customers cu
    WHERE cu.organization_id = a.organization_id AND cu.id = a.customer_id AND cu.deleted_at IS NULL
  ))
  AND (sqlc.narg(after)::uuid IS NULL
       OR (@recent::boolean AND (a.created_at < sqlc.narg(after_at)::timestamptz
                                 OR (a.created_at = sqlc.narg(after_at)::timestamptz AND a.id > sqlc.narg(after))))
       OR (NOT @recent::boolean AND a.id > sqlc.narg(after)))
ORDER BY CASE WHEN @recent::boolean THEN a.created_at END DESC, a.id
LIMIT @lim;
