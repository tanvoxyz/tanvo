-- Daily usage snapshots.

-- name: CreateDailyUsage :exec
INSERT INTO daily_usages (
  organization_id, id, customer_id, subscription_id, external_subscription_id, usage, usage_diff,
  from_datetime, to_datetime, refreshed_at, usage_date, created_at, updated_at
) VALUES (
  @organization_id, @id, @customer_id, @subscription_id, @external_subscription_id, @usage, @usage_diff,
  @from_datetime, @to_datetime, @refreshed_at, @usage_date, @created_at, @updated_at
);

-- name: GetDailyUsageByDate :one
SELECT * FROM daily_usages
WHERE organization_id = @organization_id AND subscription_id = @subscription_id AND usage_date = @usage_date
ORDER BY id
LIMIT 1;

-- name: GetDailyUsageByPeriodAndDate :one
SELECT * FROM daily_usages
WHERE organization_id = @organization_id AND subscription_id = @subscription_id AND usage_date = @usage_date
  AND from_datetime = @from_datetime AND to_datetime = @to_datetime
ORDER BY id
LIMIT 1;

-- name: GetPreviousDailyUsage :one
SELECT * FROM daily_usages
WHERE organization_id = @organization_id AND subscription_id = @subscription_id
  AND from_datetime = @from_datetime AND to_datetime = @to_datetime AND usage_date < @usage_date
ORDER BY usage_date DESC, id
LIMIT 1;

-- name: ListDailyUsageDaysBetween :many
SELECT DISTINCT subscription_id, usage_date FROM daily_usages
WHERE organization_id = @organization_id AND usage_date BETWEEN @since::date AND @until::date
ORDER BY subscription_id, usage_date;

-- name: ListDailyUsagesBySubscription :many
SELECT * FROM daily_usages
WHERE organization_id = @organization_id AND subscription_id = @subscription_id
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after)::uuid)
ORDER BY id
LIMIT @lim;

-- name: DeleteDailyUsagesFrom :execrows
DELETE FROM daily_usages d
WHERE d.organization_id = @organization_id::uuid AND d.id IN (
  SELECT x.id FROM daily_usages x
  WHERE x.organization_id = @organization_id::uuid AND x.subscription_id = ANY(@subscription_ids::uuid[])
    AND x.usage_date >= @since::date
  LIMIT @lim
);
