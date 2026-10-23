package porttest

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/export"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// deletedRow is the table and ID of a record deletion.
type deletedRow struct {
	Table string
	ID    uuid.UUID
}

// recordDeletions returns the record deletions of org, in ID order, and
// checks that each is stamped once (deleted_at = created_at = updated_at).
func recordDeletions(e *env, org core.OrganizationID) []*export.RecordDeletion {
	e.t.Helper()
	var out []*export.RecordDeletion
	e.tx(func(_ context.Context, r port.Repos) error {
		out = listAll(e.t, 2, func(page core.PageRequest) (core.Page[*export.RecordDeletion], error) {
			return r.RecordDeletions.List(context.Background(), org, page)
		})
		return nil
	})
	for _, d := range out {
		if !d.DeletedAt.Equal(d.CreatedAt) || !d.DeletedAt.Equal(d.UpdatedAt) {
			e.t.Errorf("record deletion %s: got = deleted %v created %v updated %v, want = one instant", d.RecordID, d.DeletedAt, d.CreatedAt, d.UpdatedAt)
		}
		if d.OrganizationID != org {
			e.t.Errorf("record deletion %s organization: got = %s, want = %s", d.RecordID, d.OrganizationID, org)
		}
	}
	return out
}

// deletedRows returns the tables and IDs of ds, sorted.
func deletedRows(ds []*export.RecordDeletion) []deletedRow {
	out := make([]deletedRow, len(ds))
	for i, d := range ds {
		out[i] = deletedRow{d.RecordTable, d.RecordID}
	}
	return sortedRows(out...)
}

// sortedRows returns rs sorted by table, then ID.
func sortedRows(rs ...deletedRow) []deletedRow {
	slices.SortFunc(rs, func(a, b deletedRow) int {
		return cmp.Or(cmp.Compare(a.Table, b.Table), cmp.Compare(a.ID.String(), b.ID.String()))
	})
	return rs
}

// testRecordDeletion checks the record deletions written as transactions
// that delete tracked rows commit (export.RecordDeletionRepository),
// ported from spec/db/triggers/record_deletions_spec.rb and
// spec/jobs/clock/record_deletions_cleanup_job_spec.rb.
func testRecordDeletion(t *testing.T, h Harness) {
	t.Run("invoice rows", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		p := invoiceFixtureParents(e, a)
		draft := invoiceFixtureMinimal(e, p, now)
		refreshed := invoiceFixtureMinimal(e, p, at(time.Minute))
		invoiceFixtureCreate(e, draft, refreshed)
		fee, refreshedFee := invoiceFixtureFee(e, p, draft.ID, now), invoiceFixtureFee(e, p, refreshed.ID, now)
		subOf := func(inv core.InvoiceID) *invoice.Subscription {
			return &invoice.Subscription{
				ID: core.NewID[core.InvoiceSubscriptionID](e.ids), OrganizationID: p.org, InvoiceID: inv,
				SubscriptionID: p.subscriptions[0], Reason: invoice.InvoicingReasonSubscriptionPeriodic,
				Period: period.Period{From: now, To: at(time.Hour)}, Timestamp: now, CreatedAt: now,
			}
		}
		taxOf := func(inv core.InvoiceID, code string) *invoice.AppliedTax {
			return &invoice.AppliedTax{
				ID: core.NewID[core.InvoiceAppliedTaxID](e.ids), OrganizationID: p.org, InvoiceID: inv, TaxCode: code, TaxName: code,
				TaxRate: money.MustParse("0"), PreciseAmount: money.MustParse("0"), Currency: invoiceEUR, CreatedAt: now,
			}
		}
		sub, refreshedSub := subOf(draft.ID), subOf(refreshed.ID)
		tax, kept, dropped := taxOf(draft.ID, "vat"), taxOf(refreshed.ID, "kept"), taxOf(refreshed.ID, "dropped")
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, f := range []*invoice.Fee{fee, refreshedFee} {
				noErr(t, "Fees.Create()", r.Fees.Create(ctx, f))
			}
			for _, s := range []*invoice.Subscription{sub, refreshedSub} {
				noErr(t, "InvoiceSubscriptions.Create()", r.InvoiceSubscriptions.Create(ctx, s))
			}
			noErr(t, "ReplaceForInvoice()", r.InvoiceAppliedTaxes.ReplaceForInvoice(ctx, p.org, draft.ID, []*invoice.AppliedTax{tax}))
			return r.InvoiceAppliedTaxes.ReplaceForInvoice(ctx, p.org, refreshed.ID, []*invoice.AppliedTax{kept, dropped})
		})
		equal(t, "record deletions after inserts", len(recordDeletions(e, p.org)), 0)

		// A rolled back deletion records nothing
		// (spec/db/triggers/record_deletions_spec.rb, "rollback").
		errRollback := errors.New("rollback")
		err := e.txErr(func(ctx context.Context, r port.Repos) error {
			noErr(t, "DeleteDraft()", r.Invoices.DeleteDraft(ctx, p.org, draft.ID))
			return errRollback
		})
		wantErr(t, "rolled back DeleteDraft()", err, errRollback)
		equal(t, "record deletions after a rollback", len(recordDeletions(e, p.org)), 0)

		// Deleting a draft records its fees, subscriptions and taxes.
		e.tx(func(ctx context.Context, r port.Repos) error {
			return r.Invoices.DeleteDraft(ctx, p.org, draft.ID)
		})
		first := recordDeletions(e, p.org)
		equal(t, "record deletions of a deleted draft", deletedRows(first), sortedRows(
			deletedRow{export.TableFees, fee.ID.UUID()},
			deletedRow{export.TableInvoiceSubscriptions, sub.ID.UUID()},
			deletedRow{export.TableInvoiceAppliedTaxes, tax.ID.UUID()},
		))
		for _, d := range first[1:] {
			if !d.DeletedAt.Equal(first[0].DeletedAt) {
				t.Errorf("record deletion %s instant: got = %v, want = %v (one commit)", d.RecordID, d.DeletedAt, first[0].DeletedAt)
			}
		}

		// A draft refresh deletes and recreates its rows; a row written
		// again with the same ID leaves none
		// (spec/db/triggers/record_deletions_spec.rb, "draft refresh").
		newFee := invoiceFixtureFee(e, p, refreshed.ID, now)
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Fees.DeleteByInvoice()", r.Fees.DeleteByInvoice(ctx, p.org, refreshed.ID))
			noErr(t, "Fees.Create()", r.Fees.Create(ctx, newFee))
			noErr(t, "InvoiceSubscriptions.DeleteByInvoice()", r.InvoiceSubscriptions.DeleteByInvoice(ctx, p.org, refreshed.ID))
			return r.InvoiceAppliedTaxes.ReplaceForInvoice(ctx, p.org, refreshed.ID, []*invoice.AppliedTax{kept})
		})
		all := recordDeletions(e, p.org)
		second := slices.DeleteFunc(slices.Clone(all), func(d *export.RecordDeletion) bool {
			return slices.ContainsFunc(first, func(f *export.RecordDeletion) bool { return f.ID == d.ID })
		})
		equal(t, "record deletions of a refresh", deletedRows(second), sortedRows(
			deletedRow{export.TableFees, refreshedFee.ID.UUID()},
			deletedRow{export.TableInvoiceSubscriptions, refreshedSub.ID.UUID()},
			deletedRow{export.TableInvoiceAppliedTaxes, dropped.ID.UUID()},
		))
		for _, d := range second {
			if d.DeletedAt.Before(first[0].DeletedAt) {
				t.Errorf("record deletion %s of a later commit: got = %v, want >= %v", d.RecordID, d.DeletedAt, first[0].DeletedAt)
			}
		}
		equal(t, "record deletions of another organization", len(recordDeletions(e, b.OrganizationID)), 0)
	})

	t.Run("credit note taxes", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		_, be := e.org("Acme")
		p := creditNoteFixtureParents(e, be)
		taxOf := func(code string) creditnote.AppliedTax {
			return creditnote.AppliedTax{
				ID: core.NewID[core.CreditNoteAppliedTaxID](e.ids), TaxCode: code, TaxName: code,
				TaxRate: money.MustParse("20"), PreciseAmount: money.MustParse("2"),
			}
		}
		updated, deleted := creditNoteFixtureMinimal(e, p, now), creditNoteFixtureMinimal(e, p, at(time.Minute))
		old, kept := taxOf("old"), taxOf("kept")
		updated.AppliedTaxes = []creditnote.AppliedTax{old, kept}
		gone := taxOf("gone")
		deleted.AppliedTaxes = []creditnote.AppliedTax{gone}
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "CreditNotes.Create()", r.CreditNotes.Create(ctx, updated))
			return r.CreditNotes.Create(ctx, deleted)
		})
		e.tx(func(ctx context.Context, r port.Repos) error {
			updated.AppliedTaxes = []creditnote.AppliedTax{kept, taxOf("new")}
			noErr(t, "CreditNotes.Update()", r.CreditNotes.Update(ctx, updated))
			return r.CreditNotes.Delete(ctx, p.org, deleted.ID)
		})
		equal(t, "record deletions of credit notes", deletedRows(recordDeletions(e, p.org)), sortedRows(
			deletedRow{export.TableCreditNoteAppliedTaxes, old.ID.UUID()},
			deletedRow{export.TableCreditNoteAppliedTaxes, gone.ID.UUID()},
		))
	})

	// A transaction that deletes first and commits last is stamped last:
	// the instant is the commit's, not the statement's
	// (spec/db/triggers/record_deletions_spec.rb, "concurrent").
	t.Run("late committer", func(t *testing.T) {
		if !h.Concurrent {
			t.Skip("transactions run one after the other")
		}
		t.Parallel()
		e := newEnv(t, h)
		_, be := e.org("Acme")
		p := invoiceFixtureParents(e, be)
		early, late := invoiceFixtureMinimal(e, p, now), invoiceFixtureMinimal(e, p, at(time.Minute))
		invoiceFixtureCreate(e, early, late)
		earlyFee, lateFee := invoiceFixtureFee(e, p, early.ID, now), invoiceFixtureFee(e, p, late.ID, now)
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Fees.Create()", r.Fees.Create(ctx, earlyFee))
			return r.Fees.Create(ctx, lateFee)
		})
		deleted, release := make(chan struct{}), make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() {
			err := e.tr.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
				if err := r.Fees.DeleteByInvoice(ctx, p.org, early.ID); err != nil {
					return err
				}
				close(deleted)
				<-release
				return nil
			})
			if err != nil {
				t.Errorf("first InTx() error got = %v, want = nil", err)
			}
		})
		<-deleted
		e.tx(func(ctx context.Context, r port.Repos) error {
			return r.Fees.DeleteByInvoice(ctx, p.org, late.ID)
		})
		close(release)
		wg.Wait()
		stamps := map[uuid.UUID]time.Time{}
		for _, d := range recordDeletions(e, p.org) {
			stamps[d.RecordID] = d.DeletedAt
		}
		equal(t, "record deletions", len(stamps), 2)
		if first, second := stamps[earlyFee.ID.UUID()], stamps[lateFee.ID.UUID()]; !first.After(second) {
			t.Errorf("late committer instant: got = %v, want > %v", first, second)
		}
	})

	// source: spec/jobs/clock/record_deletions_cleanup_job_spec.rb
	t.Run("delete expired", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		pa, pb := invoiceFixtureParents(e, a), invoiceFixtureParents(e, b)
		deleteFees := func(p invoiceParents, n int) {
			inv := invoiceFixtureMinimal(e, p, now)
			invoiceFixtureCreate(e, inv)
			e.tx(func(ctx context.Context, r port.Repos) error {
				for range n {
					noErr(t, "Fees.Create()", r.Fees.Create(ctx, invoiceFixtureFee(e, p, inv.ID, now)))
				}
				return nil
			})
			e.tx(func(ctx context.Context, r port.Repos) error {
				return r.Fees.DeleteByInvoice(ctx, p.org, inv.ID)
			})
		}
		deleteFees(pa, 5)
		deleteFees(pb, 1)
		stamped := recordDeletions(e, pa.org)[0].DeletedAt
		e.tx(func(ctx context.Context, r port.Repos) error {
			// Tombstones not older than the cutoff are kept.
			n, err := r.RecordDeletions.DeleteExpired(ctx, pa.org, stamped, 10)
			noErr(t, "DeleteExpired() at the instant", err)
			equal(t, "DeleteExpired() at the instant", n, 0)
			// Older ones go, up to the limit, in this organization only.
			cutoff := stamped.Add(time.Hour)
			n, err = r.RecordDeletions.DeleteExpired(ctx, pa.org, cutoff, 3)
			noErr(t, "DeleteExpired() limited", err)
			equal(t, "DeleteExpired() limited", n, 3)
			n, err = r.RecordDeletions.DeleteExpired(ctx, pa.org, cutoff, 3)
			noErr(t, "DeleteExpired() rest", err)
			equal(t, "DeleteExpired() rest", n, 2)
			n, err = r.RecordDeletions.DeleteExpired(ctx, pa.org, cutoff, 3)
			noErr(t, "DeleteExpired() none left", err)
			equal(t, "DeleteExpired() none left", n, 0)
			return nil
		})
		equal(t, "record deletions after the cleanup", len(recordDeletions(e, pa.org)), 0)
		equal(t, "record deletions of another organization after the cleanup", len(recordDeletions(e, pb.org)), 1)
	})
}
