package store

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/export"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// Record deletions (parity §1.49). lago-api writes them with deferred
// AFTER DELETE triggers (db/migrate/20260903164414_add_record_deletion_triggers.rb),
// which fire as the transaction commits. The store does the same in Go: the
// repositories that delete tracked rows note the rows they may remove, and
// [txState.recordDeletions] runs as the last statement before the commit,
// writing a tombstone for each noted row that is gone, stamped with
// clock_timestamp(). A row deleted and inserted again with the same ID
// leaves none, and the rows removed by ON DELETE CASCADE are noted with
// their parent.

// trackedRow is a row of a tracked table (export.TrackedTables).
type trackedRow struct {
	table string
	id    uuid.UUID
}

// noteDeleted notes rows of table as possibly deleted by the transaction.
func (t *txState) noteDeleted(org core.OrganizationID, table string, ids ...uuid.UUID) {
	if t.deleted == nil {
		t.deleted = map[trackedRow]core.OrganizationID{}
	}
	for _, id := range ids {
		t.deleted[trackedRow{table, id}] = org
	}
}

// trackInvoice notes the tracked rows of invoice inv (and of its credit
// notes) before a statement that may delete them. The rows inserted later
// in the transaction need no tombstone: they were never visible.
func (t *txState) trackInvoice(ctx context.Context, q *db.Queries, org core.OrganizationID, inv core.InvoiceID) error {
	if t.trackedInvoices[inv] {
		return nil
	}
	rows, err := q.ListInvoiceTrackedRows(ctx, db.ListInvoiceTrackedRowsParams{OrganizationID: org.UUID(), InvoiceID: inv.UUID()})
	if err != nil {
		return fmt.Errorf("list tracked rows of invoice %s: %w", inv, mapErr(err))
	}
	for _, r := range rows {
		t.noteDeleted(org, r.RecordTable, r.ID)
	}
	if t.trackedInvoices == nil {
		t.trackedInvoices = map[core.InvoiceID]bool{}
	}
	t.trackedInvoices[inv] = true
	return nil
}

// trackCreditNote notes the applied taxes of credit note cn before a
// statement that may delete them.
func (t *txState) trackCreditNote(ctx context.Context, q *db.Queries, org core.OrganizationID, cn core.CreditNoteID) error {
	if t.trackedCreditNotes[cn] {
		return nil
	}
	ids, err := q.ListCreditNoteTrackedRows(ctx, db.ListCreditNoteTrackedRowsParams{OrganizationID: org.UUID(), CreditNoteID: cn.UUID()})
	if err != nil {
		return fmt.Errorf("list tracked rows of credit note %s: %w", cn, mapErr(err))
	}
	t.noteDeleted(org, export.TableCreditNoteAppliedTaxes, ids...)
	if t.trackedCreditNotes == nil {
		t.trackedCreditNotes = map[core.CreditNoteID]bool{}
	}
	t.trackedCreditNotes[cn] = true
	return nil
}

// recordDeletionsSQL writes the tombstones of the noted rows that no longer
// exist, all stamped with one clock_timestamp().
const recordDeletionsSQL = `
INSERT INTO record_deletions (organization_id, id, record_table, record_id, deleted_at, created_at, updated_at)
SELECT c.organization_id, c.id, c.record_table, c.record_id, n.at, n.at, n.at
FROM (SELECT clock_timestamp() AS at) n,
  unnest($1::uuid[], $2::uuid[], $3::text[], $4::uuid[]) AS c(organization_id, id, record_table, record_id)
WHERE NOT (c.record_table = 'fees' AND EXISTS
    (SELECT 1 FROM fees x WHERE x.organization_id = c.organization_id AND x.id = c.record_id))
  AND NOT (c.record_table = 'invoice_subscriptions' AND EXISTS
    (SELECT 1 FROM invoice_subscriptions x WHERE x.organization_id = c.organization_id AND x.id = c.record_id))
  AND NOT (c.record_table = 'invoice_applied_taxes' AND EXISTS
    (SELECT 1 FROM invoice_applied_taxes x WHERE x.organization_id = c.organization_id AND x.id = c.record_id))
  AND NOT (c.record_table = 'credit_note_applied_taxes' AND EXISTS
    (SELECT 1 FROM credit_note_applied_taxes x WHERE x.organization_id = c.organization_id AND x.id = c.record_id))`

// recordDeletions writes the record deletions of the transaction; it is
// the last statement before the commit.
func (t *txState) recordDeletions(ctx context.Context) error {
	if len(t.deleted) == 0 {
		return nil
	}
	n := len(t.deleted)
	orgs, ids, tables, records := make([]uuid.UUID, 0, n), make([]uuid.UUID, 0, n), make([]string, 0, n), make([]uuid.UUID, 0, n)
	for r, org := range t.deleted {
		orgs = append(orgs, org.UUID())
		ids = append(ids, core.UUIDv7Generator{}.NewUUID())
		tables = append(tables, r.table)
		records = append(records, r.id)
	}
	if err := t.setScope(ctx, scopeAll); err != nil {
		return err
	}
	if _, err := t.tx.Exec(ctx, recordDeletionsSQL, orgs, ids, tables, records); err != nil {
		return fmt.Errorf("store: record deletions: %w", mapErr(err))
	}
	return nil
}

// recordDeletionRepo implements export.RecordDeletionRepository.
type recordDeletionRepo struct{ t *txState }

var _ export.RecordDeletionRepository = recordDeletionRepo{}

func (r recordDeletionRepo) List(ctx context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*export.RecordDeletion], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*export.RecordDeletion]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*export.RecordDeletion]{}, err
	}
	rows, err := q.ListRecordDeletions(ctx, db.ListRecordDeletionsParams{OrganizationID: org.UUID(), After: after, Lim: lim})
	if err != nil {
		return core.Page[*export.RecordDeletion]{}, fmt.Errorf("list record deletions: %w", mapErr(err))
	}
	return pageOf(rows, page, func(x db.RecordDeletion) (*export.RecordDeletion, error) {
		return &export.RecordDeletion{
			ID:             idOf[core.RecordDeletionID](x.ID),
			OrganizationID: idOf[core.OrganizationID](x.OrganizationID),
			RecordTable:    x.RecordTable,
			RecordID:       x.RecordID,
			DeletedAt:      utc(x.DeletedAt),
			CreatedAt:      utc(x.CreatedAt),
			UpdatedAt:      utc(x.UpdatedAt),
		}, nil
	}, func(x *export.RecordDeletion) string { return x.ID.String() })
}

func (r recordDeletionRepo) DeleteExpired(ctx context.Context, org core.OrganizationID, cutoff time.Time, limit int) (int, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	n, err := q.DeleteExpiredRecordDeletions(ctx, db.DeleteExpiredRecordDeletionsParams{
		OrganizationID: org.UUID(), Cutoff: cutoff.UTC(), Lim: int32(min(limit, math.MaxInt32)), //nolint:gosec // bounded
	})
	if err != nil {
		return 0, fmt.Errorf("delete expired record deletions: %w", mapErr(err))
	}
	return int(n), nil
}
