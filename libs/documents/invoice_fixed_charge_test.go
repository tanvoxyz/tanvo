package documents

import (
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
)

// Ports of spec/views/app/views/templates/invoices/v4/fixed_charge.slim_spec.rb:
// pay-in-advance fixed charge invoices.

// fixedChargeInvoice builds the spec's invoice (LAGO-202509-FC-001 unless
// inv changes it) with the fixed charge fees of specs, all paid in advance
// and billing September unless their period says otherwise.
func fixedChargeInvoice(inv func(inv *invoice.Invoice), specs ...fixedSpec) func(t *testing.T, g *ids) *Invoice {
	return func(t *testing.T, g *ids) *Invoice {
		sep := span("2025-09-01 00:00:00", "2025-09-30 23:59:59")
		plan := newPlan(g, "monthly", func(p *catalog.Plan) { p.InvoiceDisplayName = ptr("Monthly Plan") })
		sub := newSubscription(g, plan, periods(sep), func(is *invoice.Subscription) { is.Timestamp = sep.From })
		in := newInvoice(g, invoice.TypeSubscription, "LAGO-202509-FC-001", func(i *invoice.Invoice) {
			i.IssuingDate, i.PaymentDueDate = day("2025-09-01"), day("2025-09-15")
			i.FeesAmount, i.SubTotalExcludingTaxesAmount, i.SubTotalIncludingTaxesAmount, i.TotalAmount = 10000, 10000, 10000, 10000
			if inv != nil {
				inv(i)
			}
		})
		out := &Invoice{Party: staticParty(), Invoice: in, Subscriptions: []Subscription{sub}}
		for _, s := range specs {
			s.advance = true
			if s.period.From.IsZero() {
				s.period = sep
			}
			out.Fees = append(out.Fees, fixedFee(t, g, &sub, s))
		}
		return out
	}
}

func TestInvoiceV4FixedCharge(t *testing.T) {
	standard := func(name string) fixedSpec {
		return fixedSpec{name: name, amount: 5000, units: "2", unitCents: 2500, precise: "25"}
	}
	runInvoiceCases(t, []invoiceCase{
		{name: "single fixed charge", snapshot: "v4_fixed_charge/with_a_single_fixed_charge_fee", build: fixedChargeInvoice(nil, standard("Standard Fixed Charge"))},
		{
			name: "different billing periods", snapshot: "v4_fixed_charge/with_multiple_fixed_charge_fees_in_different_billing_periods",
			build: fixedChargeInvoice(nil, standard("September Fixed Charge"), fixedSpec{
				name: "October Fixed Charge", amount: 3000, units: "1", unitCents: 3000, precise: "30",
				period: span("2025-10-01 00:00:00", "2025-10-31 23:59:59"),
			}),
		},
		{
			name: "same billing period", snapshot: "v4_fixed_charge/with_multiple_fixed_charge_fees_in_the_same_billing_period",
			build: fixedChargeInvoice(nil, standard("Fixed Charge A"), fixedSpec{name: "Fixed Charge B", amount: 3000, units: "1", unitCents: 3000, precise: "30"}),
		},
		{
			name: "graduated", snapshot: "v4_fixed_charge/with_graduated_charge_model",
			build: fixedChargeInvoice(nil, fixedSpec{
				name: "Graduated Fixed Charge", model: catalog.FixedChargeModelGraduated, amount: 55500, units: "15", unitCents: 3700, precise: "37",
				details: graduatedFixedDetails(),
			}),
		},
		{
			name: "prorated", snapshot: "v4_fixed_charge/with_prorated_fixed_charge",
			build: fixedChargeInvoice(nil, fixedSpec{name: "Prorated Fixed Charge", prorated: true, amount: 2500, units: "0.5", unitCents: 5000, precise: "50"}),
		},
		{
			name: "zero amount", snapshot: "v4_fixed_charge/with_zero_amount_fee",
			build: fixedChargeInvoice(nil, fixedSpec{name: "Zero Amount Fixed Charge", units: "0"}),
		},
		{
			name: "coupon", snapshot: "v4_fixed_charge/with_coupon_applied",
			build: func(t *testing.T, g *ids) *Invoice {
				d := fixedChargeInvoice(func(inv *invoice.Invoice) {
					inv.Number, inv.TotalAmount, inv.FeesAmount, inv.CouponsAmount = "LAGO-202509-FC-002", 4500, 5000, 500
					inv.SubTotalExcludingTaxesAmount, inv.SubTotalIncludingTaxesAmount = 4500, 4500
				}, standard("Fixed Charge with Coupon"))(t, g)
				d.Coupons = []CouponCredit{{CouponName: "10% Off Coupon", FixedAmount: 200, Currency: eur(), Amount: 500}}
				return d
			},
		},
		{
			name: "prepaid credits", snapshot: "v4_fixed_charge/with_prepaid_credits",
			build: func(t *testing.T, g *ids) *Invoice {
				d := fixedChargeInvoice(func(inv *invoice.Invoice) {
					inv.Number, inv.TotalAmount, inv.FeesAmount = "LAGO-202509-FC-003", 4000, 5000
					inv.SubTotalExcludingTaxesAmount, inv.SubTotalIncludingTaxesAmount = 5000, 5000
					inv.PrepaidCreditAmount, inv.PrepaidGrantedCreditAmount, inv.PrepaidPurchasedCreditAmount = 1000, 400, 600
				}, standard("Fixed Charge"))(t, g)
				d.HasWalletTransactions = true
				return d
			},
		},
	})
}

// source: spec/views/app/views/templates/invoices/v4/fixed_charge.slim_spec.rb:328
func TestInvoiceV4FixedChargeProrationCaption(t *testing.T) {
	d := fixedChargeInvoice(nil, fixedSpec{name: "Prorated Fixed Charge", prorated: true, amount: 2500, units: "0.5", unitCents: 5000, precise: "50"})(t, &ids{})
	w := newWriter(testCatalog(t), &d.Party, "", d.Invoice.Currency)
	got := strings.Join(w.invoiceLayout(d).texts(), "\n")
	if want := "The fee is prorated for the period; the displayed unit price is an average"; !strings.Contains(got, want) {
		t.Errorf("texts: got = no %q, want = %q", want, want)
	}
	if strings.Contains(got, "prorated on days of usage") {
		t.Errorf("texts: got = %q, want = no usage proration caption", "prorated on days of usage")
	}
}

// source: spec/views/app/views/templates/invoices/v4/fixed_charge.slim_spec.rb:89
func TestInvoiceV4FixedChargePurchaseOrderNumber(t *testing.T) {
	d := fixedChargeInvoice(nil, fixedSpec{name: "Standard Fixed Charge", amount: 5000, units: "2", unitCents: 2500, precise: "25"})(t, &ids{})
	d.Invoice.PurchaseOrderNumber = ptr("PO-123")
	w := newWriter(testCatalog(t), &d.Party, "", d.Invoice.Currency)
	got := strings.Join(w.invoiceLayout(d).texts(), "\n")
	for _, want := range []string{"Purchase Order Number", "PO-123"} {
		if !strings.Contains(got, want) {
			t.Errorf("texts: got = no %q, want = %q", want, want)
		}
	}
}
