package einvoice

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// goldenFee is a fee of amount minor units at rate, with its tax and coupon
// share.
func goldenFee(typ core.FeeType, units, unitPrice string, amount int64, rate string, coupons, tax int64, taxPrecise string) invoice.Fee {
	return invoice.Fee{
		Type:                 typ,
		Amount:               amount,
		PreciseAmount:        money.NewFromInt(amount),
		PreciseUnitAmount:    dec(unitPrice),
		Units:                dec(units),
		TaxesAmount:          tax,
		TaxesPreciseAmount:   dec(taxPrecise),
		TaxesRate:            dec(rate),
		PreciseCouponsAmount: money.NewFromInt(coupons),
		Currency:             cur("EUR"),
	}
}

// totals fills the invoice amounts from its fees.
func totals(in *Invoice) {
	inv := in.Invoice
	inv.FeesAmount, inv.TaxesAmount = 0, 0
	for _, l := range in.Lines {
		inv.FeesAmount += l.Fee.Amount
		inv.TaxesAmount += l.Fee.TaxesAmount
	}
	inv.SubTotalExcludingTaxesAmount = inv.FeesAmount - inv.CouponsAmount - inv.ProgressiveBillingCreditAmount
	inv.SubTotalIncludingTaxesAmount = inv.SubTotalExcludingTaxesAmount + inv.TaxesAmount
	inv.TotalAmount = inv.SubTotalIncludingTaxesAmount - inv.PrepaidCreditAmount - inv.CreditNotesAmount
}

// frSubscription is a French subscription invoice: a plan at 20 % carrying
// a 10.00 coupon, usage at 5.5 % and storage at 0 %.
func frSubscription() *Invoice {
	in := newInvoiceInput(invoice.TypeSubscription, "EUR")
	in.Invoice.Number = "TAN-FR-2025-0042"
	in.Invoice.NetPaymentTermDays = 30
	in.Invoice.PaymentDueDate = date("2025-04-14")
	in.Invoice.CouponsAmount = 1000
	in.DeliveryDate = date("2025-03-01")
	in.Lines = []Line{
		{goldenFee(core.FeeTypeSubscription, "1", "100", 10000, "20", 1000, 1800, "1800"), "Premium", "Monthly subscription - Premium"},
		{goldenFee(core.FeeTypeCharge, "1234", "0.01", 1234, "5.5", 0, 68, "67.87"), "API calls", "API calls & <requests>"},
		{goldenFee(core.FeeTypeCharge, "50", "0.1", 500, "0", 0, 0, "0"), "Storage", "Storage GB"},
	}
	totals(in)
	return in
}

// deOneOff is a German one-off invoice at 19 % with a purchase order and
// prepaid credits applied.
func deOneOff() *Invoice {
	in := newInvoiceInput(invoice.TypeOneOff, "EUR",
		goldenFee(core.FeeTypeAddOn, "2", "125", 25000, "19", 0, 4750, "4750"))
	in.Lines[0].Name, in.Lines[0].Description = "Onboarding", "Onboarding workshop"
	in.BillingEntity.Address.Country = "DE"
	in.BillingEntity.Phone = ptr("+49 30 1234-5678")
	in.Invoice.Number = "TAN-DE-2025-0007"
	in.Invoice.NetPaymentTermDays = 14
	in.Invoice.PaymentDueDate = date("2025-03-29")
	in.Invoice.PrepaidCreditAmount = 500
	in.PurchaseOrderNumber = "PO-2025-118"
	totals(in)
	return in
}

// creditPurchase is a prepaid credits purchase, outside the scope of VAT.
func creditPurchase() *Invoice {
	in := newInvoiceInput(invoice.TypeCredit, "EUR",
		goldenFee(core.FeeTypeCredit, "50", "1", 5000, "0", 0, 0, "0"))
	in.Lines[0].Name, in.Lines[0].Description = "Main wallet", "Prepaid credits"
	in.Invoice.Number = "TAN-FR-2025-0043"
	totals(in)
	return in
}

// zeroTax is a French one-off invoice of an add-on at 0 %.
func zeroTax() *Invoice {
	in := newInvoiceInput(invoice.TypeOneOff, "EUR",
		goldenFee(core.FeeTypeAddOn, "1", "80", 8000, "0", 0, 0, "0"))
	in.Lines[0].Name, in.Lines[0].Description = "Audit", "Security audit"
	in.Invoice.Number = "TAN-FR-2025-0044"
	totals(in)
	return in
}

// reverseCharge is a German invoice to a French business with the VAT
// reverse charged: a 0 % rate.
func reverseCharge() *Invoice {
	in := newInvoiceInput(invoice.TypeOneOff, "EUR",
		goldenFee(core.FeeTypeAddOn, "3", "150", 45000, "0", 0, 0, "0"))
	in.Lines[0].Name, in.Lines[0].Description = "Consulting", "Consulting days"
	in.BillingEntity.Address.Country = "DE"
	in.Customer.BillingAddress.Country = "FR"
	in.Invoice.Number = "TAN-DE-2025-0008"
	totals(in)
	return in
}

// goldenCreditNote credits the usage and half of the plan of
// [frSubscription].
func goldenCreditNote(country string) *CreditNote {
	inv := frSubscription()
	plan, usage := inv.Lines[0], inv.Lines[1]
	cn := newCreditNoteInput(inv.Invoice,
		CreditNoteLine{
			Item: creditnote.Item{Amount: 5000, PreciseAmount: dec("5000")},
			Fee:  plan.Fee, Name: plan.Name, Description: plan.Description,
		},
		CreditNoteLine{
			Item: creditnote.Item{Amount: 1234, PreciseAmount: dec("1234")},
			Fee:  usage.Fee, Name: usage.Name, Description: usage.Description,
		},
	)
	cn.BillingEntity.Address.Country = country
	c := cn.CreditNote
	c.Number = "TAN-FR-2025-0042-CN001"
	c.Reason = creditnote.ReasonOrderChange
	c.PreciseCouponsAdjustmentAmount = dec("500")
	c.CouponsAdjustmentAmount = 500
	// 20 % of 45.00 and 5.5 % of 12.34.
	c.PreciseTaxesAmount = dec("967.87")
	c.TaxesAmount = 968
	c.SubTotalExcludingTaxesAmount = 5734
	c.TotalAmount = 6702
	c.CreditAmount = 6702
	cn.PurchaseOrderNumber = "PO-2025-007"
	return cn
}

func goldenPayment(method payment.MethodDetails) *Payment {
	kind := payment.KindProvider
	if method.IsZero() {
		kind = payment.KindManual
	}
	in := newPaymentInput(kind, method)
	in.Payment.Amount, in.Payment.Currency = 14602, cur("EUR")
	in.Invoices[0] = frSubscription().Invoice
	return in
}

// TestGolden compares every syntax of every scenario with its golden file
// byte for byte; UPDATE_GOLDEN=1 rewrites them.
func TestGolden(t *testing.T) {
	type render func() ([]byte, error)
	invoiceBoth := func(name string, in func() *Invoice) map[string]render {
		return map[string]render{
			name + ".cii.xml": func() ([]byte, error) { return InvoiceCII(in()) },
			name + ".ubl.xml": func() ([]byte, error) { return InvoiceUBL(in()) },
		}
	}
	golden := map[string]render{}
	for _, set := range []map[string]render{
		invoiceBoth("invoice_fr_subscription", frSubscription),
		invoiceBoth("invoice_de_one_off", deOneOff),
		invoiceBoth("invoice_credit_purchase", creditPurchase),
		invoiceBoth("invoice_zero_tax", zeroTax),
		invoiceBoth("invoice_de_reverse_charge", reverseCharge),
		{
			"credit_note_fr.cii.xml": func() ([]byte, error) { return CreditNoteCII(goldenCreditNote("FR")) },
			"credit_note_fr.ubl.xml": func() ([]byte, error) { return CreditNoteUBL(goldenCreditNote("FR")) },
			"credit_note_de.cii.xml": func() ([]byte, error) { return CreditNoteCII(goldenCreditNote("DE")) },
			"credit_note_de.ubl.xml": func() ([]byte, error) { return CreditNoteUBL(goldenCreditNote("DE")) },
			"payment_manual.cii.xml": func() ([]byte, error) { return PaymentCII(goldenPayment(payment.MethodDetails{})) },
			"payment_manual.ubl.xml": func() ([]byte, error) { return PaymentUBL(goldenPayment(payment.MethodDetails{})) },
			"payment_card.cii.xml": func() ([]byte, error) {
				return PaymentCII(goldenPayment(payment.MethodDetails{Type: "card", Brand: "visa", Last4: "4242"}))
			},
			"payment_card.ubl.xml": func() ([]byte, error) {
				return PaymentUBL(goldenPayment(payment.MethodDetails{Type: "card", Brand: "visa", Last4: "4242"}))
			},
		},
	} {
		for k, v := range set {
			golden[k] = v
		}
	}
	update := os.Getenv("UPDATE_GOLDEN") == "1"
	names := make([]string, 0, len(golden))
	for name := range golden {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			got, err := golden[name]()
			if err != nil {
				t.Fatalf("render: got = %v, want = nil", err)
			}
			parseXML(t, got)
			path := filepath.Join("testdata", name)
			if update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with UPDATE_GOLDEN=1): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs from the golden file:\ngot = %s\nwant = %s", name, got, want)
			}
		})
	}
}

// TestDeterministic renders the same documents twice, and with their lines
// shuffled, and expects identical bytes.
func TestDeterministic(t *testing.T) {
	reversed := func() *Invoice {
		in := frSubscription()
		slices.Reverse(in.Lines)
		return in
	}
	for _, f := range []struct {
		name   string
		render func(*Invoice) ([]byte, error)
	}{{"cii", InvoiceCII}, {"ubl", InvoiceUBL}} {
		t.Run(f.name, func(t *testing.T) {
			a, errA := f.render(frSubscription())
			b, errB := f.render(frSubscription())
			c, errC := f.render(reversed())
			if errA != nil || errB != nil || errC != nil {
				t.Fatalf("render: got = %v, %v, %v, want = nil", errA, errB, errC)
			}
			if !bytes.Equal(a, b) {
				t.Errorf("second render: got = %s, want = %s", b, a)
			}
			if !bytes.Equal(a, c) {
				t.Errorf("render with reversed lines: got = %s, want = %s", c, a)
			}
		})
	}
	cn := func() *CreditNote {
		in := goldenCreditNote("FR")
		slices.Reverse(in.Lines)
		return in
	}
	a, _ := CreditNoteUBL(goldenCreditNote("FR"))
	if b, _ := CreditNoteUBL(cn()); !bytes.Equal(a, b) {
		t.Errorf("credit note with reversed lines: got = %s, want = %s", b, a)
	}
}
