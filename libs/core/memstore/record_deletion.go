package memstore

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/export"
)

// recordDeletionRepo implements export.RecordDeletionRepository.
type recordDeletionRepo struct{ d *exportData }

var _ export.RecordDeletionRepository = recordDeletionRepo{}

func (r recordDeletionRepo) List(_ context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*export.RecordDeletion], error) {
	rows := r.d.deletions.find(func(x *export.RecordDeletion) bool { return x.OrganizationID == org })
	return paginate(rows, func(x *export.RecordDeletion) core.RecordDeletionID { return x.ID }, page)
}

func (r recordDeletionRepo) DeleteExpired(_ context.Context, org core.OrganizationID, cutoff time.Time, limit int) (int, error) {
	rows := r.d.deletions.find(func(x *export.RecordDeletion) bool {
		return x.OrganizationID == org && x.DeletedAt.Before(cutoff)
	})
	n := 0
	for _, x := range rows[:min(limit, len(rows))] {
		r.d.deletions.del(x.ID)
		n++
	}
	return n, nil
}

// trackedRow is a row of a tracked table (export.TrackedTables).
type trackedRow struct {
	table string
	org   core.OrganizationID
	id    uuid.UUID
}

// trackedRows returns the rows of the tracked tables of st.
func trackedRows(st *state) map[trackedRow]bool {
	out := map[trackedRow]bool{}
	for _, x := range st.invoice.fees.rows {
		out[trackedRow{export.TableFees, x.OrganizationID, x.ID.UUID()}] = true
	}
	for _, x := range st.invoice.subscriptions.rows {
		out[trackedRow{export.TableInvoiceSubscriptions, x.OrganizationID, x.ID.UUID()}] = true
	}
	for _, x := range st.invoice.appliedTaxes.rows {
		out[trackedRow{export.TableInvoiceAppliedTaxes, x.OrganizationID, x.ID.UUID()}] = true
	}
	for _, cn := range st.creditNote.creditNotes.rows {
		for _, t := range cn.AppliedTaxes {
			out[trackedRow{export.TableCreditNoteAppliedTaxes, cn.OrganizationID, t.ID.UUID()}] = true
		}
	}
	return out
}

// recordDeletions writes to after the record deletions of the tracked rows
// of before that after no longer holds, as the PostgreSQL store does when
// a transaction commits. A row inserted and deleted within one
// transaction leaves none here; the PostgreSQL store, like lago-api's
// triggers, may record one, which no reader can tell from a row it never
// saw.
func recordDeletions(before, after *state) {
	gone := trackedRows(before)
	for k := range trackedRows(after) {
		delete(gone, k)
	}
	if len(gone) == 0 {
		return
	}
	now := time.Now().UTC()
	for k := range gone {
		id := core.NewID[core.RecordDeletionID](core.UUIDv7Generator{})
		after.export.deletions.put(id, export.RecordDeletion{
			ID: id, OrganizationID: k.org, RecordTable: k.table, RecordID: k.id,
			DeletedAt: now, CreatedAt: now, UpdatedAt: now,
		})
	}
}
