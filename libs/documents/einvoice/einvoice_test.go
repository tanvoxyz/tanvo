package einvoice

import (
	"errors"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// TestMissingDocument ports the create services' "without <resource>"
// examples: a missing document or party fails with core.ErrNotFound.
func TestMissingDocument(t *testing.T) {
	const svc = "spec/services/e_invoices/"
	withoutParty := func(in *Invoice) *Invoice { in.Customer = nil; return in }
	tests := []struct {
		name, source string
		render       func() ([]byte, error)
	}{
		{"invoice CII", svc + "invoices/cii/create_service_spec.rb:28", func() ([]byte, error) { return InvoiceCII(nil) }},
		{"invoice UBL", svc + "invoices/ubl/create_service_spec.rb:28", func() ([]byte, error) { return InvoiceUBL(&Invoice{}) }},
		{"credit note CII", svc + "credit_notes/cii/create_service_spec.rb:28", func() ([]byte, error) { return CreditNoteCII(nil) }},
		{"credit note UBL", svc + "credit_notes/ubl/create_service_spec.rb:28", func() ([]byte, error) { return CreditNoteUBL(&CreditNote{}) }},
		{"payment CII", svc + "payments/cii/create_service_spec.rb:29", func() ([]byte, error) { return PaymentCII(nil) }},
		{"payment UBL", svc + "payments/ubl/create_service_spec.rb:29", func() ([]byte, error) { return PaymentUBL(&Payment{}) }},
		{
			"credit note without its invoice", svc + "credit_notes/cii/create_service_spec.rb:28",
			func() ([]byte, error) { return CreditNoteCII(newCreditNoteInput(nil)) },
		},
		{
			"payment without its receipt", svc + "payments/ubl/create_service_spec.rb:29",
			func() ([]byte, error) {
				in := newPaymentInput(payment.KindManual, payment.MethodDetails{})
				in.Receipt = nil
				return PaymentUBL(in)
			},
		},
		{
			"invoice without its customer", svc + "invoices/cii/create_service_spec.rb:28",
			func() ([]byte, error) { return InvoiceCII(withoutParty(newInvoiceInput(invoice.TypeOneOff, "EUR"))) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			b, err := tt.render()
			if !errors.Is(err, core.ErrNotFound) || b != nil {
				t.Errorf("got = (%q, %v), want = (nil, %v)", b, err, core.ErrNotFound)
			}
		})
	}
}

// TestEligible ports the generate_xml services' guards: only billing
// entities with e-invoicing enabled in France or Germany get a file.
func TestEligible(t *testing.T) {
	const inv = "spec/services/invoices/generate_xml_service_spec.rb:"
	const rcpt = "spec/services/payment_receipts/generate_xml_service_spec.rb:"
	entity := func(country string, enabled bool) *organization.BillingEntity {
		be := newBillingEntity(country)
		be.EInvoicing = enabled
		return be
	}
	tests := []struct {
		name, source string
		be           *organization.BillingEntity
		want         bool
	}{
		{"France", inv + "37", entity("FR", true), true},
		{"Germany", rcpt + "48", entity("DE", true), true},
		{"lowercase country", inv + "37", entity("fr", true), true},
		{"country not allowed", inv + "108", entity("BR", true), false},
		{"payment receipt country not allowed", rcpt + "107", entity("BR", true), false},
		{"no country", inv + "108", entity("", true), false},
		{"e-invoicing disabled", inv + "117", entity("FR", false), false},
		{"payment receipt e-invoicing disabled", rcpt + "116", entity("FR", false), false},
		{"no billing entity", inv + "73", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			if got := Eligible(tt.be); got != tt.want {
				t.Errorf("Eligible() got = %v, want = %v", got, tt.want)
			}
		})
	}
}

func TestAllocate(t *testing.T) {
	ds := func(ss ...string) []money.Decimal {
		out := make([]money.Decimal, len(ss))
		for i, s := range ss {
			out[i] = dec(s)
		}
		return out
	}
	tests := []struct {
		name    string
		total   int64
		weights []money.Decimal
		want    []int64
	}{
		{"exact split", 76, ds("0", "19", "57"), []int64{0, 19, 57}},
		{"residue to the largest remainder", 21, ds("0.6", "20"), []int64{1, 20}},
		{"ties go to the earlier part", 1, ds("0.4", "0.4"), []int64{1, 0}},
		{"three-way residue", 10, ds("1", "1", "1"), []int64{4, 3, 3}},
		{"negative total", -10, ds("1", "1", "1"), []int64{-4, -3, -3}},
		{"zero total", 0, ds("1", "2"), []int64{0, 0}},
		{"zero weights", 5, ds("0", "0"), []int64{0, 0}},
		{"no parts", 5, nil, []int64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := allocate(tt.total, tt.weights); !slices.Equal(got, tt.want) {
				t.Errorf("allocate(%d, %v) got = %v, want = %v", tt.total, tt.weights, got, tt.want)
			}
		})
	}
	t.Run("by group falls back to precise amounts", func(t *testing.T) {
		got := allocateByGroup(1, ds("0", "0"), ds("0.4", "0.4"))
		if want := []int64{1, 0}; !slices.Equal(got, want) {
			t.Errorf("allocateByGroup() got = %v, want = %v", got, want)
		}
	})
}

func TestFormat(t *testing.T) {
	en := EnglishLabels()
	tests := []struct {
		name, got, want string
	}{
		{"number rounds half up", number(dec("0.125")), "0.13"},
		{"number pads", number(dec("5")), "5.00"},
		{"number has no negative zero", number(dec("-0.001")), "0.00"},
		{"plain integer", plain(dec("5")), "5.0"},
		{"plain fraction", plain(dec("0.0590")), "0.059"},
		{"plain negative", plain(dec("-1")), "-1.0"},
		{"plain zero", plain(money.Zero), "0.0"},
		{"amount in EUR", amountText(dec("10"), cur("EUR")), "10.00"},
		{"amount in JPY", amountText(dec("1000"), cur("JPY")), "1000"},
		{"amount has no negative zero", amountText(dec("-0"), cur("EUR")), "0.00"},
		{"rounded units", roundedUnits(dec("0.5"), cur("EUR")).String(), "0.01"},
		{"one item", sentence([]string{"a"}, en), "a"},
		{"two items", sentence([]string{"a", "b"}, en), "a and b"},
		{"three items", sentence([]string{"a", "b", "c"}, en), "a, b, and c"},
		{"titleize", titleize("sepa_debit"), "Sepa Debit"},
		{"titleize empty", titleize(""), ""},
		{"interpolate", interpolate("Payment term %{net_payment_term} days", "net_payment_term", "30"), "Payment term 30 days"},
		{"time", timeText(instant("2025-10-22T12:00:00+02:00")), "2025-10-22 10:00:00 UTC"},
		{"CII date", ciiDate(date("2025-03-05")), "20250305"},
		{"UBL date", ublDate(date("2025-03-05")), "2025-03-05"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got = %q, want = %q", tt.got, tt.want)
			}
		})
	}
}
