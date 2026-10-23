package documents

import (
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

// Ports of spec/views/app/views/templates/invoices/v4/self_billed.slim_spec.rb.

func selfBilled(build func(t *testing.T, g *ids) *Invoice) func(t *testing.T, g *ids) *Invoice {
	return func(t *testing.T, g *ids) *Invoice {
		d := build(t, g)
		d.Invoice.SelfBilled = true
		return d
	}
}

func selfBilledOneOff() func(t *testing.T, g *ids) *Invoice {
	return selfBilled(oneOffInvoice(func(inv *invoice.Invoice) {
		inv.Number, inv.TotalAmount, inv.FeesAmount = "LAGO-202509-SB-001", 50000, 50000
		inv.SubTotalExcludingTaxesAmount, inv.SubTotalIncludingTaxesAmount = 50000, 50000
	}, addOnSpec{addOn: "Partner Commission", name: "Partner Commission", description: "Monthly partner commission", amount: 50000, units: "1", unitCents: 50000}))
}

func TestInvoiceV4SelfBilled(t *testing.T) {
	vat := d("20.0")
	runInvoiceCases(t, []invoiceCase{
		{name: "one-off", snapshot: "v4_self_billed/with_one_off_invoice", build: selfBilledOneOff()},
		{
			name: "subscription", snapshot: "v4_self_billed/with_subscription_invoice",
			build: selfBilled(func(t *testing.T, g *ids) *Invoice {
				sep := span("2025-09-01 00:00:00", "2025-09-30 23:59:59")
				plan := newPlan(g, "partner", func(p *catalog.Plan) { p.InvoiceDisplayName = ptr("Partner Plan") })
				sub := newSubscription(g, plan, periods(sep), func(is *invoice.Subscription) { is.Timestamp = sep.From })
				inv := newInvoice(g, invoice.TypeSubscription, "LAGO-202509-SB-002", func(i *invoice.Invoice) {
					i.IssuingDate, i.PaymentDueDate = day("2025-09-01"), day("2025-09-15")
					i.FeesAmount, i.SubTotalExcludingTaxesAmount, i.SubTotalIncludingTaxesAmount, i.TotalAmount = 10000, 10000, 10000, 10000
				})
				fee := newFee(g, core.FeeTypeSubscription, func(f *invoice.Fee) {
					f.SubscriptionID, f.Amount, f.Units, f.InvoiceDisplayName = sub.Subscription.ID, 10000, d("1"), ptr("Partner Plan - Monthly")
					f.Properties.From, f.Properties.To, f.Properties.ChargesFrom, f.Properties.ChargesTo = sep.From, sep.To, sep.From, sep.To
				})
				return &Invoice{Party: staticParty(), Invoice: inv, Subscriptions: []Subscription{sub}, Fees: []Fee{{Fee: fee}}}
			}),
		},
		{
			name: "credit", snapshot: "v4_self_billed/with_credit_invoice",
			build: selfBilled(func(t *testing.T, g *ids) *Invoice {
				inv := newInvoice(g, invoice.TypeCredit, "LAGO-202509-SB-003", func(i *invoice.Invoice) {
					i.IssuingDate, i.PaymentDueDate = day("2025-09-01"), day("2025-09-15")
					i.FeesAmount, i.SubTotalExcludingTaxesAmount, i.SubTotalIncludingTaxesAmount, i.TotalAmount = 50000, 50000, 50000, 50000
				})
				w := wallet.Wallet{Currency: usd(), RateAmount: d("1.0")}
				wt := wallet.Transaction{CreditAmount: d("500.0"), Name: ptr("Prepaid Credits")}
				fee := newFee(g, core.FeeTypeCredit, func(f *invoice.Fee) {
					f.Amount, f.Units, f.InvoiceDisplayName = 50000, d("500"), ptr("Prepaid Credits")
				})
				return &Invoice{Party: staticParty(), Invoice: inv, Fees: []Fee{{Fee: fee, Wallet: &w, WalletTransaction: &wt}}}
			}),
		},
		{
			name: "taxes", snapshot: "v4_self_billed/with_taxes_applied",
			build: selfBilled(func(t *testing.T, g *ids) *Invoice {
				out := oneOffInvoice(func(inv *invoice.Invoice) {
					inv.Number, inv.TotalAmount, inv.FeesAmount, inv.TaxesAmount = "LAGO-202509-SB-004", 60000, 50000, 10000
					inv.SubTotalExcludingTaxesAmount, inv.SubTotalIncludingTaxesAmount = 50000, 60000
				}, addOnSpec{addOn: "Commission", name: "Commission", amount: 50000, units: "1", unitCents: 50000, taxes: 10000})(t, g)
				out.AppliedTaxes = []invoice.AppliedTax{{
					TaxID: g.tax(), TaxName: "VAT", TaxCode: "vat", TaxRate: vat, Amount: 10000, FeesAmount: 50000, Currency: usd(),
				}}
				return out
			}),
		},
	})
	t.Run("purchase order number", func(t *testing.T) {
		// source: spec/views/app/views/templates/invoices/v4/self_billed.slim_spec.rb:88
		d := selfBilledOneOff()(t, &ids{})
		d.Invoice.PurchaseOrderNumber = ptr("PO-123")
		got := invoiceText(t, d)
		for _, want := range []string{"Purchase Order Number", "PO-123"} {
			if !strings.Contains(got, want) {
				t.Errorf("texts: got = no %q, want = %q", want, want)
			}
		}
	})
	t.Run("progressive billing without threshold", func(t *testing.T) {
		// source: spec/views/app/views/templates/invoices/v4/self_billed.slim_spec.rb:310
		got := invoiceText(t, selfBilled(progressiveBillingInvoice(false))(t, &ids{}))
		if strings.Contains(got, "This progressive billing is generated because your cumulative usage has reached") {
			t.Errorf("texts: got = a reached usage threshold line, want = none")
		}
	})
}
