package invoicing_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// noteWant is the expected shape of a credit note issued by a void.
type noteWant struct {
	credit, refund, total int64
	creditStatus          creditnote.CreditStatus
}

func TestVoidGenerateCreditNote(t *testing.T) {
	tests := []struct {
		name   string
		source string
		in     invoicing.VoidInput
		want   []noteWant
		field  string
	}{
		{
			name: "the requested note credits the whole invoice", source: "spec/scenarios/invoices/void_invoice_spec.rb:16",
			in:   invoicing.VoidInput{GenerateCreditNote: true, CreditAmount: 1200},
			want: []noteWant{{1200, 0, 1200, creditnote.CreditStatusAvailable}},
		},
		{
			// lago pays 300 first; voiding needs total_paid_amount = 0 here
			// (domain §11.4), so the split is credit only.
			name: "a partial note and a voided note for the rest", source: "spec/scenarios/invoices/void_invoice_spec.rb:91",
			in: invoicing.VoidInput{GenerateCreditNote: true, CreditAmount: 600},
			want: []noteWant{
				{600, 0, 600, creditnote.CreditStatusAvailable},
				{600, 0, 600, creditnote.CreditStatusVoided},
			},
		},
		{
			name: "no amount voids a note over the whole invoice", source: "app/services/invoices/void_service.rb:89",
			in:   invoicing.VoidInput{GenerateCreditNote: true},
			want: []noteWant{{1200, 0, 1200, creditnote.CreditStatusVoided}},
		},
		{
			name: "a credit above the creditable amount fails", source: "app/services/invoices/void_service.rb:80",
			in:    invoicing.VoidInput{GenerateCreditNote: true, CreditAmount: 1201},
			field: "credit_refund_amount:" + creditnotes.CodeTotalAmountExceedsInvoiceAmount,
		},
		{
			name: "a refund above the refundable amount fails", source: "app/services/invoices/void_service.rb:81",
			in:    invoicing.VoidInput{GenerateCreditNote: true, RefundAmount: 1},
			field: "credit_refund_amount:" + creditnotes.CodeTotalAmountExceedsInvoiceAmount,
		},
		{
			name: "without generate_credit_note no note is issued", source: "spec/services/invoices/void_service_spec.rb:64",
			in: invoicing.VoidInput{CreditAmount: 1200},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			f := newFixture(t, billingDay)
			f.taxCustomer("20")
			inv := f.billOne(f.subscribe())
			checkAmounts(t, inv, 1000, 200, 1200)
			_, err := f.svc.Void(f.Ctx, f.OrgID(), inv.ID, tt.in)
			if tt.field != "" {
				var ve *core.ValidationError
				if !errors.As(err, &ve) || !slices.Contains(fieldCodes(ve), tt.field) {
					t.Fatalf("err = %v, want = a validation error on %s", err, tt.field)
				}
				if got := f.Invoice(inv.ID).Status; got != invoice.StatusFinalized {
					t.Errorf("status = %s, want = finalized", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("void: %v", err)
			}
			if got := f.Invoice(inv.ID).Status; got != invoice.StatusVoided {
				t.Fatalf("status = %s, want = voided", got)
			}
			notes := f.CreditNotes(inv.ID)
			if len(notes) != len(tt.want) {
				t.Fatalf("credit notes = %d, want = %d", len(notes), len(tt.want))
			}
			slices.SortFunc(notes, func(a, b *creditnote.CreditNote) int { return int(a.SequentialID - b.SequentialID) })
			for i, w := range tt.want {
				cn := notes[i]
				got := noteWant{cn.CreditAmount, cn.RefundAmount, cn.TotalAmount, cn.CreditStatus}
				if got != w {
					t.Errorf("note %d = %+v, want = %+v", i, got, w)
				}
				if cn.Status != creditnote.StatusFinalized || cn.Reason != creditnote.ReasonOther {
					t.Errorf("note %d status, reason = %s, %s, want = finalized, other", i, cn.Status, cn.Reason)
				}
				if want := "Credit note created due to voided invoice " + inv.ID.String(); cn.Description == nil || *cn.Description != want {
					t.Errorf("note %d description = %v, want = %q", i, cn.Description, want)
				}
			}
			// The notes' webhooks come before invoice.voided.
			hooks := f.Webhooks()
			voided := slices.Index(hooks, webhook.EventTypeInvoiceVoided)
			created := 0
			for i, h := range hooks {
				if h == webhook.EventTypeCreditNoteCreated {
					created++
					if i > voided {
						t.Errorf("webhooks = %v, want = credit_note.created before invoice.voided", hooks)
					}
				}
			}
			if voided < 0 || created != len(tt.want) {
				t.Errorf("webhooks = %v, want = %d credit_note.created and invoice.voided", hooks, len(tt.want))
			}
		})
	}
}

// fieldCodes returns the "field:code" pairs of ve.
func fieldCodes(ve *core.ValidationError) []string {
	var out []string
	for _, fe := range ve.Errors {
		out = append(out, fe.Field+":"+fe.Code)
	}
	return out
}

// source: spec/services/invoices/void_service_spec.rb:229
func TestVoidGenerateCreditNoteProgressiveBilling(t *testing.T) {
	f := newFixture(t, billingDay)
	inv, _ := f.CreditableInvoice(f.cust, []int64{400}, nil, []func(*invoice.Invoice){func(inv *invoice.Invoice) {
		inv.PaymentStatus = core.PaymentStatusPending
	}})
	inv.ProgressiveBillingCreditAmount = 200
	inv.SubTotalExcludingTaxesAmount, inv.SubTotalIncludingTaxesAmount, inv.TotalAmount = 200, 200, 200
	f.SaveInvoice(inv)
	if _, err := f.svc.Void(f.Ctx, f.OrgID(), inv.ID, invoicing.VoidInput{GenerateCreditNote: true, CreditAmount: 200}); err != nil {
		t.Fatalf("void: %v", err)
	}
	var avail []*creditnote.CreditNote
	for _, cn := range f.CreditNotes(inv.ID) {
		if cn.CreditStatus == creditnote.CreditStatusAvailable {
			avail = append(avail, cn)
		}
	}
	if len(avail) != 1 {
		t.Fatalf("available notes = %d, want = 1", len(avail))
	}
	// source: spec/services/invoices/void_service_spec.rb:280
	cn := avail[0]
	if cn.TotalAmount != 200 || cn.CreditAmount != 200 || len(cn.Items) != 1 || cn.Items[0].Amount != 200 {
		t.Errorf("total, credit, items = %d, %d, %+v, want = 200, 200, one of 200", cn.TotalAmount, cn.CreditAmount, cn.Items)
	}
}

// source: app/services/invoices/void_service.rb:42
func TestVoidGenerateCreditNoteKeepsWalletConsumed(t *testing.T) {
	for _, generate := range []bool{false, true} {
		f := newFixture(t, billingDay)
		w := f.Wallet(f.cust, "3")
		inv := f.billOne(f.subscribe())
		if inv.PrepaidCreditAmount != 300 {
			t.Fatalf("prepaid credit = %d, want = 300", inv.PrepaidCreditAmount)
		}
		if _, err := f.svc.Void(f.Ctx, f.OrgID(), inv.ID, invoicing.VoidInput{GenerateCreditNote: generate}); err != nil {
			t.Fatalf("void: %v", err)
		}
		want := int64(300)
		if generate {
			want = 0
		}
		if got := f.ReloadWallet(w.ID).BalanceAmount; got != want {
			t.Errorf("generate %t: wallet balance = %d, want = %d", generate, got, want)
		}
	}
}
