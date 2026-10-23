-- Record deletions (parity §1.49). The tombstones themselves are inserted
-- by txState.recordDeletions before the commit.

-- name: ListRecordDeletions :many
SELECT * FROM record_deletions
WHERE organization_id = @organization_id
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;

-- name: DeleteExpiredRecordDeletions :execrows
DELETE FROM record_deletions d
WHERE d.organization_id = @organization_id::uuid AND d.id IN (
  SELECT x.id FROM record_deletions x
  WHERE x.organization_id = @organization_id::uuid AND x.deleted_at < @cutoff::timestamptz
  LIMIT @lim::int
);

-- name: ListInvoiceTrackedRows :many
-- The tracked rows an invoice delete may remove, its own and those of its
-- credit notes (deleted with it).
SELECT 'fees'::text AS record_table, id FROM fees WHERE organization_id = @organization_id::uuid AND invoice_id = @invoice_id::uuid
UNION ALL
SELECT 'invoice_subscriptions'::text, id FROM invoice_subscriptions WHERE organization_id = @organization_id::uuid AND invoice_id = @invoice_id::uuid
UNION ALL
SELECT 'invoice_applied_taxes'::text, id FROM invoice_applied_taxes WHERE organization_id = @organization_id::uuid AND invoice_id = @invoice_id::uuid
UNION ALL
SELECT 'credit_note_applied_taxes'::text, t.id FROM credit_note_applied_taxes t
JOIN credit_notes cn ON cn.organization_id = t.organization_id AND cn.id = t.credit_note_id
WHERE t.organization_id = @organization_id::uuid AND cn.invoice_id = @invoice_id::uuid;

-- name: ListCreditNoteTrackedRows :many
SELECT id FROM credit_note_applied_taxes WHERE organization_id = @organization_id AND credit_note_id = @credit_note_id;
