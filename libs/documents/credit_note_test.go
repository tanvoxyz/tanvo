package documents

import (
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Ports of spec/views/app/views/templates/credit_notes/credit_note.slim_spec.rb.

// creditSpec is one credited fee of the spec.
type creditSpec struct {
	typ    core.FeeType
	name   string
	amount int64
	units  string
	// taxRates are the rates of the fee's applied taxes.
	taxRates []string
}

// creditNoteData builds the spec's credit note of the invoice
// LAGO-202509-001, crediting each fee of specs in full.
func creditNoteData(number string, total int64, mod func(cn *creditnote.CreditNote), specs ...creditSpec) func(t *testing.T, g *ids) *CreditNote {
	return func(t *testing.T, g *ids) *CreditNote {
		plan := newPlan(g, "premium", func(p *catalog.Plan) { p.Name = "Premium Plan" })
		sub := newSubscription(g, plan)
		sub.Subscription.Name = ptr("Premium Plan")
		inv := newInvoice(g, invoice.TypeSubscription, "LAGO-202509-001", nil)
		cn := creditnote.CreditNote{
			Number: number, IssuingDate: day("2025-10-05"), Currency: usd(),
			TotalAmount: total, CreditAmount: total, SubTotalExcludingTaxesAmount: total,
			PreciseCouponsAdjustmentAmount: money.Zero, PreciseTaxesAmount: money.Zero,
		}
		out := &CreditNote{Party: staticParty(), Invoice: inv, Subscriptions: []Subscription{sub}}
		for _, s := range specs {
			f := newFee(g, s.typ, func(f *invoice.Fee) {
				f.SubscriptionID, f.Amount = sub.Subscription.ID, s.amount
				if s.units != "" {
					f.Units = d(s.units)
				}
				for _, r := range s.taxRates {
					f.AppliedTaxes = append(f.AppliedTaxes, invoice.FeeAppliedTax{TaxRate: d(r)})
				}
			})
			fee := Fee{Fee: f}
			switch s.typ {
			case core.FeeTypeFixedCharge:
				fc := catalog.FixedCharge{ID: g.fixedCharge(), PlanID: plan.ID, InvoiceDisplayName: ptr(s.name), ChargeModel: catalog.FixedChargeModelStandard, Units: d("1")}
				fee.FixedCharge, fee.Fee.FixedChargeID = &fc, fc.ID
			case core.FeeTypeCharge:
				c, m := metricCharge(g, plan, "metric", func(c *catalog.Charge) { c.InvoiceDisplayName = ptr(s.name) })
				fee.Charge, fee.Metric, fee.Fee.ChargeID = &c, &m, c.ID
			}
			out.Fees = append(out.Fees, fee)
			cn.Items = append(cn.Items, creditnote.Item{FeeID: f.ID, Amount: s.amount, PreciseAmount: money.NewFromInt(s.amount)})
		}
		if mod != nil {
			mod(&cn)
		}
		out.CreditNote = cn
		return out
	}
}

func runCreditNoteCases(t *testing.T, cases []struct {
	name, snapshot string
	build          func(t *testing.T, g *ids) *CreditNote
	skip           []string
},
) {
	t.Helper()
	cat := testCatalog(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := c.build(t, &ids{})
			w := newWriter(cat, &d.Party, "", d.CreditNote.Currency)
			checkSnapshot(t, w.creditNoteLayout(d), c.snapshot, c.skip...)
		})
	}
}

func cnTax(name, code, rate string, base, amount int64) creditnote.AppliedTax {
	return creditnote.AppliedTax{TaxName: name, TaxCode: code, TaxRate: d(rate), BaseAmount: base, Amount: amount, PreciseAmount: money.NewFromInt(amount)}
}

func TestCreditNote(t *testing.T) {
	fixed := func(name string, rates ...string) creditSpec {
		return creditSpec{typ: core.FeeTypeFixedCharge, name: name, amount: 1000, taxRates: rates}
	}
	runCreditNoteCases(t, []struct {
		name, snapshot string
		build          func(t *testing.T, g *ids) *CreditNote
		skip           []string
	}{
		{
			name: "all fee types", snapshot: "credit_note/with_subscription_fee__fixed_charge_fee__and_charge_fee__renders_all_fee_types_correctly",
			build: creditNoteData("CN-202510-001", 3000, nil,
				creditSpec{typ: core.FeeTypeSubscription, amount: 1000}, fixed("Setup Fee"),
				creditSpec{typ: core.FeeTypeCharge, name: "API Calls", amount: 1000, units: "100"}),
		},
		{
			name: "only fixed charges", snapshot: "credit_note/with_only_fixed_charge_fees__renders_each_fixed_charge_fee_separately",
			build: creditNoteData("CN-202510-002", 2200, func(cn *creditnote.CreditNote) {
				cn.SubTotalExcludingTaxesAmount, cn.TaxesAmount = 2000, 200
				cn.AppliedTaxes = []creditnote.AppliedTax{cnTax("VAT", "vat", "10.0", 2000, 200)}
			}, fixed("Setup Fee", "10.0"), fixed("Installation Fee", "10.0")),
		},
		{
			// The spec's stubs answer 20% for both items (its stubbed
			// relation ignores the item); Tanvo prints each fee's own rate.
			name: "different tax rates", snapshot: "credit_note/with_fixed_charges_having_different_tax_rates__renders_fixed_charges_with_multiple_tax_rates_in_descending_order",
			build: creditNoteData("CN-202510-004", 2300, func(cn *creditnote.CreditNote) {
				cn.SubTotalExcludingTaxesAmount, cn.TaxesAmount = 2000, 300
				cn.AppliedTaxes = []creditnote.AppliedTax{cnTax("VAT", "vat", "10.0", 1000, 100), cnTax("Sales Tax", "sales_tax", "20.0", 1000, 200)}
			}, fixed("Setup Fee", "10.0"), fixed("Installation Fee", "20.0")),
			skip: []string{"20.0%"},
		},
		{
			name: "coupon adjustment", snapshot: "credit_note/with_fixed_charges_and_coupon_adjustment__renders_fixed_charges_with_coupon_adjustment_in_totals",
			build: creditNoteData("CN-202510-005", 1500, func(cn *creditnote.CreditNote) {
				cn.CouponsAdjustmentAmount, cn.PreciseCouponsAdjustmentAmount = 500, d("500")
			}, fixed("Setup Fee"), fixed("Onboarding Fee")),
		},
		{
			name: "only charges", snapshot: "credit_note/with_only_charge_fees__renders_each_charge_fee_correctly",
			build: creditNoteData("CN-202510-003", 2500, nil,
				creditSpec{typ: core.FeeTypeCharge, name: "API Calls", amount: 1500, units: "150"},
				creditSpec{typ: core.FeeTypeCharge, name: "Storage", amount: 1000, units: "100"}),
		},
	})
}

func TestCreditNotePurchaseOrderNumber(t *testing.T) {
	for _, c := range []struct {
		name, po string
	}{
		// source: spec/views/app/views/templates/credit_notes/credit_note.slim_spec.rb:752
		{"inherited", "PO-12345"},
		// source: spec/views/app/views/templates/credit_notes/credit_note.slim_spec.rb:759
		{"none", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := creditNoteData("CN-202510-099", 1000, nil)(t, &ids{})
			if c.po != "" {
				d.Invoice.PurchaseOrderNumber = &c.po
			}
			w := newWriter(testCatalog(t), &d.Party, "", d.CreditNote.Currency)
			got := strings.Join(w.creditNoteLayout(d).texts(), "\n")
			label := w.l.T("credit_note.purchase_order_number")
			if has := strings.Contains(got, label) && strings.Contains(got, "PO-12345"); has != (c.po != "") {
				t.Errorf("purchase order number shown: got = %t, want = %t", has, c.po != "")
			}
		})
	}
}
