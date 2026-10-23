package deduct_test

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestApplyCreditNotes(t *testing.T) {
	tests := []struct {
		name        string
		total       int64
		preview     bool
		wantCredits []int64
		wantBalance [3]int64
		wantStatus  creditnote.CreditStatus
		wantFees    [2]string
	}{
		// source: spec/services/credits/credit_note_service_spec.rb:87
		// source: spec/services/credits/credit_note_service_spec.rb:117
		{
			name: "two credit notes", total: 100, wantCredits: []int64{20, 50},
			wantBalance: [3]int64{0, 0, 30}, wantStatus: creditnote.CreditStatusConsumed, wantFees: [2]string{"42", "28"},
		},
		// source: spec/services/credits/credit_note_service_spec.rb:124
		{
			name: "preview", total: 100, preview: true, wantCredits: []int64{20, 50},
			wantBalance: [3]int64{20, 50, 30}, wantStatus: creditnote.CreditStatusAvailable, wantFees: [2]string{"0", "0"},
		},
		// source: spec/services/credits/credit_note_service_spec.rb:132
		{
			name: "zero invoice", total: 0,
			wantBalance: [3]int64{20, 50, 30}, wantStatus: creditnote.CreditStatusAvailable, wantFees: [2]string{"0", "0"},
		},
		// source: spec/services/credits/credit_note_service_spec.rb:157
		{
			name: "other currency", total: 100, wantCredits: []int64{20, 50},
			wantBalance: [3]int64{0, 0, 30}, wantStatus: creditnote.CreditStatusConsumed, wantFees: [2]string{"42", "28"},
		},
		// source: spec/services/credits/credit_note_service_spec.rb:170
		{
			name: "credit above the invoice", total: 10, wantCredits: []int64{10},
			wantBalance: [3]int64{10, 50, 30}, wantStatus: creditnote.CreditStatusAvailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			h := f.h
			src := h.StoredInvoice(f.cust, func(inv *invoice.Invoice) { inv.Status = invoice.StatusFinalized })
			cn1 := h.StoredCreditNote(src, 20)
			h.Clock.Advance(time.Minute)
			cn2 := h.StoredCreditNote(src, 50)
			h.Clock.Advance(time.Minute)
			// lago has a third note only in the currency case; it is in
			// another currency in every case here.
			cn3 := h.StoredCreditNote(src, 30, func(c *creditnote.CreditNote) { c.Currency = money.MustCurrency("EUR") })
			inv := h.StoredInvoice(f.cust)
			f1 := h.StoredFee(inv, 60)
			f2 := h.StoredFee(inv, 40)
			inv.SubTotalExcludingTaxesAmount = tt.total
			out, fees, err := f.after(inv, deduct.Options{Preview: tt.preview})
			mustOK(t, err)
			if len(out.Credits) != len(tt.wantCredits) {
				t.Fatalf("credits: got = %d, want = %d", len(out.Credits), len(tt.wantCredits))
			}
			var sum int64
			for i, c := range out.Credits {
				eq(t, "credit amount", c.Amount, tt.wantCredits[i])
				eq(t, "before taxes", c.BeforeTaxes, false)
				eq(t, "credit invoice", c.InvoiceID, inv.ID)
				sum += c.Amount
			}
			if len(out.Credits) > 0 {
				eq(t, "first credit note", out.Credits[0].CreditNoteID, cn1.ID)
			}
			eq(t, "credit notes amount", out.CreditNotes, sum)
			eq(t, "invoice credit notes amount", inv.CreditNotesAmount, sum)
			eq(t, "total", inv.TotalAmount, tt.total-sum)
			stored := 0
			if !tt.preview {
				stored = len(tt.wantCredits)
			}
			eq(t, "stored credits", len(h.InvoiceCredits(inv.ID)), stored)
			for i, id := range []core.CreditNoteID{cn1.ID, cn2.ID, cn3.ID} {
				eq(t, "balance", h.ReloadCreditNote(id).BalanceAmount, tt.wantBalance[i])
			}
			eq(t, "first credit status", h.ReloadCreditNote(cn1.ID).CreditStatus, tt.wantStatus)
			for _, fe := range fees {
				if tt.wantFees[0] == "" {
					// lago's invoice total is inconsistent with its fees here
					break
				}
				switch fe.ID {
				case f1.ID:
					eqDec(t, "fee1 credit notes", fe.PreciseCreditNotesAmount, tt.wantFees[0])
				case f2.ID:
					eqDec(t, "fee2 credit notes", fe.PreciseCreditNotesAmount, tt.wantFees[1])
				}
			}
		})
	}
}

// Credit notes of the invoice itself are not applied to it, nor applied
// twice.
func TestApplyCreditNotesOwnAndTwice(t *testing.T) {
	f := newFixture(t)
	h := f.h
	inv := h.StoredInvoice(f.cust)
	h.StoredFee(inv, 100)
	h.StoredCreditNote(inv, 40)
	src := h.StoredInvoice(f.cust)
	other := h.StoredCreditNote(src, 30)
	inv.SubTotalExcludingTaxesAmount = 100
	out, _, err := f.after(inv, deduct.Options{})
	mustOK(t, err)
	eq(t, "credit notes", out.CreditNotes, 30)
	eq(t, "credit note", out.Credits[0].CreditNoteID, other.ID)
	inv.CreditNotesAmount = 0
	out, _, err = f.after(inv, deduct.Options{})
	mustOK(t, err)
	eq(t, "second application", out.CreditNotes, 0)
	eq(t, "stored credits", len(h.InvoiceCredits(inv.ID)), 1)
}

// Drafts, one-off and credit invoices take no credit notes (domain §22.8).
func TestApplyAfterTaxesSkipped(t *testing.T) {
	tests := []struct {
		name  string
		typ   invoice.Type
		draft bool
	}{
		{"draft", invoice.TypeSubscription, true},
		{"one off", invoice.TypeOneOff, false},
		{"credit", invoice.TypeCredit, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			h := f.h
			src := h.StoredInvoice(f.cust)
			h.StoredCreditNote(src, 30)
			h.Wallet(f.cust, "10")
			inv := h.StoredInvoice(f.cust, func(inv *invoice.Invoice) { inv.Type = tt.typ })
			h.StoredFee(inv, 100)
			inv.SubTotalExcludingTaxesAmount = 100
			out, _, err := f.after(inv, deduct.Options{Draft: tt.draft})
			mustOK(t, err)
			eq(t, "credit notes", out.CreditNotes, 0)
			eq(t, "prepaid", out.PrepaidCredit, 0)
			eq(t, "total", inv.TotalAmount, 100)
		})
	}
}
