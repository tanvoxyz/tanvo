-- Data exports and their parts.

-- name: CreateDataExport :execrows
INSERT INTO data_exports (
  organization_id, id, membership_id, format, resource_type, resource_query, status, file,
  started_at, completed_at, expires_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @membership_id, @format, @resource_type, @resource_query, @status, @file,
  @started_at, @completed_at, @expires_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateDataExport :execrows
UPDATE data_exports SET
  organization_id = @organization_id, id = @id, membership_id = @membership_id, format = @format,
  resource_type = @resource_type, resource_query = @resource_query, status = @status, file = @file,
  started_at = @started_at, completed_at = @completed_at, expires_at = @expires_at,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetDataExport :one
SELECT * FROM data_exports WHERE organization_id = @organization_id AND id = @id;

-- name: ListDataExportsByMembership :many
SELECT * FROM data_exports
WHERE organization_id = @organization_id AND membership_id = @membership_id
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;

-- ListExpiredDataExports is cross-organization: the file cleanup job scans
-- every organization.
-- name: ListExpiredDataExports :many
SELECT * FROM data_exports
WHERE status = 'completed' AND file IS NOT NULL AND expires_at < @now
ORDER BY id
LIMIT @lim;

-- name: DeleteDataExport :execrows
DELETE FROM data_exports WHERE organization_id = @organization_id AND id = @id;

-- name: CreateDataExportPart :execrows
INSERT INTO data_export_parts (
  organization_id, id, data_export_id, index, object_ids, completed, csv_lines, created_at, updated_at
) VALUES (
  @organization_id, @id, @data_export_id, @index, @object_ids, @completed, @csv_lines, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateDataExportPart :execrows
UPDATE data_export_parts SET
  organization_id = @organization_id, id = @id, data_export_id = @data_export_id, index = @index,
  object_ids = @object_ids, completed = @completed, csv_lines = @csv_lines,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetDataExportPart :one
SELECT * FROM data_export_parts WHERE organization_id = @organization_id AND id = @id;

-- name: ListDataExportPartsByExport :many
SELECT * FROM data_export_parts
WHERE organization_id = @organization_id AND data_export_id = @data_export_id
ORDER BY index;

-- name: NextUnprocessedDataExportPart :one
SELECT * FROM data_export_parts
WHERE organization_id = @organization_id AND data_export_id = @data_export_id AND NOT completed
ORDER BY index
LIMIT 1;
