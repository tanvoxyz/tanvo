package invoice

import (
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestFeeHelpers(t *testing.T) {
	type tc struct {
		Name        string           `yaml:"name"`
		Source      string           `yaml:"source"`
		Kind        string           `yaml:"kind"`
		FeeType     string           `yaml:"fee_type"`
		Amount      int64            `yaml:"amount"`
		Units       spectest.Decimal `yaml:"units"`
		EventsCount int64            `yaml:"events_count"`
		Coupons     spectest.Decimal `yaml:"coupons"`
		Credited    int64            `yaml:"credited"`
		Wallet      int64            `yaml:"wallet"`
		Want        any              `yaml:"want"`
	}
	for _, c := range spectest.Load[tc](t, "fee.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			f := &Fee{Type: core.FeeType(c.FeeType), Amount: c.Amount, Units: c.Units.Decimal, EventsCount: c.EventsCount, PreciseCouponsAmount: c.Coupons.Decimal}
			var got any
			switch c.Kind {
			case "non_zero":
				got = f.NonZero()
			case "taxable":
				got = f.Taxable()
			case "creditable":
				got = int(f.CreditableAmount(c.Credited, c.Wallet))
			default:
				t.Fatalf("kind got = %q, want = a known kind", c.Kind)
			}
			if got != c.Want {
				t.Errorf("%s got = %v, want = %v (%s)", c.Kind, got, c.Want, c.Source)
			}
		})
	}
}

func TestClamp(t *testing.T) {
	if got := ClampAmount(-5); got != 0 {
		t.Errorf("ClampAmount(-5) got = %d, want = 0", got)
	}
	if got := ClampAmount(5); got != 5 {
		t.Errorf("ClampAmount(5) got = %d, want = 5", got)
	}
	if got := ClampPreciseAmount(money.MustParse("-0.5")); !got.IsZero() {
		t.Errorf("ClampPreciseAmount(-0.5) got = %s, want = 0", got)
	}
}

func TestCloneFee(t *testing.T) {
	f := Fee{GroupedBy: map[string]string{"a": "1"}, AppliedTaxes: []FeeAppliedTax{{TaxCode: "vat"}}, PricingUnitUsage: &PricingUnitUsage{ShortName: "CR"}}
	c := CloneFee(f)
	c.GroupedBy["a"] = "2"
	c.AppliedTaxes[0].TaxCode = "gst"
	c.PricingUnitUsage.ShortName = "XX"
	if f.GroupedBy["a"] != "1" || f.AppliedTaxes[0].TaxCode != "vat" || f.PricingUnitUsage.ShortName != "CR" {
		t.Errorf("CloneFee() shares storage got = %+v, want = independent copy", f)
	}
}

func TestSettlementValidate(t *testing.T) {
	var gen core.UUIDv7Generator
	inv := core.NewID[core.InvoiceID](gen)
	pay := core.NewID[core.PaymentID](gen)
	cn := core.NewID[core.CreditNoteID](gen)
	eur := money.MustCurrency("EUR")
	tests := []struct {
		name string
		s    Settlement
		want []string
	}{
		{"payment", Settlement{InvoiceID: inv, Type: SettlementTypePayment, PaymentID: pay, Amount: 10, Currency: eur}, nil},
		// spec/models/invoice_settlement_spec.rb:38
		{"payment without payment", Settlement{InvoiceID: inv, Type: SettlementTypePayment, Amount: 10, Currency: eur}, []string{"source_payment_id: value_is_mandatory"}},
		// spec/models/invoice_settlement_spec.rb:45
		{"payment with credit note", Settlement{InvoiceID: inv, Type: SettlementTypePayment, PaymentID: pay, CreditNoteID: cn, Amount: 10, Currency: eur}, []string{"source_credit_note_id: value_not_allowed"}},
		// spec/models/invoice_settlement_spec.rb:53
		{"credit note without credit note", Settlement{InvoiceID: inv, Type: SettlementTypeCreditNote, Amount: 10, Currency: eur}, []string{"source_credit_note_id: value_is_mandatory"}},
		// spec/models/invoice_settlement_spec.rb:60
		{"credit note with payment", Settlement{InvoiceID: inv, Type: SettlementTypeCreditNote, CreditNoteID: cn, PaymentID: pay, Amount: 10, Currency: eur}, []string{"source_payment_id: value_not_allowed"}},
	}
	for _, tt := range tests {
		var got []string
		for _, fe := range core.FieldErrors(tt.s.Validate()) {
			got = append(got, fe.String())
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("%s: Validate() got = %v, want = %v", tt.name, got, tt.want)
		}
	}
}

func TestResolveCustomSections(t *testing.T) {
	var gen core.UUIDv7Generator
	a, b, c := core.NewID[core.InvoiceCustomSectionID](gen), core.NewID[core.InvoiceCustomSectionID](gen), core.NewID[core.InvoiceCustomSectionID](gen)
	def := []core.InvoiceCustomSectionID{c}
	tests := []struct {
		name     string
		sub, cus SectionSelection
		want     []core.InvoiceCustomSectionID
	}{
		{"billing entity defaults", SectionSelection{}, SectionSelection{}, def},
		{"customer selection", SectionSelection{}, SectionSelection{SectionIDs: []core.InvoiceCustomSectionID{b}}, []core.InvoiceCustomSectionID{b}},
		{"subscription selection wins", SectionSelection{SectionIDs: []core.InvoiceCustomSectionID{a}}, SectionSelection{SectionIDs: []core.InvoiceCustomSectionID{b}}, []core.InvoiceCustomSectionID{a}},
		{"customer skip", SectionSelection{SectionIDs: []core.InvoiceCustomSectionID{a}}, SectionSelection{Skip: true}, nil},
		{"subscription skip", SectionSelection{Skip: true}, SectionSelection{}, nil},
	}
	for _, tt := range tests {
		if got := ResolveCustomSections(tt.sub, tt.cus, def); !slices.Equal(got, tt.want) {
			t.Errorf("%s: ResolveCustomSections() got = %v, want = %v", tt.name, got, tt.want)
		}
	}
}

func TestSubscriptionKey(t *testing.T) {
	var gen core.UUIDv7Generator
	sub := core.NewID[core.SubscriptionID](gen)
	from := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	s := Subscription{SubscriptionID: sub, Reason: InvoicingReasonSubscriptionPeriodic}
	s.Period.From = from
	if got, ok := s.Key(); !ok || got != (IdempotencyKey{SubscriptionID: sub, Reason: InvoicingReasonSubscriptionPeriodic, PeriodStart: from}) {
		t.Errorf("Key() got = %+v, %v, want = periodic key of %s", got, ok, sub)
	}
	for _, r := range InvoicingReasonValues() {
		if got, want := r.Periodic(), r != InvoicingReasonInAdvanceCharge; got != want {
			t.Errorf("%s.Periodic() got = %v, want = %v", r, got, want)
		}
	}
}

func TestGroupUnbilled(t *testing.T) {
	var gen core.UUIDv7Generator
	s1, s2 := core.NewID[core.SubscriptionID](gen), core.NewID[core.SubscriptionID](gen)
	eur := money.MustCurrency("EUR")
	mar := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	apr := time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)
	fee := func(s core.SubscriptionID, from time.Time) *Fee {
		return &Fee{SubscriptionID: s, Currency: eur, Properties: FeeProperties{ChargesFrom: from}}
	}
	groups := GroupUnbilled([]*Fee{fee(s1, mar), fee(s2, mar), fee(s1, mar), fee(s1, apr)})
	got := []int{}
	for _, g := range groups {
		got = append(got, len(g.Fees))
	}
	if want := []int{2, 1, 1}; !slices.Equal(got, want) {
		t.Errorf("GroupUnbilled() sizes got = %v, want = %v", got, want)
	}
}
