-- Dunning campaigns with their thresholds.

-- name: CreateDunningCampaign :execrows
INSERT INTO dunning_campaigns (
  organization_id, id, code, name, description, days_between_attempts, max_attempts, bcc_emails,
  deleted_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @code, @name, @description, @days_between_attempts, @max_attempts, @bcc_emails,
  @deleted_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateDunningCampaign :execrows
UPDATE dunning_campaigns SET
  -- The key columns are assigned to themselves first so that the parameters
  -- follow the order of CreateDunningCampaign (see repo_dunning.go).
  organization_id = @organization_id, id = @id, code = @code, name = @name, description = @description,
  days_between_attempts = @days_between_attempts, max_attempts = @max_attempts, bcc_emails = @bcc_emails,
  deleted_at = @deleted_at, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetDunningCampaign :one
SELECT sqlc.embed(c),
  ARRAY(SELECT t.id FROM dunning_campaign_thresholds t
        WHERE t.organization_id = c.organization_id AND t.dunning_campaign_id = c.id ORDER BY t.position)::uuid[] AS threshold_ids,
  ARRAY(SELECT t.currency FROM dunning_campaign_thresholds t
        WHERE t.organization_id = c.organization_id AND t.dunning_campaign_id = c.id ORDER BY t.position)::text[] AS threshold_currencies,
  ARRAY(SELECT t.amount FROM dunning_campaign_thresholds t
        WHERE t.organization_id = c.organization_id AND t.dunning_campaign_id = c.id ORDER BY t.position)::bigint[] AS threshold_amounts
FROM dunning_campaigns c
WHERE c.organization_id = @organization_id AND c.id = @id;

-- name: GetDunningCampaignByCode :one
SELECT sqlc.embed(c),
  ARRAY(SELECT t.id FROM dunning_campaign_thresholds t
        WHERE t.organization_id = c.organization_id AND t.dunning_campaign_id = c.id ORDER BY t.position)::uuid[] AS threshold_ids,
  ARRAY(SELECT t.currency FROM dunning_campaign_thresholds t
        WHERE t.organization_id = c.organization_id AND t.dunning_campaign_id = c.id ORDER BY t.position)::text[] AS threshold_currencies,
  ARRAY(SELECT t.amount FROM dunning_campaign_thresholds t
        WHERE t.organization_id = c.organization_id AND t.dunning_campaign_id = c.id ORDER BY t.position)::bigint[] AS threshold_amounts
FROM dunning_campaigns c
WHERE c.organization_id = @organization_id AND c.code = @code AND c.deleted_at IS NULL;

-- name: ListDunningCampaigns :many
SELECT sqlc.embed(c),
  ARRAY(SELECT t.id FROM dunning_campaign_thresholds t
        WHERE t.organization_id = c.organization_id AND t.dunning_campaign_id = c.id ORDER BY t.position)::uuid[] AS threshold_ids,
  ARRAY(SELECT t.currency FROM dunning_campaign_thresholds t
        WHERE t.organization_id = c.organization_id AND t.dunning_campaign_id = c.id ORDER BY t.position)::text[] AS threshold_currencies,
  ARRAY(SELECT t.amount FROM dunning_campaign_thresholds t
        WHERE t.organization_id = c.organization_id AND t.dunning_campaign_id = c.id ORDER BY t.position)::bigint[] AS threshold_amounts
FROM dunning_campaigns c
WHERE c.organization_id = @organization_id AND c.deleted_at IS NULL
  AND (cardinality(@currencies::text[]) = 0 OR EXISTS (
        SELECT 1 FROM dunning_campaign_thresholds t
        WHERE t.organization_id = c.organization_id AND t.dunning_campaign_id = c.id AND t.currency = ANY(@currencies::text[])))
  AND (sqlc.narg(after)::uuid IS NULL OR c.id > sqlc.narg(after))
ORDER BY c.id
LIMIT @lim;

-- name: DeleteDunningCampaignThresholds :exec
DELETE FROM dunning_campaign_thresholds WHERE organization_id = @organization_id AND dunning_campaign_id = @dunning_campaign_id;

-- name: InsertDunningCampaignThresholds :exec
INSERT INTO dunning_campaign_thresholds (organization_id, id, dunning_campaign_id, currency, amount, position)
SELECT @organization_id, (@ids::uuid[])[i], @dunning_campaign_id, (@currencies::text[])[i], (@amounts::bigint[])[i], i
FROM generate_subscripts(@ids::uuid[], 1) AS i;
