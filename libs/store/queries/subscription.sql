-- Subscriptions, activation rules, fixed charge events, applied usage
-- thresholds and lifetime usage.

-- name: CreateSubscription :execrows
INSERT INTO subscriptions (
  organization_id, id, external_id, customer_id, plan_id, contract, billing_anchor_date, name, billing_time, status,
  subscription_at, started_at, ending_at, terminated_at, canceled_at, trial_ended_at,
  cancellation_reason, previous_subscription_id, on_termination_credit_note, on_termination_invoice,
  payment_method_type, payment_method_id, progressive_billing_disabled, consolidate_invoice,
  purchase_order_number, billing_entity_id, skip_invoice_custom_sections, next_billing_at,
  skip_daily_usage, created_at, updated_at
) VALUES (
  @organization_id, @id, @external_id, @customer_id, @plan_id, @contract, @billing_anchor_date, @name, @billing_time, @status,
  @subscription_at, @started_at, @ending_at, @terminated_at, @canceled_at, @trial_ended_at,
  @cancellation_reason, @previous_subscription_id, @on_termination_credit_note, @on_termination_invoice,
  @payment_method_type, @payment_method_id, @progressive_billing_disabled, @consolidate_invoice,
  @purchase_order_number, @billing_entity_id, @skip_invoice_custom_sections, @next_billing_at,
  @skip_daily_usage, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateSubscription :one
-- Returns the external ID the row had, locked first so that it is the one
-- the update replaces: the catalog change of a renamed subscription
-- expires its old external ID too.
WITH old AS (
  SELECT o.id AS old_id, o.external_id AS old_external_id FROM subscriptions o
  WHERE o.organization_id = @organization_id::uuid AND o.id = @id::uuid
  FOR UPDATE
)
UPDATE subscriptions SET
  organization_id = @organization_id, id = @id, external_id = @external_id, customer_id = @customer_id,
  plan_id = @plan_id, contract = @contract, billing_anchor_date = @billing_anchor_date, name = @name, billing_time = @billing_time, status = @status,
  subscription_at = @subscription_at, started_at = @started_at, ending_at = @ending_at,
  terminated_at = @terminated_at, canceled_at = @canceled_at, trial_ended_at = @trial_ended_at,
  cancellation_reason = @cancellation_reason, previous_subscription_id = @previous_subscription_id,
  on_termination_credit_note = @on_termination_credit_note, on_termination_invoice = @on_termination_invoice,
  payment_method_type = @payment_method_type, payment_method_id = @payment_method_id,
  progressive_billing_disabled = @progressive_billing_disabled, consolidate_invoice = @consolidate_invoice,
  purchase_order_number = @purchase_order_number, billing_entity_id = @billing_entity_id,
  skip_invoice_custom_sections = @skip_invoice_custom_sections, next_billing_at = @next_billing_at,
  skip_daily_usage = @skip_daily_usage, created_at = @created_at, updated_at = @updated_at
FROM old
WHERE subscriptions.organization_id = @organization_id AND subscriptions.id = old.old_id
RETURNING old.old_external_id;

-- name: RecordSubscriptionEventReceived :exec
UPDATE subscriptions SET last_received_event_on = @day::date
WHERE organization_id = @organization_id AND id = @id AND status = 'active'
  AND last_received_event_on IS DISTINCT FROM @day::date;

-- name: DeleteSubscriptionSections :exec
DELETE FROM subscription_invoice_custom_sections WHERE organization_id = @organization_id AND subscription_id = @subscription_id;

-- name: InsertSubscriptionSections :exec
INSERT INTO subscription_invoice_custom_sections (organization_id, subscription_id, invoice_custom_section_id, position)
SELECT @organization_id, @subscription_id, s.id, s.ord
FROM unnest(@section_ids::uuid[]) WITH ORDINALITY AS s(id, ord);

-- name: GetSubscription :one
SELECT sqlc.embed(s),
  ARRAY(SELECT c.invoice_custom_section_id FROM subscription_invoice_custom_sections c
        WHERE c.organization_id = s.organization_id AND c.subscription_id = s.id ORDER BY c.position)::uuid[] AS invoice_custom_section_ids
FROM subscriptions s
WHERE s.organization_id = @organization_id AND s.id = @id;

-- name: GetSubscriptionByExternalID :one
SELECT sqlc.embed(s),
  ARRAY(SELECT c.invoice_custom_section_id FROM subscription_invoice_custom_sections c
        WHERE c.organization_id = s.organization_id AND c.subscription_id = s.id ORDER BY c.position)::uuid[] AS invoice_custom_section_ids
FROM subscriptions s
WHERE s.organization_id = @organization_id AND s.external_id = @external_id AND NOT s.contract
  AND s.status IN ('active', 'incomplete', 'pending')
ORDER BY CASE s.status WHEN 'active' THEN 0 WHEN 'incomplete' THEN 1 ELSE 2 END, s.id
LIMIT 1;

-- name: ListSubscriptions :many
-- The subscription list of the API (subscription.Filter). @recent lists by
-- subscription_at, most recent first, then by created_at, newest first,
-- then by id ascending (subscriptions_query.rb:24-30, base_query.rb:111-112);
-- its cursor is (@after_at, @after_created_at, @after).
SELECT sqlc.embed(s),
  ARRAY(SELECT c.invoice_custom_section_id FROM subscription_invoice_custom_sections c
        WHERE c.organization_id = s.organization_id AND c.subscription_id = s.id ORDER BY c.position)::uuid[] AS invoice_custom_section_ids
FROM subscriptions s
WHERE s.organization_id = @organization_id AND NOT s.contract
  AND (sqlc.narg(customer_id)::uuid IS NULL OR s.customer_id = sqlc.narg(customer_id))
  AND (sqlc.narg(external_customer_id)::text IS NULL OR EXISTS (
    SELECT 1 FROM customers c
    WHERE c.organization_id = s.organization_id AND c.id = s.customer_id
      AND c.external_id = sqlc.narg(external_customer_id) AND c.deleted_at IS NULL))
  AND (sqlc.narg(plan_id)::uuid IS NULL OR s.plan_id = sqlc.narg(plan_id))
  AND ((sqlc.narg(plan_code)::text IS NULL AND sqlc.narg(overridden)::boolean IS NULL AND sqlc.narg(currency)::text IS NULL)
       OR EXISTS (
         SELECT 1 FROM plans p
         WHERE p.organization_id = s.organization_id AND p.id = s.plan_id AND p.status <> 'deleted'
           AND (sqlc.narg(plan_code)::text IS NULL OR p.code = sqlc.narg(plan_code))
           AND (sqlc.narg(overridden)::boolean IS NULL OR (p.parent_id IS NOT NULL) = sqlc.narg(overridden))
           AND (sqlc.narg(currency)::text IS NULL OR p.currency = sqlc.narg(currency))))
  AND (sqlc.narg(external_id)::text IS NULL OR s.external_id = sqlc.narg(external_id))
  AND (cardinality(@statuses::text[]) = 0 OR s.status = ANY(@statuses::text[]))
  AND (cardinality(@billing_entity_ids::uuid[]) = 0
       OR s.billing_entity_id = ANY(@billing_entity_ids::uuid[])
       OR (s.billing_entity_id IS NULL AND EXISTS (
         SELECT 1 FROM customers c
         WHERE c.organization_id = s.organization_id AND c.id = s.customer_id
           AND c.billing_entity_id = ANY(@billing_entity_ids::uuid[]))))
  AND (NOT @exclude_next_subscriptions::boolean
       OR s.previous_subscription_id IS NULL
       OR s.status = 'canceled'
       OR EXISTS (
         SELECT 1 FROM subscriptions prev
         WHERE prev.organization_id = s.organization_id AND prev.id = s.previous_subscription_id
           AND (prev.status = 'terminated'
                OR (cardinality(@statuses::text[]) > 0
                    AND prev.status <> ALL(@statuses::text[]) AND s.status = ANY(@statuses::text[])))))
  AND (sqlc.narg(search)::text IS NULL
       OR s.name ILIKE sqlc.narg(search) OR s.external_id ILIKE sqlc.narg(search)
       OR s.id = sqlc.narg(search_id)::uuid
       OR EXISTS (
         SELECT 1 FROM plans p
         WHERE p.organization_id = s.organization_id AND p.id = s.plan_id AND p.status <> 'deleted'
           AND (p.name ILIKE sqlc.narg(search) OR p.code ILIKE sqlc.narg(search)))
       OR (@search_customers::boolean AND EXISTS (
         SELECT 1 FROM customers c
         WHERE c.organization_id = s.organization_id AND c.id = s.customer_id AND c.deleted_at IS NULL
           AND (c.name ILIKE sqlc.narg(search) OR c.firstname ILIKE sqlc.narg(search)
                OR c.lastname ILIKE sqlc.narg(search) OR c.external_id ILIKE sqlc.narg(search)
                OR c.email ILIKE sqlc.narg(search)))))
  AND (sqlc.narg(after)::uuid IS NULL
       OR (@recent::boolean AND (s.subscription_at < sqlc.narg(after_at)::timestamptz
                                 OR (s.subscription_at = sqlc.narg(after_at)::timestamptz
                                     AND (s.created_at < sqlc.narg(after_created_at)::timestamptz
                                          OR (s.created_at = sqlc.narg(after_created_at)::timestamptz AND s.id > sqlc.narg(after))))))
       OR (NOT @recent::boolean AND s.id > sqlc.narg(after)))
ORDER BY CASE WHEN @recent::boolean THEN s.subscription_at END DESC,
  CASE WHEN @recent::boolean THEN s.created_at END DESC, s.id
LIMIT @lim;

-- name: ListSubscriptionsByExternalID :many
SELECT sqlc.embed(s),
  ARRAY(SELECT c.invoice_custom_section_id FROM subscription_invoice_custom_sections c
        WHERE c.organization_id = s.organization_id AND c.subscription_id = s.id ORDER BY c.position)::uuid[] AS invoice_custom_section_ids
FROM subscriptions s
WHERE s.organization_id = @organization_id AND s.external_id = @external_id
ORDER BY s.id;

-- name: GetInitialSubscriptionStartedAt :one
SELECT s.started_at FROM subscriptions s
WHERE s.organization_id = @organization_id AND s.customer_id = @customer_id
  AND s.external_id = @external_id AND s.started_at IS NOT NULL
ORDER BY s.started_at
LIMIT 1;

-- name: ListSubscriptionsByCustomer :many
SELECT sqlc.embed(s),
  ARRAY(SELECT c.invoice_custom_section_id FROM subscription_invoice_custom_sections c
        WHERE c.organization_id = s.organization_id AND c.subscription_id = s.id ORDER BY c.position)::uuid[] AS invoice_custom_section_ids
FROM subscriptions s
WHERE s.organization_id = @organization_id AND s.customer_id = @customer_id AND NOT s.contract
  AND (cardinality(@statuses::text[]) = 0 OR s.status = ANY(@statuses::text[]))
ORDER BY s.id;

-- name: ListActiveOrPendingSubscriptionsByPlan :many
SELECT sqlc.embed(s),
  ARRAY(SELECT c.invoice_custom_section_id FROM subscription_invoice_custom_sections c
        WHERE c.organization_id = s.organization_id AND c.subscription_id = s.id ORDER BY c.position)::uuid[] AS invoice_custom_section_ids
FROM subscriptions s
WHERE s.organization_id = @organization_id AND s.plan_id = @plan_id AND NOT s.contract AND s.status IN ('active', 'pending')
ORDER BY s.id;

-- name: ListSubscriptionsDueForBilling :many
-- The cursor is the id, so pages follow the id; the repository orders each
-- page by next_billing_at.
SELECT sqlc.embed(s),
  ARRAY(SELECT c.invoice_custom_section_id FROM subscription_invoice_custom_sections c
        WHERE c.organization_id = s.organization_id AND c.subscription_id = s.id ORDER BY c.position)::uuid[] AS invoice_custom_section_ids
FROM subscriptions s
WHERE s.organization_id = @organization_id AND s.status = 'active' AND NOT s.contract
  AND s.next_billing_at IS NOT NULL AND s.next_billing_at <= @at::timestamptz
  AND (sqlc.narg(after)::uuid IS NULL OR s.id > sqlc.narg(after))
ORDER BY s.id
LIMIT @lim;

-- name: ListPendingSubscriptionsToActivate :many
SELECT sqlc.embed(s),
  ARRAY(SELECT c.invoice_custom_section_id FROM subscription_invoice_custom_sections c
        WHERE c.organization_id = s.organization_id AND c.subscription_id = s.id ORDER BY c.position)::uuid[] AS invoice_custom_section_ids
FROM subscriptions s
WHERE s.organization_id = @organization_id AND s.status = 'pending' AND NOT s.contract
  AND s.previous_subscription_id IS NULL AND s.subscription_at <= @at
ORDER BY s.id;

-- name: GetNextSubscription :one
SELECT sqlc.embed(s),
  ARRAY(SELECT c.invoice_custom_section_id FROM subscription_invoice_custom_sections c
        WHERE c.organization_id = s.organization_id AND c.subscription_id = s.id ORDER BY c.position)::uuid[] AS invoice_custom_section_ids
FROM subscriptions s
WHERE s.organization_id = @organization_id AND s.previous_subscription_id = @previous_subscription_id
  AND s.status = 'pending'
ORDER BY s.id
LIMIT 1;

-- name: SubscriptionChain :many
-- The subscription and its predecessors, newest first (depth 0 is the
-- subscription itself); the cycle guard stops at a subscription already
-- seen.
WITH RECURSIVE chain (id, previous_subscription_id, depth, path) AS (
  SELECT c.id, c.previous_subscription_id, 0, ARRAY[c.id]
  FROM subscriptions c
  WHERE c.organization_id = @organization_id AND c.id = @id
  UNION ALL
  SELECT p.id, p.previous_subscription_id, chain.depth + 1, chain.path || p.id
  FROM chain
  JOIN subscriptions p ON p.organization_id = @organization_id AND p.id = chain.previous_subscription_id
  WHERE NOT p.id = ANY(chain.path)
)
SELECT sqlc.embed(s),
  ARRAY(SELECT c.invoice_custom_section_id FROM subscription_invoice_custom_sections c
        WHERE c.organization_id = s.organization_id AND c.subscription_id = s.id ORDER BY c.position)::uuid[] AS invoice_custom_section_ids
FROM chain
JOIN subscriptions s ON s.organization_id = @organization_id AND s.id = chain.id
ORDER BY chain.depth DESC;

-- name: ListSubscriptionsEndingBetween :many
SELECT sqlc.embed(s),
  ARRAY(SELECT c.invoice_custom_section_id FROM subscription_invoice_custom_sections c
        WHERE c.organization_id = s.organization_id AND c.subscription_id = s.id ORDER BY c.position)::uuid[] AS invoice_custom_section_ids
FROM subscriptions s
WHERE s.organization_id = @organization_id AND s.status = 'active' AND NOT s.contract
  AND s.ending_at >= @from_at::timestamptz AND s.ending_at < @to_at::timestamptz
ORDER BY s.id;

-- name: ListIncompleteSubscriptionsWithExpiredRules :many
SELECT sqlc.embed(s),
  ARRAY(SELECT c.invoice_custom_section_id FROM subscription_invoice_custom_sections c
        WHERE c.organization_id = s.organization_id AND c.subscription_id = s.id ORDER BY c.position)::uuid[] AS invoice_custom_section_ids
FROM subscriptions s
WHERE s.organization_id = @organization_id AND s.status = 'incomplete'
  AND EXISTS (
    SELECT 1 FROM subscription_activation_rules r
    WHERE r.organization_id = s.organization_id AND r.subscription_id = s.id
      AND r.status = 'pending' AND r.expires_at <= @now::timestamptz
  )
ORDER BY s.id;

-- name: CreateActivationRule :execrows
INSERT INTO subscription_activation_rules (
  organization_id, id, subscription_id, rule_type, timeout_hours, status, expires_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @subscription_id, @rule_type, @timeout_hours, @status, @expires_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: InsertActivationRule :exec
-- A plain insert: a taken ID fails (ReplaceForSubscription runs it under a
-- savepoint).
INSERT INTO subscription_activation_rules (
  organization_id, id, subscription_id, rule_type, timeout_hours, status, expires_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @subscription_id, @rule_type, @timeout_hours, @status, @expires_at, @created_at, @updated_at
);

-- name: UpdateActivationRule :execrows
UPDATE subscription_activation_rules SET
  organization_id = @organization_id, id = @id, subscription_id = @subscription_id, rule_type = @rule_type,
  timeout_hours = @timeout_hours, status = @status, expires_at = @expires_at,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: ListActivationRulesBySubscription :many
SELECT * FROM subscription_activation_rules
WHERE organization_id = @organization_id AND subscription_id = @subscription_id
ORDER BY id;

-- name: DeleteActivationRulesBySubscription :exec
DELETE FROM subscription_activation_rules WHERE organization_id = @organization_id AND subscription_id = @subscription_id;

-- name: DeleteFixedChargeEvents :exec
DELETE FROM fixed_charge_events WHERE organization_id = @organization_id AND id = ANY(@ids::uuid[]);

-- name: CreateFixedChargeEvent :execrows
INSERT INTO fixed_charge_events (
  organization_id, id, subscription_id, fixed_charge_id, units, timestamp, apply_units_immediately, created_at, updated_at
) VALUES (
  @organization_id, @id, @subscription_id, @fixed_charge_id, @units, @timestamp, @apply_units_immediately, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: ListFixedChargeEventsBySubscription :many
SELECT * FROM fixed_charge_events
WHERE organization_id = @organization_id AND subscription_id = @subscription_id
  AND (sqlc.narg(fixed_charge_id)::uuid IS NULL OR fixed_charge_id = sqlc.narg(fixed_charge_id))
ORDER BY timestamp, id;

-- name: CreateAppliedUsageThreshold :execrows
INSERT INTO applied_usage_thresholds (
  organization_id, id, usage_threshold_id, subscription_id, invoice_id, lifetime_usage_amount, created_at, updated_at
) VALUES (
  @organization_id, @id, @usage_threshold_id, @subscription_id, @invoice_id, @lifetime_usage_amount, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: ListAppliedUsageThresholdsBySubscription :many
SELECT * FROM applied_usage_thresholds
WHERE organization_id = @organization_id AND subscription_id = @subscription_id
ORDER BY id;

-- name: ListAppliedUsageThresholdsByInvoice :many
SELECT * FROM applied_usage_thresholds
WHERE organization_id = @organization_id AND invoice_id = @invoice_id
ORDER BY id;

-- name: UpsertLifetimeUsage :one
-- Inserts the record, or replaces the chain's record keeping its id and
-- creation instant; returns the stored id and creation instant. An id
-- taken by another chain's record conflicts on the primary key, which ON
-- CONFLICT (external_subscription_id) does not catch: the repository runs
-- it under a savepoint.
INSERT INTO lifetime_usages (
  organization_id, id, subscription_id, external_subscription_id, currency,
  historical_usage_amount, invoiced_usage_amount, current_usage_amount,
  recalculate_invoiced_usage, recalculate_current_usage,
  invoiced_usage_amount_refreshed_at, current_usage_amount_refreshed_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @subscription_id, @external_subscription_id, @currency,
  @historical_usage_amount, @invoiced_usage_amount, @current_usage_amount,
  @recalculate_invoiced_usage, @recalculate_current_usage,
  @invoiced_usage_amount_refreshed_at, @current_usage_amount_refreshed_at, @created_at, @updated_at
)
ON CONFLICT (organization_id, external_subscription_id) DO UPDATE SET
  subscription_id = EXCLUDED.subscription_id, currency = EXCLUDED.currency,
  historical_usage_amount = EXCLUDED.historical_usage_amount,
  invoiced_usage_amount = EXCLUDED.invoiced_usage_amount,
  current_usage_amount = EXCLUDED.current_usage_amount,
  recalculate_invoiced_usage = EXCLUDED.recalculate_invoiced_usage,
  recalculate_current_usage = EXCLUDED.recalculate_current_usage,
  invoiced_usage_amount_refreshed_at = EXCLUDED.invoiced_usage_amount_refreshed_at,
  current_usage_amount_refreshed_at = EXCLUDED.current_usage_amount_refreshed_at,
  updated_at = EXCLUDED.updated_at
RETURNING id, created_at;

-- name: GetLifetimeUsageByExternalID :one
SELECT * FROM lifetime_usages
WHERE organization_id = @organization_id AND external_subscription_id = @external_subscription_id;

-- name: GetLifetimeUsageBySubscription :one
SELECT * FROM lifetime_usages
WHERE organization_id = @organization_id AND subscription_id = @subscription_id
ORDER BY id
LIMIT 1;

-- name: ListLifetimeUsagesToRecalculate :many
SELECT * FROM lifetime_usages
WHERE organization_id = @organization_id AND recalculate_invoiced_usage
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;
