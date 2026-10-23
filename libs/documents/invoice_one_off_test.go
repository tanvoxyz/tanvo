package documents

import (
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Ports of spec/views/app/views/templates/invoices/v4/one_off.slim_spec.rb.

// addOnSpec is one add-on fee of the spec.
type addOnSpec struct {
	addOn       string
	name        string
	description string
	amount      int64
	units       string
	unitCents   int64
	taxes       int64
	period      period.Period
}

// oneOffInvoice builds the spec's invoice (LAGO-202509-OO-001 unless inv
// changes it) with the add-on fees of specs.
func oneOffInvoice(inv func(inv *invoice.Invoice), specs ...addOnSpec) func(t *testing.T, g *ids) *Invoice {
	return func(t *testing.T, g *ids) *Invoice {
		in := newInvoice(g, invoice.TypeOneOff, "LAGO-202509-OO-001", func(i *invoice.Invoice) {
			i.IssuingDate, i.PaymentDueDate = day("2025-09-01"), day("2025-09-15")
			i.FeesAmount, i.SubTotalExcludingTaxesAmount, i.SubTotalIncludingTaxesAmount, i.TotalAmount = 10000, 10000, 10000, 10000
			if inv != nil {
				inv(i)
			}
		})
		out := &Invoice{Party: staticParty(), Invoice: in}
		for _, s := range specs {
			a := catalog.AddOn{ID: g.addOn(), Code: s.addOn, Name: s.addOn, Amount: 200, Currency: usd()}
			f := newFee(g, core.FeeTypeAddOn, func(f *invoice.Fee) {
				f.AddOnID, f.Amount, f.Units, f.UnitAmount = a.ID, s.amount, d(s.units), s.unitCents
				f.InvoiceDisplayName, f.TaxesAmount = ptr(s.name), s.taxes
				if s.description != "" {
					f.Description = ptr(s.description)
				}
				f.Properties.From, f.Properties.To = s.period.From, s.period.To
			})
			out.Fees = append(out.Fees, Fee{Fee: f, AddOn: &a})
		}
		return out
	}
}

func TestInvoiceV4OneOff(t *testing.T) {
	rate10 := d("10.0")
	setup := addOnSpec{addOn: "Setup Fee", name: "Setup Fee", amount: 10000, units: "1", unitCents: 10000}
	runInvoiceCases(t, []invoiceCase{
		{
			name: "single add-on", snapshot: "v4_one_off/with_a_single_add_on_fee",
			build: oneOffInvoice(nil, addOnSpec{addOn: "Setup Fee", name: "Setup Fee", description: "One-time setup fee", amount: 10000, units: "1", unitCents: 10000}),
		},
		{
			name: "multiple add-ons", snapshot: "v4_one_off/with_multiple_add_on_fees",
			build: oneOffInvoice(nil,
				addOnSpec{addOn: "Setup Fee", name: "Setup Fee", description: "Initial setup", amount: 5000, units: "1", unitCents: 5000},
				addOnSpec{addOn: "Training Fee", name: "Training Session", description: "2 hours of training", amount: 5000, units: "2", unitCents: 2500}),
		},
		{
			name: "date range", snapshot: "v4_one_off/with_add_on_fee_with_date_range",
			build: oneOffInvoice(nil, addOnSpec{
				addOn: "Monthly Support", name: "Premium Support", description: "Premium support package", amount: 10000, units: "1", unitCents: 10000,
				// Half-open: lago's to_datetime "2025-09-30" is the last day.
				period: period.Period{From: at("2025-09-01 00:00:00"), To: at("2025-10-01 00:00:00")},
			}),
		},
		{
			name: "taxes", snapshot: "v4_one_off/with_taxes_applied",
			build: func(t *testing.T, g *ids) *Invoice {
				d := oneOffInvoice(func(inv *invoice.Invoice) {
					inv.Number, inv.TotalAmount, inv.TaxesAmount = "LAGO-202509-OO-003", 11000, 1000
					inv.SubTotalIncludingTaxesAmount = 11000
				}, addOnSpec{addOn: "Professional Services", name: "Professional Services", amount: 10000, units: "1", unitCents: 10000, taxes: 1000})(t, g)
				d.AppliedTaxes = []invoice.AppliedTax{{
					TaxID: g.tax(), TaxName: "Sales Tax", TaxCode: "sales_tax", TaxRate: rate10, Amount: 1000, FeesAmount: 10000, Currency: usd(),
				}}
				return d
			},
		},
	})
	t.Run("purchase order number", func(t *testing.T) {
		// source: spec/views/app/views/templates/invoices/v4/one_off.slim_spec.rb:235
		d := oneOffInvoice(func(inv *invoice.Invoice) { inv.Number = "LAGO-202509-OO-PO" }, setup)(t, &ids{})
		d.PurchaseOrderNumber = "PO-12345"
		got := invoiceText(t, d)
		for _, want := range []string{"Purchase Order Number", "PO-12345"} {
			if !strings.Contains(got, want) {
				t.Errorf("texts: got = no %q, want = %q", want, want)
			}
		}
	})
	t.Run("without purchase order number", func(t *testing.T) {
		// source: spec/views/app/views/templates/invoices/v4/one_off.slim_spec.rb:258
		got := invoiceText(t, oneOffInvoice(nil, setup)(t, &ids{}))
		if strings.Contains(got, "Purchase Order Number") {
			t.Errorf("texts: got = %q, want = no purchase order number row", "Purchase Order Number")
		}
	})
}

// invoiceText is the texts of d's layout, one per line.
func invoiceText(t *testing.T, d *Invoice) string {
	t.Helper()
	w := newWriter(testCatalog(t), &d.Party, "", d.Invoice.Currency)
	return strings.Join(w.invoiceLayout(d).texts(), "\n")
}
