package export

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Tables whose deleted rows leave a [RecordDeletion]: the rows a draft
// invoice refresh or a credit note recomputation replaces, which an
// incremental export would otherwise keep (lago RecordDeletion
// TRACKED_TABLES, app/models/record_deletion.rb:5). Fee taxes are a column
// of their fee, so a deleted fee covers them.
const (
	// TableFees is the table of fees.
	TableFees = "fees"
	// TableInvoiceSubscriptions is the table of invoice subscriptions.
	TableInvoiceSubscriptions = "invoice_subscriptions"
	// TableInvoiceAppliedTaxes is the table of invoice applied taxes.
	TableInvoiceAppliedTaxes = "invoice_applied_taxes"
	// TableCreditNoteAppliedTaxes is the table of credit note applied
	// taxes.
	TableCreditNoteAppliedTaxes = "credit_note_applied_taxes"
)

// TrackedTables returns the tables whose deletions are recorded.
func TrackedTables() []string {
	return []string{TableFees, TableInvoiceSubscriptions, TableInvoiceAppliedTaxes, TableCreditNoteAppliedTaxes}
}

// RecordDeletion is the tombstone of a deleted row of a tracked table, for
// the downstream sync of the export views (parity §1.49). It is written by
// the store only, as the deleting transaction commits: a row deleted and
// inserted again with the same ID in one transaction leaves none, and the
// instants are taken at the commit, so an export cursor on UpdatedAt that
// passed a tombstone never misses an earlier one.
type RecordDeletion struct {
	// ID identifies the tombstone.
	ID core.RecordDeletionID
	// OrganizationID is the organization of the deleted row.
	OrganizationID core.OrganizationID
	// RecordTable is the tracked table of the row.
	RecordTable string
	// RecordID is the ID of the deleted row.
	RecordID uuid.UUID
	// DeletedAt is the instant of the deletion's commit.
	DeletedAt time.Time
	// CreatedAt is DeletedAt.
	CreatedAt time.Time
	// UpdatedAt is DeletedAt.
	UpdatedAt time.Time
}

// RecordDeletionBatch is the number of record deletions one cleanup
// statement removes (lago RecordDeletionsCleanupJob BATCH_SIZE).
const RecordDeletionBatch = 10_000

// RecordDeletionCutoff returns the instant before which the record
// deletions are expired at now (lago Clock::RecordDeletionsCleanupJob
// RETENTION_PERIOD): one calendar month earlier in UTC, the day
// clamped to the end of a shorter month, as Rails' 1.month.ago.
func RecordDeletionCutoff(now time.Time) time.Time {
	now = now.UTC()
	y, m, d := now.Date()
	first := time.Date(y, m-1, 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	h, mi, s := now.Clock()
	return time.Date(first.Year(), first.Month(), min(d, last), h, mi, s, now.Nanosecond(), time.UTC)
}

// RecordDeletionRepository reads and expires record deletions.
type RecordDeletionRepository interface {
	// List returns the record deletions of org in ID order (UUIDv7 taken
	// at the commit).
	List(ctx context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*RecordDeletion], error)
	// DeleteExpired removes up to limit record deletions of org deleted
	// before cutoff and returns their number.
	DeleteExpired(ctx context.Context, org core.OrganizationID, cutoff time.Time, limit int) (int, error)
}
