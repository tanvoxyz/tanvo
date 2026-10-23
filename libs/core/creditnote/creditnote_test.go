package creditnote

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var testNow = time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

func codes(err error) []string {
	var out []string
	for _, fe := range core.FieldErrors(err) {
		out = append(out, fe.String())
	}
	return out
}

func TestHelpers(t *testing.T) {
	type tc struct {
		Name         string `yaml:"name"`
		Source       string `yaml:"source"`
		Kind         string `yaml:"kind"`
		Credit       int64  `yaml:"credit"`
		Refund       int64  `yaml:"refund"`
		Offset       int64  `yaml:"offset"`
		Balance      int64  `yaml:"balance"`
		CreditStatus string `yaml:"credit_status"`
		Want         bool   `yaml:"want"`
	}
	for _, c := range spectest.Load[tc](t, "creditnote.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			cn := &CreditNote{CreditAmount: c.Credit, RefundAmount: c.Refund, OffsetAmount: c.Offset, BalanceAmount: c.Balance, CreditStatus: CreditStatus(c.CreditStatus)}
			var got bool
			switch c.Kind {
			case "credited":
				got = cn.Credited()
			case "refunded":
				got = cn.Refunded()
			case "has_offset":
				got = cn.HasOffset()
			case "voidable":
				got = cn.Voidable()
				if err := cn.Void(testNow); (err == nil) != c.Want {
					t.Errorf("Void() error got = %v, want ok = %v", err, c.Want)
				}
				if c.Want && (cn.BalanceAmount != 0 || cn.CreditStatus != CreditStatusVoided || cn.VoidedAt == nil) {
					t.Errorf("Void() got = balance %d, %s, want = 0, voided", cn.BalanceAmount, cn.CreditStatus)
				}
			default:
				t.Fatalf("kind got = %q, want = a known kind", c.Kind)
			}
			if got != c.Want {
				t.Errorf("%s got = %v, want = %v (%s)", c.Kind, got, c.Want, c.Source)
			}
		})
	}
}

func TestNumber(t *testing.T) {
	type tc struct {
		Name          string `yaml:"name"`
		Source        string `yaml:"source"`
		InvoiceNumber string `yaml:"invoice_number"`
		Seq           int64  `yaml:"seq"`
		Want          string `yaml:"want"`
	}
	for _, c := range spectest.Load[tc](t, "number.yaml") {
		if got := Number(c.InvoiceNumber, c.Seq); got != c.Want {
			t.Errorf("%s: Number() got = %q, want = %q (%s)", c.Name, got, c.Want, c.Source)
		}
	}
}

func TestTransitionMatrices(t *testing.T) {
	for _, from := range StatusValues() {
		for _, to := range StatusValues() {
			want := from == StatusDraft && to == StatusFinalized
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) got = %v, want = %v", from, to, got, want)
			}
			cn := &CreditNote{Status: from}
			if err := cn.Finalize(testNow); to == StatusFinalized && (err == nil) != want {
				t.Errorf("Finalize(%s) error got = %v, want ok = %v", from, err, want)
			}
		}
	}
	credit := map[CreditStatus][]CreditStatus{
		CreditStatusAvailable: {CreditStatusConsumed, CreditStatusVoided},
		CreditStatusConsumed:  {CreditStatusAvailable},
	}
	for _, from := range CreditStatusValues() {
		for _, to := range CreditStatusValues() {
			if got, want := CanTransitionCredit(from, to), slices.Contains(credit[from], to); got != want {
				t.Errorf("CanTransitionCredit(%s, %s) got = %v, want = %v", from, to, got, want)
			}
		}
	}
	for _, from := range core.RefundStatusValues() {
		for _, to := range core.RefundStatusValues() {
			want := from == core.RefundStatusPending && to != core.RefundStatusPending
			if got := CanTransitionRefund(from, to); got != want {
				t.Errorf("CanTransitionRefund(%s, %s) got = %v, want = %v", from, to, got, want)
			}
			cn := &CreditNote{RefundStatus: from}
			err := cn.SetRefundStatus(to, testNow)
			var te *core.TransitionError
			if want != (err == nil) || (!want && !errors.As(err, &te)) {
				t.Errorf("SetRefundStatus(%s → %s) error got = %v, want ok = %v", from, to, err, want)
			}
			if want && to == core.RefundStatusSucceeded && cn.RefundedAt == nil {
				t.Errorf("SetRefundStatus(succeeded) RefundedAt got = nil, want = set")
			}
		}
	}
}

func TestBalance(t *testing.T) {
	cn := &CreditNote{CreditAmount: 100, BalanceAmount: 100, CreditStatus: CreditStatusAvailable}
	if n, err := cn.ConsumeBalance(30, testNow); n != 30 || err != nil || cn.BalanceAmount != 70 {
		t.Errorf("ConsumeBalance(30) got = %d, %v, balance %d, want = 30, nil, 70", n, err, cn.BalanceAmount)
	}
	if n, _ := cn.ConsumeBalance(500, testNow); n != 70 || cn.CreditStatus != CreditStatusConsumed {
		t.Errorf("ConsumeBalance(500) got = %d, %s, want = 70, consumed", n, cn.CreditStatus)
	}
	var te *core.TransitionError
	if _, err := cn.ConsumeBalance(1, testNow); !errors.As(err, &te) {
		t.Errorf("ConsumeBalance(consumed) error got = %v, want = *TransitionError", err)
	}
	if err := cn.Void(testNow); !errors.Is(err, core.ErrNotAllowed) {
		t.Errorf("Void(consumed) error got = %v, want = ErrNotAllowed", err)
	}
	if err := cn.Recredit(40, testNow); err != nil || cn.BalanceAmount != 40 || cn.CreditStatus != CreditStatusAvailable {
		t.Errorf("Recredit(40) got = %v, balance %d, %s, want = nil, 40, available", err, cn.BalanceAmount, cn.CreditStatus)
	}
	_ = cn.Void(testNow)
	if err := cn.Recredit(10, testNow); !errors.Is(err, core.ErrNotAllowed) {
		t.Errorf("Recredit(voided) error got = %v, want = ErrNotAllowed", err)
	}
}

func TestCheck(t *testing.T) {
	regular := Facts{ItemsTotal: 100, Creditable: 150, Paid: 60, Offsettable: 80}
	tests := []struct {
		name string
		a    Amounts
		f    Facts
		want []string
	}{
		{"credit only", Amounts{Credit: 100}, regular, nil},
		{"split", Amounts{Credit: 20, Refund: 50, Offset: 30}, regular, nil},
		// spec/services/credit_notes/validate_service_spec.rb:418
		{"zero total", Amounts{}, Facts{}, []string{"base: total_amount_must_be_positive"}},
		{"negative amount", Amounts{Credit: -1, Refund: 2}, regular, []string{"credit_amount: value_is_negative"}},
		{"items mismatch", Amounts{Credit: 90}, regular, []string{"base: does_not_match_item_amounts"}},
		// spec/services/credit_notes/validate_service_spec.rb:132
		{"refund unpaid", Amounts{Refund: 100}, Facts{ItemsTotal: 100, Creditable: 150}, []string{"refund_amount: cannot_refund_unpaid_invoice"}},
		{"refund above paid", Amounts{Refund: 70, Credit: 30}, regular, []string{"refund_amount: higher_than_remaining_invoice_amount"}},
		{"refund above remaining paid", Amounts{Refund: 50, Credit: 50}, Facts{ItemsTotal: 100, Creditable: 150, Paid: 60, AlreadyRefunded: 20}, []string{"refund_amount: higher_than_remaining_invoice_amount"}},
		{"offset above offsettable", Amounts{Offset: 100}, regular, []string{"offset_amount: higher_than_remaining_invoice_amount"}},
		{"above creditable", Amounts{Credit: 100}, Facts{ItemsTotal: 100, Creditable: 99}, []string{"base: higher_than_remaining_invoice_amount"}},
		// spec/services/credit_notes/validate_service_spec.rb:438
		{"credit on credit invoice", Amounts{Credit: 100}, Facts{CreditInvoice: true, ItemsTotal: 100}, []string{"credit_amount: cannot_credit_invoice"}},
		{"refund of credit invoice", Amounts{Refund: 100}, Facts{CreditInvoice: true, ItemsTotal: 100, Paid: 100}, nil},
	}
	for _, tt := range tests {
		if got := codes(Check(tt.a, tt.f)); !slices.Equal(got, tt.want) {
			t.Errorf("%s: Check() got = %v, want = %v", tt.name, got, tt.want)
		}
	}
}

func TestCheckItem(t *testing.T) {
	tests := []struct {
		amount, creditable, wallet int64
		want                       []string
	}{
		{50, 100, -1, nil},
		{100, 100, -1, nil},
		{-1, 100, -1, []string{"amount: invalid_value"}},
		{101, 100, -1, []string{"amount: higher_than_remaining_fee_amount"}}, // spec/services/credit_notes/validate_item_service_spec.rb:81
		{60, 100, 50, []string{"amount: higher_than_wallet_balance"}},        // spec/services/credit_notes/validate_item_service_spec.rb:170
	}
	for _, tt := range tests {
		if got := codes(CheckItem(tt.amount, tt.creditable, tt.wallet)); !slices.Equal(got, tt.want) {
			t.Errorf("CheckItem(%d, %d, %d) got = %v, want = %v", tt.amount, tt.creditable, tt.wallet, got, tt.want)
		}
	}
}

func TestValidateAndTotals(t *testing.T) {
	var gen core.UUIDv7Generator
	cn := &CreditNote{
		InvoiceID: core.NewID[core.InvoiceID](gen), CustomerID: core.NewID[core.CustomerID](gen),
		Status: StatusFinalized, Reason: ReasonOther, Currency: money.MustCurrency("EUR"),
		CreditAmount: 110, BalanceAmount: 110, CreditStatus: CreditStatusAvailable, TotalAmount: 110,
		Items:              []Item{{FeeID: core.NewID[core.FeeID](gen), Amount: 100, PreciseAmount: money.MustParse("100")}},
		PreciseTaxesAmount: money.MustParse("10"),
	}
	if err := cn.Validate(); err != nil {
		t.Fatalf("Validate() got = %v, want = nil", err)
	}
	if got := cn.PreciseTotal(); got.Cmp(money.MustParse("110")) != 0 {
		t.Errorf("PreciseTotal() got = %s, want = 110", got)
	}
	cn.RefundAmount = 5
	cn.BalanceAmount = 200
	if got, want := codes(cn.Validate()), []string{"refund_status: value_is_mandatory", "balance_amount: value_is_out_of_range"}; !slices.Equal(got, want) {
		t.Errorf("Validate() got = %v, want = %v", got, want)
	}
	c := Clone(*cn)
	c.Items[0].Amount = 1
	if cn.Items[0].Amount != 100 {
		t.Errorf("Clone() shares items got = %d, want = 100", cn.Items[0].Amount)
	}
}
