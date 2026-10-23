package creditnotes

import (
	"slices"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// fieldCodesOf returns the codes of err for field.
func fieldCodesOf(err error, field string) []string {
	var out []string
	for _, e := range core.FieldErrors(err) {
		if e.Field == field {
			out = append(out, e.Code)
		}
	}
	return out
}

// note is a finalized earlier credit note of the split.
func note(credit, refund, offset, total int64) *creditnote.CreditNote {
	return &creditnote.CreditNote{
		ID: core.MustParseID[core.CreditNoteID]("0190a6c0-0000-7000-8000-000000000001"), Status: creditnote.StatusFinalized,
		CreditAmount: credit, RefundAmount: refund, OffsetAmount: offset, TotalAmount: total,
	}
}

func TestValidAmounts(t *testing.T) {
	// The lago spec's default: an invoice of fees 100 + taxes 20, a note
	// crediting 12 for an item of 10 with 2 of taxes.
	base := func() *invoice.Invoice {
		return &invoice.Invoice{
			Type: invoice.TypeSubscription, Status: invoice.StatusFinalized, PaymentStatus: core.PaymentStatusPending,
			FeesAmount: 100, TaxesAmount: 20, TotalAmount: 120,
		}
	}
	tests := []struct {
		source  string
		inv     func(*invoice.Invoice)
		notes   []*creditnote.CreditNote
		item    string // precise item amount; "" means 10
		coupons string
		taxes   string // precise taxes; "" means 2
		credit  int64
		refund  int64
		offset  int64
		// field and codes are the errors expected on one field; an empty
		// field means valid.
		field string
		codes []string
	}{
		{source: "spec/services/credit_notes/validate_service_spec.rb:58", credit: 12},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:69", credit: 10, refund: 2,
			inv:   func(i *invoice.Invoice) { i.TotalPaidAmount = 120 },
			field: "refund_amount", codes: []string{creditnote.CodeCannotRefundUnpaidInvoice},
		},
		{source: "spec/services/credit_notes/validate_service_spec.rb:79", credit: 12},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:88", item: "1", credit: 12,
			field: "base", codes: []string{creditnote.CodeDoesNotMatchItemAmounts},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:104", credit: 250,
			inv:   func(i *invoice.Invoice) { i.FeesAmount, i.TaxesAmount, i.TotalAmount = 200, 40, 240 },
			field: "credit_amount", codes: []string{creditnote.CodeHigherThanRemainingInvoiceAmount},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:115", item: "239", credit: 241,
			inv: func(i *invoice.Invoice) { i.FeesAmount, i.TaxesAmount, i.TotalAmount = 200, 40, 240 },
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:128", item: "10", credit: 0, refund: 12,
			field: "refund_amount", codes: []string{creditnote.CodeCannotRefundUnpaidInvoice},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:145", credit: 0, refund: 12,
			inv: func(i *invoice.Invoice) {
				i.PaymentStatus, i.TotalPaidAmount = core.PaymentStatusSucceeded, 10
			},
			field: "refund_amount", codes: []string{creditnote.CodeHigherThanRemainingInvoiceAmount},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:158", credit: 12,
			notes: []*creditnote.CreditNote{note(120, 0, 0, 99)},
			field: "credit_amount", codes: []string{creditnote.CodeHigherThanRemainingInvoiceAmount},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:176", credit: 0, refund: 12,
			inv: func(i *invoice.Invoice) {
				i.PaymentStatus, i.TotalPaidAmount = core.PaymentStatusSucceeded, 5
			},
			notes: []*creditnote.CreditNote{note(0, 199, 0, 119)},
			field: "refund_amount", codes: []string{creditnote.CodeHigherThanRemainingInvoiceAmount},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:195", credit: 12,
			notes: []*creditnote.CreditNote{note(86, 33, 0, 119)},
			field: "base", codes: []string{creditnote.CodeHigherThanRemainingInvoiceAmount},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:224", item: "20", coupons: "2", taxes: "3.6", credit: 22,
			inv: func(i *invoice.Invoice) {
				i.FeesAmount, i.CouponsAmount, i.TaxesAmount, i.TotalAmount = 100, 10, 18, 108
				i.PaymentStatus = core.PaymentStatusSucceeded
			},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:238", item: "50", coupons: "5", taxes: "9", credit: 54,
			inv: func(i *invoice.Invoice) {
				i.FeesAmount, i.CouponsAmount, i.TaxesAmount, i.TotalAmount = 100, 10, 18, 108
				i.PaymentStatus = core.PaymentStatusSucceeded
			},
			notes: []*creditnote.CreditNote{note(54, 0, 0, 54)},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:246", item: "62", coupons: "5", taxes: "9", credit: 66,
			inv: func(i *invoice.Invoice) {
				i.FeesAmount, i.CouponsAmount, i.TaxesAmount, i.TotalAmount = 100, 10, 18, 108
				i.PaymentStatus = core.PaymentStatusSucceeded
			},
			notes: []*creditnote.CreditNote{note(54, 0, 0, 54)},
			field: "credit_amount", codes: []string{creditnote.CodeHigherThanRemainingInvoiceAmount},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:256", item: "1", coupons: "2", taxes: "3.6", credit: 22,
			inv: func(i *invoice.Invoice) {
				i.FeesAmount, i.CouponsAmount, i.TaxesAmount, i.TotalAmount = 100, 10, 18, 108
				i.PaymentStatus = core.PaymentStatusSucceeded
			},
			field: "base", codes: []string{creditnote.CodeDoesNotMatchItemAmounts},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:271", item: "0", taxes: "0",
			field: "base", codes: []string{creditnote.CodeTotalAmountMustBePositive},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:305", item: "19333", taxes: "4833.3333", credit: 24166,
			inv: func(i *invoice.Invoice) {
				i.FeesAmount, i.TaxesAmount, i.TotalAmount, i.TotalPaidAmount = 20000, 5000, 25000, 25000
			},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:319", item: "239", credit: 241,
			inv: func(i *invoice.Invoice) { i.FeesAmount, i.TaxesAmount, i.TotalAmount = 200, 40, 240 },
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:341", item: "10", taxes: "0", credit: 100,
			notes: []*creditnote.CreditNote{note(0, 0, 22, 120)},
			field: "credit_amount", codes: []string{creditnote.CodeHigherThanRemainingInvoiceAmount},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:359", item: "50", taxes: "10", offset: 60,
			notes: []*creditnote.CreditNote{note(60, 0, 0, 60)},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:367", item: "50", taxes: "11", offset: 61,
			notes: []*creditnote.CreditNote{note(60, 0, 0, 60)},
			field: "offset_amount", codes: []string{creditnote.CodeHigherThanRemainingInvoiceAmount},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:383", item: "10", offset: 24,
			inv: func(i *invoice.Invoice) {
				i.FeesAmount, i.TaxesAmount, i.TotalAmount, i.TotalPaidAmount = 0, 0, 12, 12
			},
			field: "offset_amount", codes: []string{creditnote.CodeHigherThanRemainingInvoiceAmount},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:402", item: "50", taxes: "0", credit: 10, refund: 20, offset: 20,
			inv: func(i *invoice.Invoice) {
				i.PaymentStatus, i.TotalPaidAmount = core.PaymentStatusSucceeded, 50
			},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:414", item: "0", taxes: "0",
			field: "base", codes: []string{creditnote.CodeTotalAmountMustBePositive},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:434", credit: 12,
			inv: func(i *invoice.Invoice) {
				i.Type, i.FeesAmount, i.TaxesAmount, i.TotalAmount, i.TotalPaidAmount = invoice.TypeCredit, 0, 0, 12, 12
				i.PaymentStatus = core.PaymentStatusSucceeded
			},
			field: "credit_amount", codes: []string{creditnote.CodeCannotCreditInvoice},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:446", offset: 7,
			inv: func(i *invoice.Invoice) {
				i.Type, i.FeesAmount, i.TaxesAmount, i.TotalAmount, i.TotalPaidAmount = invoice.TypeCredit, 0, 0, 12, 12
				i.PaymentStatus = core.PaymentStatusSucceeded
			},
			field: "offset_amount", codes: []string{CodeCannotApplyToPaidInvoice},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:458", offset: 12,
			inv: func(i *invoice.Invoice) {
				i.Type, i.FeesAmount, i.TaxesAmount, i.TotalAmount, i.TotalPaidAmount = invoice.TypeCredit, 0, 0, 12, 12
				i.PaymentStatus = core.PaymentStatusSucceeded
			},
			field: "offset_amount", codes: []string{CodeCannotApplyToPaidInvoice},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:482", offset: 7,
			inv: func(i *invoice.Invoice) {
				i.Type, i.FeesAmount, i.TaxesAmount, i.TotalAmount = invoice.TypeCredit, 10, 2, 12
			},
			field: "offset_amount", codes: []string{CodeNotEqualToTotalAmount},
		},
		{
			source: "spec/services/credit_notes/validate_service_spec.rb:494", offset: 12,
			inv: func(i *invoice.Invoice) {
				i.Type, i.FeesAmount, i.TaxesAmount, i.TotalAmount = invoice.TypeCredit, 10, 2, 12
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.source[strings.LastIndex(tt.source, "/")+1:], func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			inv := base()
			if tt.inv != nil {
				tt.inv(inv)
			}
			st := &invoiceState{inv: inv, notes: tt.notes, credited: map[core.FeeID]int64{}}
			cn := &creditnote.CreditNote{
				Items:                          []creditnote.Item{{PreciseAmount: money.MustParse(or(tt.item, "10"))}},
				PreciseCouponsAdjustmentAmount: money.MustParse(or(tt.coupons, "0")),
				PreciseTaxesAmount:             money.MustParse(or(tt.taxes, "2")),
				CreditAmount:                   tt.credit, RefundAmount: tt.refund, OffsetAmount: tt.offset,
			}
			err := st.validAmounts(cn)
			if tt.field == "" {
				if err != nil {
					t.Fatalf("got = %v, want = nil", err)
				}
				return
			}
			if got := fieldCodesOf(err, tt.field); !slices.Equal(got, tt.codes) {
				t.Fatalf("%s: got = %v (%v), want = %v", tt.field, got, err, tt.codes)
			}
		})
	}
}

func TestValidItem(t *testing.T) {
	type walletSpec struct {
		balance   int64
		remaining *int64
		traceable bool
	}
	n := func(v int64) *int64 { return &v }
	tests := []struct {
		source   string
		credit   bool
		status   core.PaymentStatus
		fee      int64
		credited int64
		wallet   *walletSpec
		amount   int64
		codes    []string
	}{
		{source: "spec/services/credit_notes/validate_item_service_spec.rb:36", fee: 100, amount: 10},
		{source: "spec/services/credit_notes/validate_item_service_spec.rb:54", fee: 100, amount: -3, codes: []string{core.CodeInvalid}},
		{source: "spec/services/credit_notes/validate_item_service_spec.rb:65", fee: 100, amount: 0},
		{source: "spec/services/credit_notes/validate_item_service_spec.rb:77", fee: 100, amount: 110, codes: []string{creditnote.CodeHigherThanRemainingFeeAmount}},
		{source: "spec/services/credit_notes/validate_item_service_spec.rb:91", fee: 100, credited: 99, amount: 10, codes: []string{creditnote.CodeHigherThanRemainingFeeAmount}},
		{source: "spec/services/credit_notes/validate_item_service_spec.rb:100", fee: 100, amount: 20},
		{source: "spec/services/credit_notes/validate_item_service_spec.rb:107", fee: 100, amount: 50},
		{source: "spec/services/credit_notes/validate_item_service_spec.rb:114", fee: 100, amount: 15},
		{source: "spec/services/credit_notes/validate_item_service_spec.rb:121", fee: 100, amount: 30},
		{
			source: "spec/services/credit_notes/validate_item_service_spec.rb:136", credit: true, status: core.PaymentStatusPending,
			fee: 1000, wallet: &walletSpec{balance: 1000, remaining: n(1000), traceable: true}, amount: 1000,
		},
		{
			source: "spec/services/credit_notes/validate_item_service_spec.rb:144", credit: true, status: core.PaymentStatusFailed,
			fee: 1000, wallet: &walletSpec{balance: 1000, remaining: n(1000), traceable: true}, amount: 1000,
		},
		{
			source: "spec/services/credit_notes/validate_item_service_spec.rb:152", credit: true, status: core.PaymentStatusSucceeded,
			fee: 500, wallet: &walletSpec{balance: 1000, remaining: n(500), traceable: true}, amount: 500,
		},
		{
			source: "spec/services/credit_notes/validate_item_service_spec.rb:162", credit: true, status: core.PaymentStatusSucceeded,
			fee: 2000, wallet: &walletSpec{balance: 1000, remaining: n(800), traceable: true}, amount: 1500,
			codes: []string{creditnote.CodeHigherThanWalletBalance},
		},
		{
			source: "spec/services/credit_notes/validate_item_service_spec.rb:177", credit: true, status: core.PaymentStatusSucceeded,
			fee: 1000, wallet: &walletSpec{balance: 1000}, amount: 1000,
		},
		{
			source: "spec/services/credit_notes/validate_item_service_spec.rb:183", credit: true, status: core.PaymentStatusSucceeded,
			fee: 2000, wallet: &walletSpec{balance: 1000}, amount: 1500,
			codes: []string{creditnote.CodeHigherThanWalletBalance},
		},
	}
	for _, tt := range tests {
		t.Run(tt.source[strings.LastIndex(tt.source, ":")+1:], func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			inv := &invoice.Invoice{Type: invoice.TypeSubscription, Status: invoice.StatusFinalized, PaymentStatus: tt.status, TotalAmount: 120}
			f := &invoice.Fee{ID: core.MustParseID[core.FeeID]("0190a6c0-0000-7000-8000-000000000002"), Type: core.FeeTypeSubscription, Amount: tt.fee}
			st := &invoiceState{inv: inv, fees: []*invoice.Fee{f}, credited: map[core.FeeID]int64{f.ID: tt.credited}}
			if tt.credit {
				inv.Type, f.Type = invoice.TypeCredit, core.FeeTypeCredit
				st.wallet = &wallet.Wallet{Status: wallet.StatusActive, BalanceAmount: tt.wallet.balance, Traceable: tt.wallet.traceable}
				st.walletTx = &wallet.Transaction{RemainingAmount: tt.wallet.remaining}
			}
			err := st.validItem(0, tt.amount, f)
			if got := fieldCodesOf(err, "items[0].amount"); !slices.Equal(got, tt.codes) {
				t.Fatalf("got = %v (%v), want = %v", got, err, tt.codes)
			}
		})
	}
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
