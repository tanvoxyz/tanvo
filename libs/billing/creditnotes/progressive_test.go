package creditnotes_test

import (
	"context"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestCreateFromProgressiveBillingInvoice(t *testing.T) {
	tests := []struct {
		name   string
		source string
		typ    invoice.Type
		amount string
		check  func(t *testing.T, f *fixture, cn *creditnote.CreditNote, err error)
	}{
		{
			name: "a zero amount issues nothing", source: "spec/services/credit_notes/create_from_progressive_billing_invoice_spec.rb:61",
			amount: "0",
			check: func(t *testing.T, f *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				if cn != nil {
					t.Fatalf("credit note: got = %+v, want = nil", cn)
				}
				eq(t, "stored", len(f.h.CreditNotes(f.inv.ID)), 0)
			},
		},
		{
			name: "a subscription invoice fails", source: "spec/services/credit_notes/create_from_progressive_billing_invoice_spec.rb:71",
			typ: invoice.TypeSubscription, amount: "100",
			check: func(t *testing.T, _ *fixture, _ *creditnote.CreditNote, err error) {
				wantRule(t, err, creditnotes.CodeNotProgressiveBilling)
			},
		},
		{
			name: "credits the largest fees first", source: "spec/services/credit_notes/create_from_progressive_billing_invoice_spec.rb:97",
			amount: "100",
			check: func(t *testing.T, f *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "credit", cn.CreditAmount, 120)
				if len(cn.Items) != 2 {
					t.Fatalf("items: got = %d, want = 2", len(cn.Items))
				}
				for _, it := range cn.Items {
					switch it.FeeID {
					case f.fees[0].ID:
						eq(t, "fee 1 item", it.Amount, 80)
					case f.fees[1].ID:
						eq(t, "fee 2 item", it.Amount, 20)
					default:
						t.Errorf("item fee: got = %v, want = a fee of the invoice", it.FeeID)
					}
				}
				eq(t, "applied taxes", len(cn.AppliedTaxes), 1)
				if len(cn.AppliedTaxes) == 1 {
					eq(t, "tax code", cn.AppliedTaxes[0].TaxCode, f.tax.Code)
					eq(t, "tax", cn.AppliedTaxes[0].TaxID, f.tax.ID)
				}
				eq(t, "reason", cn.Reason, creditnote.ReasonOther)
				eq(t, "stored", len(f.h.CreditNotes(f.inv.ID)), 1)
			},
		},
		{
			name: "an amount above the creditable fees fails", source: "spec/services/credit_notes/create_from_progressive_billing_invoice_spec.rb:118",
			amount: "130",
			check: func(t *testing.T, _ *fixture, _ *creditnote.CreditNote, err error) {
				wantRule(t, err, creditnotes.CodeCreditableAmountIsLessThanRequested)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h, svc := newHarness(t)
			f := &fixture{h: h, svc: svc, cust: h.Customer(), tax: h.Tax("vat", "20")}
			typ := tt.typ
			if typ == "" {
				typ = invoice.TypeProgressiveBilling
			}
			f.inv, f.fees = h.CreditableInvoice(f.cust, []int64{80, 40}, []*catalog.Tax{f.tax},
				[]func(*invoice.Invoice){func(inv *invoice.Invoice) { inv.Type = typ }})
			var cn *creditnote.CreditNote
			err := h.Deps.Tx.InTx(h.Ctx, func(ctx context.Context, r port.Repos) error {
				var err error
				cn, err = svc.CreateFromProgressiveBillingInvoice(ctx, r, creditnotes.ProgressiveInput{
					OrganizationID: h.OrgID(), InvoiceID: f.inv.ID, Amount: money.MustParse(tt.amount),
					Reason: creditnote.ReasonOther,
				})
				return err
			})
			tt.check(t, f, cn, err)
		})
	}
}
