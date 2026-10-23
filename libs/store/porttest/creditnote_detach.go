package porttest

import (
	"context"
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// testCreditNoteDetachFees checks CreditNotes.DetachFees: the items of the
// invoice's credit notes let go of their fees, which can then be deleted,
// and move to new fees with Update; a fee still referenced cannot be
// deleted.
//
// source: app/services/invoices/refresh_draft_service.rb:124-125,
// app/services/credit_notes/refresh_draft_service.rb:20-29
func testCreditNoteDetachFees(t *testing.T, h Harness) {
	e := newEnv(t, h)
	_, be := e.org("Acme")
	otherOrg, _ := e.org("Other")
	p := creditNoteFixtureParents(e, be)
	newFee := func(inv core.InvoiceID) *invoice.Fee {
		return &invoice.Fee{
			ID: core.NewID[core.FeeID](e.ids), OrganizationID: p.org, InvoiceID: inv, Type: core.FeeTypeAddOn,
			Amount: 1000, Currency: creditNoteEUR, CreatedAt: now, UpdatedAt: now,
		}
	}
	otherFee, regenerated := newFee(p.invoices[1]), newFee(p.invoices[0])
	refreshed := creditNoteFixtureMinimal(e, p, now)
	refreshed.Items = []creditnote.Item{creditNoteFixtureItem(e, p.fees[0], 10), creditNoteFixtureItem(e, p.fees[1], 20)}
	other := creditNoteFixtureMinimal(e, p, at(1))
	other.InvoiceID = p.invoices[1]
	other.Items = []creditnote.Item{creditNoteFixtureItem(e, otherFee.ID, 30)}
	e.tx(func(ctx context.Context, r port.Repos) error {
		if err := r.Fees.Create(ctx, otherFee); err != nil {
			return err
		}
		for _, cn := range []*creditnote.CreditNote{refreshed, other} {
			if err := r.CreditNotes.Create(ctx, cn); err != nil {
				return err
			}
		}
		// Another organization's call changes nothing.
		return r.CreditNotes.DetachFees(ctx, otherOrg.ID, p.invoices[0])
	})

	err := e.txErr(func(ctx context.Context, r port.Repos) error {
		return r.Fees.DeleteByInvoice(ctx, p.org, p.invoices[0])
	})
	if !errors.Is(err, core.ErrConflict) {
		t.Errorf("DeleteByInvoice() with referenced fees error = %v, want = %v", err, core.ErrConflict)
	}

	e.tx(func(ctx context.Context, r port.Repos) error {
		if err := r.CreditNotes.DetachFees(ctx, p.org, p.invoices[0]); err != nil {
			return err
		}
		got, err := r.CreditNotes.Get(ctx, p.org, refreshed.ID)
		if err != nil {
			return err
		}
		for i, it := range got.Items {
			if !it.FeeID.IsZero() || it.ID != refreshed.Items[i].ID || it.Amount != refreshed.Items[i].Amount {
				t.Errorf("items[%d] = %s/%s/%d, want = %s/zero/%d", i, it.ID, it.FeeID, it.Amount, refreshed.Items[i].ID, refreshed.Items[i].Amount)
			}
		}
		kept, err := r.CreditNotes.Get(ctx, p.org, other.ID)
		if err != nil {
			return err
		}
		if kept.Items[0].FeeID != otherFee.ID {
			t.Errorf("other invoice's item fee = %s, want = %s", kept.Items[0].FeeID, otherFee.ID)
		}
		credited, err := r.CreditNotes.CreditedByFee(ctx, p.org, p.fees[:])
		if err != nil {
			return err
		}
		if len(credited) != 0 {
			t.Errorf("CreditedByFee() = %v, want = none", credited)
		}
		if err := r.Fees.DeleteByInvoice(ctx, p.org, p.invoices[0]); err != nil {
			return err
		}
		if err := r.Fees.Create(ctx, regenerated); err != nil {
			return err
		}
		for i := range got.Items {
			got.Items[i].FeeID = regenerated.ID
		}
		if err := r.CreditNotes.Update(ctx, got); err != nil {
			return err
		}
		back, err := r.CreditNotes.Get(ctx, p.org, refreshed.ID)
		if err != nil {
			return err
		}
		for i, it := range back.Items {
			if it.FeeID != regenerated.ID {
				t.Errorf("moved items[%d].FeeID = %s, want = %s", i, it.FeeID, regenerated.ID)
			}
		}
		return nil
	})
}
