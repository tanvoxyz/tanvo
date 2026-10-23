package creditnotes_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
)

func TestGetAndList(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	final := f.h.StoredCreditNote(f.inv, 10)
	draft := f.h.StoredCreditNote(f.inv, 10, func(cn *creditnote.CreditNote) { cn.Status = creditnote.StatusDraft })

	// source: spec/requests/api/v1/credit_notes_controller_spec.rb:32
	got, err := f.svc.Get(ctx, f.h.OrgID(), final.ID)
	must(t, err)
	eq(t, "id", got.ID, final.ID)

	// source: spec/requests/api/v1/credit_notes_controller_spec.rb:87
	if _, err := f.svc.Get(ctx, f.h.OrgID(), draft.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("draft: got = %v, want = not found", err)
	}

	// source: spec/requests/api/v1/credit_notes_controller_spec.rb:366
	page, err := f.svc.List(ctx, f.h.OrgID(), creditnote.Filter{CustomerID: f.cust.ID}, core.PageRequest{Size: 10})
	must(t, err)
	eq(t, "listed", len(page.Items), 1)
	eq(t, "listed id", page.Items[0].ID, final.ID)
}

func TestInvoiceCurrency(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	c, err := f.svc.InvoiceCurrency(ctx, f.h.OrgID(), f.inv.ID)
	must(t, err)
	eq(t, "currency", c, f.inv.Currency)

	// source: spec/requests/api/v1/credit_notes_controller_spec.rb:455
	f.inv.Status = invoice.StatusGenerating
	f.h.SaveInvoice(f.inv)
	if _, err := f.svc.InvoiceCurrency(ctx, f.h.OrgID(), f.inv.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("generating: got = %v, want = not found", err)
	}
}

func TestEstimateMaximums(t *testing.T) {
	ctx := context.Background()

	// The fixture's invoice totals 24 with 6 paid: 18 is due.
	// source: spec/requests/api/v1/credit_notes_controller_spec.rb:756
	f := newFixture(t)
	est, err := f.svc.EstimateMaximums(ctx, f.h.OrgID(), f.inv.ID, []creditnotes.ItemInput{item(f.fees[0], "10")})
	must(t, err)
	eq(t, "max creditable", est.MaxCreditable, 12)
	eq(t, "max refundable", est.MaxRefundable, 6)
	eq(t, "max offsettable", est.MaxOffsettable, 12)
	eq(t, "stored", len(f.h.CreditNotes(f.inv.ID)), 0)

	// The offsettable amount is the due amount when it is lower.
	est, err = f.svc.EstimateMaximums(ctx, f.h.OrgID(), f.inv.ID, []creditnotes.ItemInput{item(f.fees[0], "10"), item(f.fees[1], "10")})
	must(t, err)
	eq(t, "max creditable", est.MaxCreditable, 24)
	eq(t, "max offsettable", est.MaxOffsettable, 18)

	// source: spec/requests/api/v1/credit_notes_controller_spec.rb:777
	f.inv.Status = invoice.StatusGenerating
	f.h.SaveInvoice(f.inv)
	if _, err := f.svc.EstimateMaximums(ctx, f.h.OrgID(), f.inv.ID, []creditnotes.ItemInput{item(f.fees[0], "10")}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("generating: got = %v, want = not found", err)
	}
}

func TestDownload(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	cn := f.h.StoredCreditNote(f.inv, 10)

	// source: spec/requests/api/v1/credit_notes_controller_spec.rb:250
	_, ready, err := f.svc.Download(ctx, f.h.OrgID(), cn.ID)
	must(t, err)
	eq(t, "ready", ready, false)
	eq(t, "document job", contains(f.h.JobKinds(), kernel.JobGenerateCreditNoteDocument), true)

	// source: spec/requests/api/v1/credit_notes_controller_spec.rb:260
	withFile := f.h.StoredCreditNote(f.inv, 10, func(cn *creditnote.CreditNote) { cn.File = "credit_notes/cn.pdf" })
	got, ready, err := f.svc.Download(ctx, f.h.OrgID(), withFile.ID)
	must(t, err)
	eq(t, "ready", ready, true)
	eq(t, "file", got.File, withFile.File)

	// source: spec/requests/api/v1/credit_notes_controller_spec.rb:280
	draft := f.h.StoredCreditNote(f.inv, 10, func(cn *creditnote.CreditNote) { cn.Status = creditnote.StatusDraft })
	if _, _, err := f.svc.Download(ctx, f.h.OrgID(), draft.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("draft: got = %v, want = not found", err)
	}
}
