package invoice

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestAppliedTaxTaxableAmount(t *testing.T) {
	tax := core.MustParseID[core.TaxID]("6f1b3c1e-7f0a-4a55-9c9c-0d5f3f1d2a10")
	for _, c := range []struct {
		name     string
		at       AppliedTax
		taxable  int64
		provider bool
	}{
		// source: spec/models/invoice/applied_tax_spec.rb:12, :50
		{name: "no base falls back to the fees amount", at: AppliedTax{TaxID: tax, FeesAmount: 1000}, taxable: 1000},
		{name: "base wins", at: AppliedTax{FeesAmount: 1000, TaxableBaseAmount: 600}, taxable: 600, provider: true},
		{name: "local tax with a base is not a provider tax", at: AppliedTax{TaxID: tax, FeesAmount: 1000, TaxableBaseAmount: 600}, taxable: 600},
		{name: "deleted local tax without a base is not a provider tax", at: AppliedTax{FeesAmount: 1000}, taxable: 1000},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.at.TaxableAmount(); got != c.taxable {
				t.Errorf("taxable amount: got = %d, want = %d", got, c.taxable)
			}
			if got := c.at.IsProviderTax(); got != c.provider {
				t.Errorf("provider tax: got = %t, want = %t", got, c.provider)
			}
		})
	}
}

func TestCreditValidate(t *testing.T) {
	inv := core.MustParseID[core.InvoiceID]("6f1b3c1e-7f0a-4a55-9c9c-0d5f3f1d2a11")
	coupon := core.MustParseID[core.AppliedCouponID]("6f1b3c1e-7f0a-4a55-9c9c-0d5f3f1d2a12")
	note := core.MustParseID[core.CreditNoteID]("6f1b3c1e-7f0a-4a55-9c9c-0d5f3f1d2a13")
	usd := money.MustCurrency("USD")
	for _, c := range []struct {
		name string
		c    Credit
		kind CreditKind
		ok   bool
	}{
		{name: "coupon", c: Credit{InvoiceID: inv, AppliedCouponID: coupon, Amount: 10, Currency: usd}, kind: CreditKindCoupon, ok: true},
		{name: "credit note", c: Credit{InvoiceID: inv, CreditNoteID: note, Amount: 10, Currency: usd}, kind: CreditKindCreditNote, ok: true},
		{name: "two sources", c: Credit{InvoiceID: inv, AppliedCouponID: coupon, CreditNoteID: note, Currency: usd}, kind: CreditKindCoupon},
		{name: "no source", c: Credit{InvoiceID: inv, Currency: usd}, kind: CreditKindProgressiveBilling},
		{name: "negative amount", c: Credit{InvoiceID: inv, AppliedCouponID: coupon, Amount: -1, Currency: usd}, kind: CreditKindCoupon},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.c.Kind(); got != c.kind {
				t.Errorf("kind: got = %v, want = %v", got, c.kind)
			}
			if err := c.c.Validate(); (err == nil) != c.ok {
				t.Errorf("validate: got = %v, want ok = %t", err, c.ok)
			}
		})
	}
}

func TestAdjustedFeeMatches(t *testing.T) {
	from := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)
	a := AdjustedFee{ChargesFrom: from, ChargesTo: to.Add(-time.Microsecond), FixedChargesFrom: from, FixedChargesTo: to}
	// lago compares the boundaries at millisecond precision (iso8601(3)).
	if a.MatchesCharges(from, to) {
		t.Errorf("matches charges: got = true, want = false for a period ending a millisecond later")
	}
	if !a.MatchesCharges(from, to.Add(-time.Nanosecond)) {
		t.Errorf("matches charges: got = false, want = true within a millisecond")
	}
	if !a.MatchesFixedCharges(from, to) || a.MatchesFixedCharges(from.Add(time.Second), to) {
		t.Errorf("matches fixed charges: got = wrong, want = only the same period")
	}
}

func TestFeeRefsNames(t *testing.T) {
	display := "Monthly minimum"
	plan := &catalog.Plan{Name: "Premium", Code: "premium"}
	for _, c := range []struct {
		name string
		refs FeeRefs
		fee  Fee
		want string
	}{
		{name: "display name wins", refs: FeeRefs{Plan: plan}, fee: Fee{Type: core.FeeTypeCommitment, InvoiceDisplayName: &display}, want: display},
		{name: "credit without a transaction", fee: Fee{Type: core.FeeTypeCredit}, want: "credit"},
		{name: "charge without a charge uses the metric", refs: FeeRefs{Metric: &catalog.BillableMetric{Name: "API calls"}}, fee: Fee{Type: core.FeeTypeCharge}, want: "API calls"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.refs.InvoiceName(&c.fee); got != c.want {
				t.Errorf("invoice name: got = %q, want = %q", got, c.want)
			}
		})
	}
	if got := (FeeRefs{Plan: plan}).ItemCode(&Fee{Type: core.FeeTypeSubscription}); got != "premium" {
		t.Errorf("item code: got = %q, want = %q", got, "premium")
	}
	f := Fee{Type: core.FeeTypeCharge, GroupedBy: map[string]string{"region": "eu", "a": "x", "cloud": ""}}
	if got := GroupedByDisplay(&f); got != " • x • eu" {
		t.Errorf("grouped by display: got = %q, want = %q", got, " • x • eu")
	}
}
