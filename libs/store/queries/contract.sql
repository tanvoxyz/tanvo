-- Contracts (subscriptions priced by rate cards), their rate cards and
-- phases, and their billing segments (domain §21.4).

-- name: GetLiveContract :one
-- The pending contract first, then the latest started, then the latest
-- created (contract.rb live_by_external_id).
SELECT sqlc.embed(s),
  ARRAY(SELECT c.invoice_custom_section_id FROM subscription_invoice_custom_sections c
        WHERE c.organization_id = s.organization_id AND c.subscription_id = s.id ORDER BY c.position)::uuid[] AS invoice_custom_section_ids
FROM subscriptions s
WHERE s.organization_id = @organization_id AND s.contract AND s.external_id = @external_id
  AND s.status IN ('pending', 'active')
ORDER BY CASE s.status WHEN 'pending' THEN 0 ELSE 1 END, s.subscription_at DESC, s.created_at DESC, s.id
LIMIT 1;

-- name: GetContractByStatus :one
SELECT sqlc.embed(s),
  ARRAY(SELECT c.invoice_custom_section_id FROM subscription_invoice_custom_sections c
        WHERE c.organization_id = s.organization_id AND c.subscription_id = s.id ORDER BY c.position)::uuid[] AS invoice_custom_section_ids
FROM subscriptions s
WHERE s.organization_id = @organization_id AND s.contract AND s.external_id = @external_id AND s.status = @status
ORDER BY s.subscription_at DESC, s.created_at DESC, s.id
LIMIT 1;

-- name: ListContracts :many
-- The contract list of the API (subscription.ContractFilter), newest
-- created first, then by id ascending; the cursor is (@after_at, @after).
SELECT sqlc.embed(s),
  ARRAY(SELECT c.invoice_custom_section_id FROM subscription_invoice_custom_sections c
        WHERE c.organization_id = s.organization_id AND c.subscription_id = s.id ORDER BY c.position)::uuid[] AS invoice_custom_section_ids
FROM subscriptions s
WHERE s.organization_id = @organization_id AND s.contract
  AND (sqlc.narg(external_customer_id)::text IS NULL OR EXISTS (
    SELECT 1 FROM customers c
    WHERE c.organization_id = s.organization_id AND c.id = s.customer_id
      AND c.external_id = sqlc.narg(external_customer_id)))
  AND (sqlc.narg(plan_code)::text IS NULL OR EXISTS (
    SELECT 1 FROM plans p
    WHERE p.organization_id = s.organization_id AND p.id = s.plan_id AND p.code = sqlc.narg(plan_code)))
  AND (sqlc.narg(external_id)::text IS NULL OR s.external_id = sqlc.narg(external_id))
  AND (cardinality(@statuses::text[]) = 0 OR s.status = ANY(@statuses::text[]))
  AND (cardinality(@billing_entity_ids::uuid[]) = 0
       OR s.billing_entity_id = ANY(@billing_entity_ids::uuid[])
       OR (s.billing_entity_id IS NULL AND EXISTS (
         SELECT 1 FROM customers c
         WHERE c.organization_id = s.organization_id AND c.id = s.customer_id
           AND c.billing_entity_id = ANY(@billing_entity_ids::uuid[]))))
  AND (sqlc.narg(has_rate_overrides)::boolean IS NULL
       OR EXISTS (
         SELECT 1 FROM subscription_rate_cards rc
         JOIN subscription_rate_phases ph ON ph.organization_id = rc.organization_id AND ph.subscription_rate_card_id = rc.id
         WHERE rc.organization_id = s.organization_id AND rc.subscription_id = s.id AND rc.deleted_at IS NULL
           AND ph.deleted_at IS NULL AND ph.override_rate_model IS NOT NULL) = sqlc.narg(has_rate_overrides)::boolean)
  AND (sqlc.narg(search)::text IS NULL
       OR s.name ILIKE sqlc.narg(search) OR s.external_id ILIKE sqlc.narg(search)
       OR s.id = sqlc.narg(search_id)::uuid
       OR EXISTS (
         SELECT 1 FROM plans p
         WHERE p.organization_id = s.organization_id AND p.id = s.plan_id
           AND (p.name ILIKE sqlc.narg(search) OR p.code ILIKE sqlc.narg(search)))
       OR (@search_customers::boolean AND EXISTS (
         SELECT 1 FROM customers c
         WHERE c.organization_id = s.organization_id AND c.id = s.customer_id
           AND (c.name ILIKE sqlc.narg(search) OR c.firstname ILIKE sqlc.narg(search)
                OR c.lastname ILIKE sqlc.narg(search) OR c.external_id ILIKE sqlc.narg(search)
                OR c.email ILIKE sqlc.narg(search)))))
  AND (sqlc.narg(after)::uuid IS NULL
       OR s.created_at < sqlc.narg(after_at)::timestamptz
       OR (s.created_at = sqlc.narg(after_at)::timestamptz AND s.id > sqlc.narg(after)))
ORDER BY s.created_at DESC, s.id
LIMIT @lim;

-- name: ListContractsDueForActivation :many
SELECT sqlc.embed(s),
  ARRAY(SELECT c.invoice_custom_section_id FROM subscription_invoice_custom_sections c
        WHERE c.organization_id = s.organization_id AND c.subscription_id = s.id ORDER BY c.position)::uuid[] AS invoice_custom_section_ids
FROM subscriptions s
JOIN customers cu ON cu.organization_id = s.organization_id AND cu.id = s.customer_id
WHERE s.organization_id = @organization_id AND s.contract AND s.status = 'pending'
  AND s.subscription_at <= @at::timestamptz AND cu.deleted_at IS NULL
ORDER BY s.id;

-- name: ContractExistsForPlan :one
SELECT EXISTS (
  SELECT 1 FROM subscriptions s
  WHERE s.organization_id = @organization_id AND s.contract AND s.plan_id = @plan_id
)::boolean;

-- name: CreateSubscriptionRateCard :execrows
INSERT INTO subscription_rate_cards (
  organization_id, id, subscription_id, rate_card_id, units, effective_date, billing_anchor_date,
  next_billing_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @subscription_id, @rate_card_id, @units, @effective_date, @billing_anchor_date,
  @next_billing_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateSubscriptionRateCard :execrows
UPDATE subscription_rate_cards SET
  units = @units, effective_date = @effective_date, billing_anchor_date = @billing_anchor_date,
  next_billing_at = @next_billing_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DiscardSubscriptionRateCard :execrows
UPDATE subscription_rate_cards SET deleted_at = @at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: GetSubscriptionRateCard :one
SELECT * FROM subscription_rate_cards WHERE organization_id = @organization_id AND id = @id;

-- name: GetSubscriptionRateCardByRateCard :one
SELECT * FROM subscription_rate_cards
WHERE organization_id = @organization_id AND subscription_id = @subscription_id
  AND rate_card_id = @rate_card_id AND deleted_at IS NULL;

-- name: ListSubscriptionRateCards :many
-- By effective date, then by id; the cursor is (@after_date, @after).
-- Every set restriction holds.
SELECT rc.* FROM subscription_rate_cards rc
JOIN rate_cards c ON c.organization_id = rc.organization_id AND c.id = rc.rate_card_id
JOIN products pr ON pr.organization_id = c.organization_id AND pr.id = c.product_id
WHERE rc.organization_id = @organization_id AND rc.deleted_at IS NULL
  AND (c.deleted_at IS NULL OR (sqlc.narg(product_id)::uuid IS NULL AND sqlc.narg(product_filter_id)::uuid IS NULL AND sqlc.narg(category_id)::uuid IS NULL))
  AND (sqlc.narg(subscription_id)::uuid IS NULL OR rc.subscription_id = sqlc.narg(subscription_id))
  AND (sqlc.narg(rate_card_id)::uuid IS NULL OR rc.rate_card_id = sqlc.narg(rate_card_id))
  AND (sqlc.narg(product_id)::uuid IS NULL OR c.product_id = sqlc.narg(product_id))
  AND (sqlc.narg(product_filter_id)::uuid IS NULL OR c.product_filter_id = sqlc.narg(product_filter_id))
  AND (sqlc.narg(category_id)::uuid IS NULL OR pr.product_category_id = sqlc.narg(category_id))
  AND (sqlc.narg(after)::uuid IS NULL
       OR rc.effective_date > sqlc.narg(after_date)::date
       OR (rc.effective_date = sqlc.narg(after_date)::date AND rc.id > sqlc.narg(after)))
ORDER BY rc.effective_date, rc.id
LIMIT @lim;

-- name: ListSubscriptionRateCardsDueForBilling :many
-- contract_rate_card.rb due_for_billing; @customer_id NULL lists every
-- customer's due cards.
SELECT rc.* FROM subscription_rate_cards rc
JOIN subscriptions s ON s.organization_id = rc.organization_id AND s.id = rc.subscription_id
JOIN customers cu ON cu.organization_id = s.organization_id AND cu.id = s.customer_id
JOIN billing_entities be ON be.organization_id = cu.organization_id AND be.id = cu.billing_entity_id
WHERE rc.organization_id = @organization_id AND rc.deleted_at IS NULL
  AND rc.next_billing_at <= @at::timestamptz
  AND s.status = 'active' AND s.subscription_at <= @at::timestamptz
  AND cu.deleted_at IS NULL
  AND (sqlc.narg(customer_id)::uuid IS NULL OR s.customer_id = sqlc.narg(customer_id))
  AND EXISTS (
    SELECT 1 FROM rate_card_rates r
    WHERE r.organization_id = rc.organization_id AND r.rate_card_id = rc.rate_card_id AND r.deleted_at IS NULL)
  AND (s.ending_at IS NULL
       OR s.ending_at > (rc.effective_date::timestamp AT TIME ZONE COALESCE(cu.timezone, be.timezone, 'UTC')))
ORDER BY rc.id;

-- name: ListCustomersDueForContractBilling :many
SELECT DISTINCT s.customer_id FROM subscription_rate_cards rc
JOIN subscriptions s ON s.organization_id = rc.organization_id AND s.id = rc.subscription_id
JOIN customers cu ON cu.organization_id = s.organization_id AND cu.id = s.customer_id
JOIN billing_entities be ON be.organization_id = cu.organization_id AND be.id = cu.billing_entity_id
WHERE rc.organization_id = @organization_id AND rc.deleted_at IS NULL
  AND rc.next_billing_at <= @at::timestamptz
  AND s.status = 'active' AND s.subscription_at <= @at::timestamptz
  AND cu.deleted_at IS NULL
  AND EXISTS (
    SELECT 1 FROM rate_card_rates r
    WHERE r.organization_id = rc.organization_id AND r.rate_card_id = rc.rate_card_id AND r.deleted_at IS NULL)
  AND (s.ending_at IS NULL
       OR s.ending_at > (rc.effective_date::timestamp AT TIME ZONE COALESCE(cu.timezone, be.timezone, 'UTC')))
ORDER BY s.customer_id;

-- name: DiscardSubscriptionRatePhases :exec
-- Discards the kept phases of the contract rate card not in @keep.
UPDATE subscription_rate_phases SET deleted_at = @at
WHERE organization_id = @organization_id AND subscription_rate_card_id = @subscription_rate_card_id AND deleted_at IS NULL
  AND NOT (id = ANY(@keep::uuid[]));

-- name: ParkSubscriptionRatePhases :exec
-- Moves the kept phases @ids past every position, so that rewriting them
-- one by one never meets the unique position index.
UPDATE subscription_rate_phases SET position = position + 1000000
WHERE organization_id = @organization_id AND subscription_rate_card_id = @subscription_rate_card_id AND deleted_at IS NULL
  AND id = ANY(@ids::uuid[]);

-- name: UpdateSubscriptionRatePhase :execrows
UPDATE subscription_rate_phases SET
  code = @code, position = @position, name = @name, billing_interval_cycle_count = @billing_interval_cycle_count,
  override_rate_model = @override_rate_model, override_properties = @override_properties,
  override_min_amount = @override_min_amount, override_billing_interval_count = @override_billing_interval_count,
  override_billing_interval_unit = @override_billing_interval_unit,
  override_pricing_unit_conversion_rate = @override_pricing_unit_conversion_rate, updated_at = @updated_at
WHERE organization_id = @organization_id AND subscription_rate_card_id = @subscription_rate_card_id AND id = @id AND deleted_at IS NULL;

-- name: InsertSubscriptionRatePhase :execrows
INSERT INTO subscription_rate_phases (
  organization_id, id, subscription_rate_card_id, code, position, name, billing_interval_cycle_count,
  override_rate_model, override_properties, override_min_amount, override_billing_interval_count,
  override_billing_interval_unit, override_pricing_unit_conversion_rate, created_at, updated_at
) VALUES (
  @organization_id, @id, @subscription_rate_card_id, @code, @position, @name, @billing_interval_cycle_count,
  @override_rate_model, @override_properties, @override_min_amount, @override_billing_interval_count,
  @override_billing_interval_unit, @override_pricing_unit_conversion_rate, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: ListSubscriptionRatePhases :many
SELECT * FROM subscription_rate_phases
WHERE organization_id = @organization_id AND subscription_rate_card_id = @subscription_rate_card_id AND deleted_at IS NULL
ORDER BY position;

-- name: BillingSegmentOverlaps :one
-- Whether a segment of the card starts at @started_at or overlaps
-- [@started_at, @ended_at).
SELECT EXISTS (
  SELECT 1 FROM billing_segments b
  WHERE b.organization_id = @organization_id AND b.subscription_rate_card_id = @subscription_rate_card_id
    AND (b.started_at = @started_at::timestamptz
         OR (b.started_at < @ended_at::timestamptz AND b.ended_at > @started_at::timestamptz))
)::boolean;

-- name: InsertBillingSegment :execrows
INSERT INTO billing_segments (
  organization_id, id, subscription_id, customer_id, subscription_rate_card_id, invoice_id, rate_card_rate_id,
  rate_phase_id, pricing_unit_id, cycle_started_at, started_at, ended_at, billing_at, rate_model,
  rate_properties, currency, proration_ratio, status, created_at, updated_at
) VALUES (
  @organization_id, @id, @subscription_id, @customer_id, @subscription_rate_card_id, @invoice_id, @rate_card_rate_id,
  @rate_phase_id, @pricing_unit_id, @cycle_started_at, @started_at, @ended_at, @billing_at, @rate_model,
  @rate_properties, @currency, @proration_ratio, @status, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateBillingSegment :execrows
UPDATE billing_segments SET
  invoice_id = @invoice_id, rate_card_rate_id = @rate_card_rate_id, rate_phase_id = @rate_phase_id,
  pricing_unit_id = @pricing_unit_id, cycle_started_at = @cycle_started_at, started_at = @started_at,
  ended_at = @ended_at, billing_at = @billing_at, rate_model = @rate_model, rate_properties = @rate_properties,
  currency = @currency, proration_ratio = @proration_ratio, status = @status, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: ListOverlappingBillingSegments :many
SELECT * FROM billing_segments
WHERE organization_id = @organization_id AND subscription_rate_card_id = @subscription_rate_card_id
  AND started_at < @to_at::timestamptz AND ended_at > @from_at::timestamptz
ORDER BY started_at, id;

-- name: MaxBillingSegmentCycleStartedAt :one
SELECT cycle_started_at FROM billing_segments
WHERE organization_id = @organization_id AND subscription_rate_card_id = @subscription_rate_card_id
ORDER BY cycle_started_at DESC
LIMIT 1;

-- name: ListBillingSegmentsAwaitingInvoicing :many
-- billing_segment.rb awaiting_invoicing: pending or processing, leaving
-- out the segments of metered in-advance rate cards.
SELECT b.* FROM billing_segments b
JOIN subscription_rate_cards rc ON rc.organization_id = b.organization_id AND rc.id = b.subscription_rate_card_id
JOIN rate_cards c ON c.organization_id = rc.organization_id AND c.id = rc.rate_card_id
JOIN products pr ON pr.organization_id = c.organization_id AND pr.id = c.product_id
WHERE b.organization_id = @organization_id AND b.customer_id = @customer_id
  AND b.status IN ('pending', 'processing')
  AND (pr.product_type <> 'metered' OR c.billing_timing <> 'advance')
ORDER BY b.billing_at, b.id;

-- name: ListCustomersAwaitingSegmentInvoicing :many
SELECT DISTINCT b.customer_id FROM billing_segments b
JOIN customers cu ON cu.organization_id = b.organization_id AND cu.id = b.customer_id
JOIN subscription_rate_cards rc ON rc.organization_id = b.organization_id AND rc.id = b.subscription_rate_card_id
JOIN rate_cards c ON c.organization_id = rc.organization_id AND c.id = rc.rate_card_id
JOIN products pr ON pr.organization_id = c.organization_id AND pr.id = c.product_id
WHERE b.organization_id = @organization_id AND cu.deleted_at IS NULL
  AND b.status IN ('pending', 'processing')
  AND (pr.product_type <> 'metered' OR c.billing_timing <> 'advance')
ORDER BY b.customer_id;

-- name: ListGeneratingInvoiceIDsOfBillingSegments :many
SELECT DISTINCT b.invoice_id::uuid FROM billing_segments b
JOIN invoices i ON i.organization_id = b.organization_id AND i.id = b.invoice_id
WHERE b.organization_id = @organization_id AND b.customer_id = @customer_id
  AND b.status = 'done' AND i.status = 'generating'
ORDER BY b.invoice_id::uuid;

-- name: ListBillingSegmentsByInvoice :many
SELECT * FROM billing_segments
WHERE organization_id = @organization_id AND invoice_id = @invoice_id
ORDER BY started_at, id;

-- name: ListBillingSegmentsBySubscription :many
SELECT * FROM billing_segments
WHERE organization_id = @organization_id AND subscription_id = @subscription_id
ORDER BY started_at, id;
