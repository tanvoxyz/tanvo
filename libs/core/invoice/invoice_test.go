package invoice

import (
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var testNow = time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

func TestVoidable(t *testing.T) {
	type tc struct {
		Name          string `yaml:"name"`
		Source        string `yaml:"source"`
		Status        string `yaml:"status"`
		PaymentStatus string `yaml:"payment_status"`
		Paid          int64  `yaml:"paid"`
		CreditNote    string `yaml:"credit_note"`
		DisputeLost   bool   `yaml:"dispute_lost"`
		Want          bool   `yaml:"want"`
	}
	for _, c := range spectest.Load[tc](t, "voidable.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			inv := &Invoice{Status: Status(c.Status), PaymentStatus: core.PaymentStatus(c.PaymentStatus), TotalAmount: 1000, TotalPaidAmount: c.Paid}
			if c.DisputeLost {
				inv.PaymentDisputeLostAt = &testNow
			}
			f := VoidFacts{}
			if c.CreditNote == "active" {
				f.NonVoidedCreditNotes = 1
			}
			if got := inv.Voidable(f); got != c.Want {
				t.Errorf("Voidable() got = %v, want = %v (%s)", got, c.Want, c.Source)
			}
			err := inv.Void(f, testNow)
			if got := err == nil; got != c.Want {
				t.Errorf("Void() ok got = %v, want = %v", got, c.Want)
			}
			if c.Want && (inv.Status != StatusVoided || inv.VoidedAt == nil) {
				t.Errorf("Void() status got = %s, want = voided", inv.Status)
			}
			if !c.Want && !errors.Is(err, core.ErrNotAllowed) {
				t.Errorf("Void() error got = %v, want = ErrNotAllowed", err)
			}
		})
	}
}

func TestAmounts(t *testing.T) {
	type tc struct {
		Name                   string `yaml:"name"`
		Source                 string `yaml:"source"`
		Kind                   string `yaml:"kind"`
		Status                 string `yaml:"status"`
		Type                   string `yaml:"type"`
		PaymentStatus          string `yaml:"payment_status"`
		Total                  int64  `yaml:"total"`
		Paid                   int64  `yaml:"paid"`
		Offset                 int64  `yaml:"offset"`
		Creditable             int64  `yaml:"creditable"`
		SubTotalIncludingTaxes int64  `yaml:"sub_total_including_taxes"`
		Credited               int64  `yaml:"credited"`
		Want                   int64  `yaml:"want"`
	}
	for _, c := range spectest.Load[tc](t, "amounts.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			inv := &Invoice{
				Status: StatusFinalized, Type: TypeSubscription, PaymentStatus: core.PaymentStatusPending,
				TotalAmount: c.Total, TotalPaidAmount: c.Paid, OffsettedAmount: c.Offset,
				SubTotalIncludingTaxesAmount: c.SubTotalIncludingTaxes,
			}
			if c.Status != "" {
				inv.Status = Status(c.Status)
			}
			if c.Type != "" {
				inv.Type = Type(c.Type)
			}
			if c.PaymentStatus != "" {
				inv.PaymentStatus = core.PaymentStatus(c.PaymentStatus)
			}
			var got int64
			switch c.Kind {
			case "total_due":
				got = inv.TotalDueAmount()
			case "total_settled":
				got = inv.TotalSettledAmount()
			case "offsettable":
				got = inv.OffsettableAmount(c.Creditable)
			case "creditable":
				got = inv.CreditableAmount(c.Credited)
			default:
				t.Fatalf("kind got = %q, want = a known kind", c.Kind)
			}
			if got != c.Want {
				t.Errorf("%s got = %d, want = %d (%s)", c.Kind, got, c.Want, c.Source)
			}
		})
	}
}

func TestNumbers(t *testing.T) {
	type tc struct {
		Name        string        `yaml:"name"`
		Source      string        `yaml:"source"`
		Kind        string        `yaml:"kind"`
		Prefix      string        `yaml:"prefix"`
		CustomerSeq int64         `yaml:"customer_seq"`
		Seq         int64         `yaml:"seq"`
		FinalizedAt spectest.Time `yaml:"finalized_at"`
		Timezone    string        `yaml:"timezone"`
		Want        string        `yaml:"want"`
	}
	for _, c := range spectest.Load[tc](t, "number.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			var got string
			switch c.Kind {
			case "draft":
				got = DraftNumber(c.Prefix)
			case "per_customer":
				got = PerCustomerNumber(c.Prefix, c.CustomerSeq, c.Seq)
			case "per_billing_entity":
				loc, err := time.LoadLocation(c.Timezone)
				if err != nil {
					t.Fatal(err)
				}
				got = PerBillingEntityNumber(c.Prefix, c.FinalizedAt.Time, loc, c.Seq)
			}
			if got != c.Want {
				t.Errorf("number got = %q, want = %q (%s)", got, c.Want, c.Source)
			}
		})
	}
}

func TestTransitionMatrix(t *testing.T) {
	allowed := map[Status][]Status{
		StatusGenerating: {StatusDraft, StatusOpen, StatusFinalized, StatusClosed, StatusFailed},
		StatusDraft:      {StatusDraft, StatusFinalized},
		StatusOpen:       {StatusFinalized, StatusClosed},
		StatusFailed:     {StatusFinalized},
		StatusFinalized:  {StatusVoided},
	}
	for _, from := range StatusValues() {
		for _, to := range StatusValues() {
			want := false
			for _, s := range allowed[from] {
				want = want || s == to
			}
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) got = %v, want = %v", from, to, got, want)
			}
			inv := &Invoice{Status: from}
			err := inv.Transition(to, testNow)
			if got := err == nil; got != want {
				t.Errorf("Transition(%s → %s) ok got = %v, want = %v", from, to, got, want)
			}
			if !want {
				var te *core.TransitionError
				if !errors.As(err, &te) || inv.Status != from {
					t.Errorf("Transition(%s → %s) error got = %v, status %s, want = *TransitionError, %s", from, to, err, inv.Status, from)
				}
				continue
			}
			if to == StatusFinalized && inv.FinalizedAt == nil {
				t.Errorf("Transition(%s → finalized) FinalizedAt got = nil, want = set", from)
			}
		}
	}
}

func TestComputeTotals(t *testing.T) {
	tests := []struct {
		name string
		fees []int64
		d    Deductions
		want Totals
	}{
		{"no deductions", []int64{600, 400}, Deductions{Taxes: 200}, Totals{FeesAmount: 1000, SubTotalExcludingTaxesAmount: 1000, TaxesAmount: 200, SubTotalIncludingTaxesAmount: 1200, TotalAmount: 1200}},
		{"every deduction", []int64{1000}, Deductions{ProgressiveBillingCredit: 100, Coupons: 200, Taxes: 140, CreditNotes: 40, PrepaidCredit: 300}, Totals{FeesAmount: 1000, ProgressiveBillingCreditAmount: 100, CouponsAmount: 200, SubTotalExcludingTaxesAmount: 700, TaxesAmount: 140, SubTotalIncludingTaxesAmount: 840, CreditNotesAmount: 40, PrepaidCreditAmount: 300, TotalAmount: 500}},
		{"coupons capped", []int64{100}, Deductions{ProgressiveBillingCredit: 60, Coupons: 100}, Totals{FeesAmount: 100, ProgressiveBillingCreditAmount: 60, CouponsAmount: 40}},
		{"prepaid capped", []int64{100}, Deductions{Taxes: 20, CreditNotes: 50, PrepaidCredit: 500}, Totals{FeesAmount: 100, SubTotalExcludingTaxesAmount: 100, TaxesAmount: 20, SubTotalIncludingTaxesAmount: 120, CreditNotesAmount: 50, PrepaidCreditAmount: 70}},
		{"negative inputs", []int64{-50, 100}, Deductions{Coupons: -10, Taxes: -5}, Totals{FeesAmount: 100, SubTotalExcludingTaxesAmount: 100, SubTotalIncludingTaxesAmount: 100, TotalAmount: 100}},
	}
	for _, tt := range tests {
		if got := ComputeTotals(tt.fees, tt.d); got != tt.want {
			t.Errorf("%s: ComputeTotals() got = %+v, want = %+v", tt.name, got, tt.want)
		}
	}
	inv := &Invoice{PaymentStatus: core.PaymentStatusPending}
	inv.ApplyTotals(ComputeTotals([]int64{100}, Deductions{Coupons: 100}))
	if inv.PaymentStatus != core.PaymentStatusSucceeded {
		t.Errorf("ApplyTotals(zero) payment status got = %s, want = succeeded", inv.PaymentStatus)
	}
}

func TestFinalStatus(t *testing.T) {
	tests := []struct {
		f    FinalStatusFacts
		want Status
	}{
		{FinalStatusFacts{UsesGracePeriod: true, GracePeriodDays: 3, FeesAmount: 100}, StatusDraft},
		{FinalStatusFacts{UsesGracePeriod: false, GracePeriodDays: 3, FeesAmount: 100}, StatusFinalized},
		{FinalStatusFacts{Gated: true, TotalAmount: 100, FeesAmount: 100}, StatusOpen},
		{FinalStatusFacts{Gated: true, TotalAmount: 0}, StatusClosed},
		{FinalStatusFacts{TaxesPending: true}, StatusOpen},
		{FinalStatusFacts{FeesAmount: 0, FinalizeZeroAmount: true}, StatusFinalized},
		{FinalStatusFacts{FeesAmount: 0}, StatusClosed},
	}
	for i, tt := range tests {
		if got := FinalStatus(tt.f); got != tt.want {
			t.Errorf("%d: FinalStatus(%+v) got = %s, want = %s", i, tt.f, got, tt.want)
		}
	}
}

func TestIssuingDateOffset(t *testing.T) {
	const (
		end   = core.IssuingDateAnchorCurrentPeriodEnd
		next  = core.IssuingDateAnchorNextPeriodStart
		keep  = core.IssuingDateAdjustmentKeepAnchor
		align = core.IssuingDateAdjustmentAlignWithFinalizationDate
	)
	tests := []struct {
		f    IssuingDateFacts
		want int
	}{
		{IssuingDateFacts{Recurring: true, UsesBillingDate: true, Anchor: end, Adjustment: keep, GracePeriodDays: 3}, 0},
		{IssuingDateFacts{Recurring: false, Anchor: end, Adjustment: keep, GracePeriodDays: 3}, 3},
		{IssuingDateFacts{Recurring: true, Anchor: end, Adjustment: keep, GracePeriodDays: 3}, -1},
		{IssuingDateFacts{Recurring: true, Anchor: end, Adjustment: align, GracePeriodDays: 3}, 3},
		{IssuingDateFacts{Recurring: true, Anchor: end, Adjustment: align}, -1},
		{IssuingDateFacts{Recurring: true, Anchor: next, Adjustment: keep, GracePeriodDays: 3}, 0},
		{IssuingDateFacts{Recurring: true, Anchor: next, Adjustment: align, GracePeriodDays: 3}, 3},
		{IssuingDateFacts{Recurring: true, Anchor: next, Adjustment: align}, 0},
	}
	billing := core.NewDate(2024, time.April, 1)
	for i, tt := range tests {
		if got := IssuingDateOffset(tt.f); got != tt.want {
			t.Errorf("%d: IssuingDateOffset(%+v) got = %d, want = %d", i, tt.f, got, tt.want)
		}
		if got, want := IssuingDate(billing, tt.f), billing.AddDays(tt.want); got != want {
			t.Errorf("%d: IssuingDate() got = %v, want = %v", i, got, want)
		}
	}
	if got, want := PaymentDueDate(billing, 30), core.NewDate(2024, time.May, 1); got != want {
		t.Errorf("PaymentDueDate() got = %v, want = %v", got, want)
	}
	if !KeepsIssuingDateOnFinalization(IssuingDateFacts{Recurring: true, Adjustment: keep}) {
		t.Errorf("KeepsIssuingDateOnFinalization(recurring keep) got = false, want = true")
	}
}

func TestDraftChecks(t *testing.T) {
	today := core.NewDate(2024, time.April, 10)
	tests := []struct {
		name string
		inv  Invoice
		fin  bool
		over bool
	}{
		// spec/models/invoice_spec.rb:3055: without an expected date, the draft is due.
		{"draft without expected date", Invoice{Status: StatusDraft}, true, false},
		{"draft due today", Invoice{Status: StatusDraft, ExpectedFinalizationDate: today}, true, false},
		{"draft due later", Invoice{Status: StatusDraft, ExpectedFinalizationDate: today.AddDays(1)}, false, false},
		{"overdue", Invoice{Status: StatusFinalized, PaymentStatus: core.PaymentStatusPending, PaymentDueDate: today.AddDays(-1)}, false, true},
		{"due today is not overdue", Invoice{Status: StatusFinalized, PaymentStatus: core.PaymentStatusPending, PaymentDueDate: today}, false, false},
		{"paid is not overdue", Invoice{Status: StatusFinalized, PaymentStatus: core.PaymentStatusSucceeded, PaymentDueDate: today.AddDays(-1)}, false, false},
		{"already overdue", Invoice{Status: StatusFinalized, PaymentOverdue: true, PaymentDueDate: today.AddDays(-1)}, false, false},
	}
	for _, tt := range tests {
		if got := tt.inv.FinalizationDue(today); got != tt.fin {
			t.Errorf("%s: FinalizationDue() got = %v, want = %v", tt.name, got, tt.fin)
		}
		if got := tt.inv.OverdueCandidate(today); got != tt.over {
			t.Errorf("%s: OverdueCandidate() got = %v, want = %v", tt.name, got, tt.over)
		}
	}
}

func TestInvoiceValidate(t *testing.T) {
	base := func() *Invoice {
		return &Invoice{Type: TypeSubscription, Status: StatusFinalized, PaymentStatus: core.PaymentStatusPending, Currency: money.MustCurrency("EUR")}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("Validate() got = %v, want = nil", err)
	}
	inv := base()
	inv.Status = StatusDraft
	inv.Metadata = core.Metadata{{Key: "k", Value: "v"}}
	inv.PaymentDisputeLostAt = &testNow
	inv.TotalAmount = -1
	got := map[string]string{}
	for _, fe := range core.FieldErrors(inv.Validate()) {
		got[fe.Field] = fe.Code
	}
	for field, code := range map[string]string{"metadata": core.CodeForbidden, "payment_dispute_lost_at": core.CodeForbidden, "total_amount": core.CodeNegative} {
		if got[field] != code {
			t.Errorf("Validate() %s got = %q, want = %q", field, got[field], code)
		}
	}
	if err := (&Invoice{Status: StatusDraft}).MarkDisputeLost(testNow); !errors.Is(err, core.ErrNotAllowed) {
		t.Errorf("MarkDisputeLost(draft) got = %v, want = ErrNotAllowed", err)
	}
}
