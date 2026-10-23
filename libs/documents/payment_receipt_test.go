package documents

import (
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// Ports of spec/views/app/views/templates/payment_receipts/v1.slim_spec.rb.

func receiptForInvoice(t *testing.T, inv *Invoice) *PaymentReceipt {
	t.Helper()
	inv.Invoice.TotalPaidAmount = inv.Invoice.TotalAmount
	return &PaymentReceipt{
		Party:   inv.Party,
		Receipt: payment.Receipt{Number: "RCT-202509-001"},
		Payment: payment.Payment{Amount: inv.Invoice.TotalAmount, Currency: usd(), CreatedAt: at("2025-09-15 13:00:00")},
		Invoice: inv,
	}
}

func receiptText(t *testing.T, d *PaymentReceipt) string {
	t.Helper()
	w := newWriter(testCatalog(t), &d.Party, "", d.Payment.Currency)
	return strings.Join(w.receiptLayout(d).texts(), "\n")
}

// source: spec/views/app/views/templates/payment_receipts/v1.slim_spec.rb:104
func TestPaymentReceiptProgressiveBillingWithoutThreshold(t *testing.T) {
	inv := progressiveBillingInvoice(false)(t, &ids{})
	inv.Invoice.Number = "LAGO-202509-PR-001"
	got := receiptText(t, receiptForInvoice(t, inv))
	if strings.Contains(got, "This progressive billing is generated because your cumulative usage has reached") {
		t.Errorf("texts: got = a reached usage threshold line, want = none")
	}
	for _, want := range []string{"RCT-202509-001", "LAGO-202509-PR-001", "Usage Charge Fee", "$95.00"} {
		if !strings.Contains(got, want) {
			t.Errorf("texts: got = no %q, want = %q", want, want)
		}
	}
}

func TestPaymentReceiptProgressiveBillingWithThreshold(t *testing.T) {
	got := receiptText(t, receiptForInvoice(t, progressiveBillingInvoice(true)(t, &ids{})))
	if want := "This progressive billing is generated because your cumulative usage has reached"; !strings.Contains(got, want) {
		t.Errorf("texts: got = no %q, want = %q", want, want)
	}
}
