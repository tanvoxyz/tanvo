package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func testAppliedInvoiceCustomSections(t *testing.T, h Harness) {
	e := newEnv(t, h)
	_, be := e.org("Acme")
	p := invoiceFixtureParents(e, be)
	inv1 := invoiceFixtureMinimal(e, p, now)
	inv2 := invoiceFixtureMinimal(e, p, at(time.Minute))
	invoiceFixtureCreate(e, inv1, inv2)
	full := &invoice.AppliedCustomSection{
		ID: core.NewID[core.AppliedInvoiceCustomSectionID](e.ids), OrganizationID: p.org, InvoiceID: inv1.ID,
		Code: "legal", Name: "Legal", DisplayName: new("Legal notice"), Details: new("Paid in 30 days."), CreatedAt: now,
	}
	minimal := &invoice.AppliedCustomSection{
		ID: core.NewID[core.AppliedInvoiceCustomSectionID](e.ids), OrganizationID: p.org, InvoiceID: inv1.ID,
		Code: "bank", Name: "Bank", CreatedAt: now,
	}
	e.tx(func(ctx context.Context, r port.Repos) error {
		for _, a := range []*invoice.AppliedCustomSection{full, minimal} {
			noErr(t, "Create()", r.AppliedInvoiceCustomSections.Create(ctx, a))
		}
		wantErr(t, "Create() same ID", r.AppliedInvoiceCustomSections.Create(ctx, full), core.ErrConflict)
		got, err := r.AppliedInvoiceCustomSections.ListByInvoice(ctx, p.org, inv1.ID)
		noErr(t, "ListByInvoice()", err)
		equal(t, "ListByInvoice()", got, []*invoice.AppliedCustomSection{full, minimal})
		got, err = r.AppliedInvoiceCustomSections.ListByInvoice(ctx, p.org, inv2.ID)
		noErr(t, "ListByInvoice() other invoice", err)
		equal(t, "ListByInvoice() other invoice", len(got), 0)

		other := &invoice.AppliedCustomSection{
			ID: core.NewID[core.AppliedInvoiceCustomSectionID](e.ids), OrganizationID: p.org, InvoiceID: inv2.ID,
			Code: "bank", Name: "Bank", CreatedAt: now,
		}
		noErr(t, "Create() other invoice", r.AppliedInvoiceCustomSections.Create(ctx, other))
		noErr(t, "DeleteByInvoice()", r.AppliedInvoiceCustomSections.DeleteByInvoice(ctx, p.org, inv2.ID))
		got, err = r.AppliedInvoiceCustomSections.ListByInvoice(ctx, p.org, inv2.ID)
		noErr(t, "ListByInvoice() after DeleteByInvoice()", err)
		equal(t, "ListByInvoice() after DeleteByInvoice()", len(got), 0)
		got, err = r.AppliedInvoiceCustomSections.ListByInvoice(ctx, p.org, inv1.ID)
		noErr(t, "ListByInvoice() kept", err)
		equal(t, "ListByInvoice() kept", len(got), 2)

		noErr(t, "DeleteDraft()", r.Invoices.DeleteDraft(ctx, p.org, inv1.ID))
		got, err = r.AppliedInvoiceCustomSections.ListByInvoice(ctx, p.org, inv1.ID)
		noErr(t, "ListByInvoice() of a deleted draft", err)
		equal(t, "ListByInvoice() of a deleted draft", len(got), 0)
		return nil
	})
}
