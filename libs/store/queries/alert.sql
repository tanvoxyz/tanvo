-- Alerts, their thresholds and triggered alerts.

-- name: CreateAlert :execrows
INSERT INTO alerts (
  organization_id, id, code, name, alert_type, subscription_external_id, wallet_id, billable_metric_id,
  direction, previous_value, last_processed_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @code, @name, @alert_type, @subscription_external_id, @wallet_id, @billable_metric_id,
  @direction, @previous_value, @last_processed_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateAlert :execrows
UPDATE alerts SET
  organization_id = @organization_id, id = @id, code = @code, name = @name, alert_type = @alert_type,
  subscription_external_id = @subscription_external_id, wallet_id = @wallet_id,
  billable_metric_id = @billable_metric_id, direction = @direction, previous_value = @previous_value,
  last_processed_at = @last_processed_at, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteAlertThresholds :exec
DELETE FROM alert_thresholds WHERE organization_id = @organization_id AND alert_id = @alert_id;

-- name: InsertAlertThresholds :exec
-- The thresholds in their order; codes are '' for none (a code is never
-- empty).
INSERT INTO alert_thresholds (organization_id, alert_id, position, id, code, value, recurring)
SELECT @organization_id, @alert_id, i.ord, i.id, NULLIF(c.code, ''), v.value::numeric, r.recurring
FROM unnest(@ids::uuid[]) WITH ORDINALITY AS i(id, ord)
JOIN unnest(@codes::text[]) WITH ORDINALITY AS c(code, ord) ON c.ord = i.ord
JOIN unnest(@threshold_values::text[]) WITH ORDINALITY AS v(value, ord) ON v.ord = i.ord
JOIN unnest(@recurrings::boolean[]) WITH ORDINALITY AS r(recurring, ord) ON r.ord = i.ord;

-- name: ListAlertThresholds :many
SELECT * FROM alert_thresholds
WHERE organization_id = @organization_id AND alert_id = ANY(@alert_ids::uuid[])
ORDER BY alert_id, position;

-- name: GetAlert :one
SELECT * FROM alerts WHERE organization_id = @organization_id AND id = @id;

-- name: GetAlertBySubscriptionCode :one
SELECT * FROM alerts
WHERE organization_id = @organization_id AND wallet_id IS NULL
  AND subscription_external_id = @subscription_external_id AND code = @code;

-- name: GetAlertByWalletCode :one
SELECT * FROM alerts WHERE organization_id = @organization_id AND wallet_id = @wallet_id AND code = @code;

-- name: ListAlertsBySubscription :many
SELECT * FROM alerts
WHERE organization_id = @organization_id AND wallet_id IS NULL AND subscription_external_id = @subscription_external_id
ORDER BY id;

-- name: ListAlertsByWallet :many
SELECT * FROM alerts WHERE organization_id = @organization_id AND wallet_id = @wallet_id ORDER BY id;

-- name: ListAlertsByBillableMetric :many
SELECT * FROM alerts WHERE organization_id = @organization_id AND billable_metric_id = @billable_metric_id ORDER BY id;

-- name: ListAlerts :many
SELECT * FROM alerts
WHERE organization_id = @organization_id
  AND (sqlc.narg(subscription_external_id)::text IS NULL OR subscription_external_id = sqlc.narg(subscription_external_id))
  AND (sqlc.narg(wallet_id)::uuid IS NULL OR wallet_id = sqlc.narg(wallet_id))
  AND (cardinality(@alert_types::text[]) = 0 OR alert_type = ANY(@alert_types::text[]))
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;

-- name: DeleteAlert :execrows
DELETE FROM alerts WHERE organization_id = @organization_id AND id = @id;

-- name: CreateTriggeredAlert :execrows
INSERT INTO triggered_alerts (
  organization_id, id, alert_id, subscription_id, wallet_id, current_value, previous_value,
  crossed_thresholds, triggered_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @alert_id, @subscription_id, @wallet_id, @current_value, @previous_value,
  @crossed_thresholds, @triggered_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: GetTriggeredAlert :one
SELECT * FROM triggered_alerts WHERE organization_id = @organization_id AND id = @id;

-- name: ListTriggeredAlerts :many
SELECT * FROM triggered_alerts
WHERE organization_id = @organization_id
  AND (sqlc.narg(alert_id)::uuid IS NULL OR alert_id = sqlc.narg(alert_id))
  AND (sqlc.narg(subscription_id)::uuid IS NULL OR subscription_id = sqlc.narg(subscription_id))
  AND (sqlc.narg(wallet_id)::uuid IS NULL OR wallet_id = sqlc.narg(wallet_id))
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;
