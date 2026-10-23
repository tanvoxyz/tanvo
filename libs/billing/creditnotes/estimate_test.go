package creditnotes_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

// estimateFixture is the lago estimate_service_spec invoice: two fees of
// 10 with 5 of coupons each, taxed at 20% (total 12, 9 paid).
func estimateFixture(t *testing.T) *fixture {
	t.Helper()
	h, svc := newHarness(t)
	f := &fixture{h: h, svc: svc, cust: h.Customer(), tax: h.Tax("vat", "20")}
	f.inv, f.fees = h.CreditableInvoice(f.cust, []int64{10, 10}, []*catalog.Tax{f.tax},
		[]func(*invoice.Invoice){func(inv *invoice.Invoice) { inv.TotalPaidAmount = 9 }}, billingtest.Coupons("5"))
	return f
}

func TestEstimate(t *testing.T) {
	tests := []struct {
		name   string
		source string
		setup  func(t *testing.T) (*fixture, []creditnotes.ItemInput)
		check  func(t *testing.T, f *fixture, cn *creditnote.CreditNote, err error)
	}{
		{
			name:   "estimates the credit and refund amount",
			source: "spec/services/credit_notes/estimate_service_spec.rb:61",
			setup: func(t *testing.T) (*fixture, []creditnotes.ItemInput) {
				f := estimateFixture(t)
				return f, []creditnotes.ItemInput{item(f.fees[0], "10"), item(f.fees[1], "5")}
			},
			check: func(t *testing.T, f *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "invoice", cn.InvoiceID, f.inv.ID)
				eq(t, "customer", cn.CustomerID, f.cust.ID)
				eq(t, "sub total", cn.SubTotalExcludingTaxesAmount, 8)
				eq(t, "credit", cn.CreditAmount, 9)
				eq(t, "refund", cn.RefundAmount, 9)
				eq(t, "coupons adjustment", cn.CouponsAdjustmentAmount, 8)
				eq(t, "taxes", cn.TaxesAmount, 1)
				eq(t, "total", cn.TotalAmount, 9)
				decEq(t, "taxes rate", cn.TaxesRate, "20")
				eq(t, "applied taxes", len(cn.AppliedTaxes), 1)
				eq(t, "items", len(cn.Items), 2)
				eq(t, "item 1", cn.Items[0].Amount, 10)
				eq(t, "item 2", cn.Items[1].Amount, 5)
				eq(t, "stored", len(f.h.CreditNotes(f.inv.ID)), 0)
			},
		},
		{
			name:   "invalid items fail",
			source: "spec/services/credit_notes/estimate_service_spec.rb:113",
			setup: func(t *testing.T) (*fixture, []creditnotes.ItemInput) {
				f := estimateFixture(t)
				return f, []creditnotes.ItemInput{item(f.fees[0], "10"), item(f.fees[1], "15")}
			},
			check: func(t *testing.T, _ *fixture, _ *creditnote.CreditNote, err error) {
				wantFields(t, err, "items[1].amount:"+creditnote.CodeHigherThanRemainingFeeAmount)
			},
		},
		{
			name:   "a fee tax without invoice tax fails",
			source: "spec/services/credit_notes/estimate_service_spec.rb:130",
			setup: func(t *testing.T) (*fixture, []creditnotes.ItemInput) {
				f := estimateFixture(t)
				f.h.Do(func(ctx context.Context, r port.Repos) error {
					return r.InvoiceAppliedTaxes.ReplaceForInvoice(ctx, f.h.OrgID(), f.inv.ID, nil)
				})
				return f, []creditnotes.ItemInput{item(f.fees[0], "10"), item(f.fees[1], "5")}
			},
			check: func(t *testing.T, _ *fixture, _ *creditnote.CreditNote, err error) {
				wantRule(t, err, creditnotes.CodeInvoiceAppliedTaxNotFound)
			},
		},
		{
			name:   "missing items fail",
			source: "spec/services/credit_notes/estimate_service_spec.rb:141",
			setup:  func(t *testing.T) (*fixture, []creditnotes.ItemInput) { return estimateFixture(t), nil },
			check: func(t *testing.T, _ *fixture, _ *creditnote.CreditNote, err error) {
				wantFields(t, err, "items:"+core.CodeRequired)
			},
		},
		{
			name:   "rounding adjustment",
			source: "spec/services/credit_notes/estimate_service_spec.rb:218",
			setup: func(t *testing.T) (*fixture, []creditnotes.ItemInput) {
				h, svc := newHarness(t)
				f := &fixture{h: h, svc: svc, cust: h.Customer(), tax: h.Tax("vat", "25")}
				f.inv, f.fees = h.CreditableInvoice(f.cust, []int64{20000}, []*catalog.Tax{f.tax}, nil)
				return f, []creditnotes.ItemInput{item(f.fees[0], "19333")}
			},
			check: func(t *testing.T, _ *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "sub total", cn.SubTotalExcludingTaxesAmount, 19333)
				eq(t, "credit", cn.CreditAmount, 24166)
				eq(t, "refund", cn.RefundAmount, 24166)
				eq(t, "coupons adjustment", cn.CouponsAdjustmentAmount, 0)
				eq(t, "taxes", cn.TaxesAmount, 4833)
				decEq(t, "taxes rate", cn.TaxesRate, "25")
			},
		},
		{
			name:   "an unknown invoice is not found",
			source: "spec/services/credit_notes/estimate_service_spec.rb:241",
			setup: func(t *testing.T) (*fixture, []creditnotes.ItemInput) {
				f := estimateFixture(t)
				f.inv = &invoice.Invoice{ID: core.NewID[core.InvoiceID](f.h.IDs)}
				return f, nil
			},
			check: func(t *testing.T, _ *fixture, _ *creditnote.CreditNote, err error) {
				if !errors.Is(err, core.ErrNotFound) {
					t.Fatalf("got = %v, want = %v", err, core.ErrNotFound)
				}
			},
		},
		{
			name:   "a credit invoice refunds within the wallet",
			source: "spec/services/credit_notes/estimate_service_spec.rb:286",
			setup: func(t *testing.T) (*fixture, []creditnotes.ItemInput) {
				f := estimateCreditFixture(t, wallet.StatusActive)
				return f, []creditnotes.ItemInput{item(f.fees[0], "50")}
			},
			check: func(t *testing.T, _ *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "sub total", cn.SubTotalExcludingTaxesAmount, 50)
				eq(t, "credit", cn.CreditAmount, 0)
				eq(t, "refund", cn.RefundAmount, 50)
				eq(t, "coupons adjustment", cn.CouponsAdjustmentAmount, 0)
				eq(t, "taxes", cn.TaxesAmount, 0)
				decEq(t, "taxes rate", cn.TaxesRate, "0")
			},
		},
		{
			name:   "a credit invoice item above the wallet fails",
			source: "spec/services/credit_notes/estimate_service_spec.rb:313",
			setup: func(t *testing.T) (*fixture, []creditnotes.ItemInput) {
				f := estimateCreditFixture(t, wallet.StatusActive)
				return f, []creditnotes.ItemInput{item(f.fees[0], "150")}
			},
			check: func(t *testing.T, _ *fixture, _ *creditnote.CreditNote, err error) {
				wantFields(t, err, "items[0].amount:"+creditnote.CodeHigherThanWalletBalance)
			},
		},
		{
			name:   "a credit invoice of a terminated wallet fails",
			source: "spec/services/credit_notes/estimate_service_spec.rb:332",
			setup: func(t *testing.T) (*fixture, []creditnotes.ItemInput) {
				f := estimateCreditFixture(t, wallet.StatusTerminated)
				return f, []creditnotes.ItemInput{item(f.fees[0], "50")}
			},
			check: func(t *testing.T, _ *fixture, _ *creditnote.CreditNote, err error) {
				wantRule(t, err, creditnotes.CodeInvalidTypeOrStatus)
			},
		},
		{
			name:   "a fee refunded in full leaves the other fee's own tax",
			source: "spec/services/credit_notes/estimate_service_native_tax_rounding_spec.rb:58",
			setup: func(t *testing.T) (*fixture, []creditnotes.ItemInput) {
				h, svc := newHarness(t)
				f := &fixture{h: h, svc: svc, cust: h.Customer()}
				taxes := []*catalog.Tax{h.Tax("state", "5"), h.Tax("city", "5")}
				f.inv, f.fees = h.CreditableInvoice(f.cust, []int64{10, 10}, taxes, nil)
				in := f.input(0, 11, item(f.fees[0], "10"))
				if _, err := svc.Create(h.Ctx, in); err != nil {
					t.Fatalf("first credit note: got = %v, want = nil", err)
				}
				return f, []creditnotes.ItemInput{item(f.fees[1], "10")}
			},
			check: func(t *testing.T, _ *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "refund", cn.RefundAmount, 11)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			f, items := tt.setup(t)
			cn, err := f.svc.Estimate(f.h.Ctx, f.h.OrgID(), f.inv.ID, items)
			tt.check(t, f, cn, err)
		})
	}
}

// estimateCreditFixture is a credit invoice of 200, paid 200 then 12 by hand, buying credits in
// a wallet with 100 left.
func estimateCreditFixture(t *testing.T, status wallet.Status) *fixture {
	t.Helper()
	h, svc := newHarness(t)
	f := &fixture{h: h, svc: svc, cust: h.Customer()}
	w := h.Wallet(f.cust, "1", func(w *wallet.Wallet) { w.Status = status })
	tx := h.InboundTransaction(w, wallet.TransactionStatusPurchased, "2")
	f.inv, f.fees = h.CreditableInvoice(f.cust, []int64{200}, nil,
		[]func(*invoice.Invoice){func(inv *invoice.Invoice) { inv.Type, inv.TotalPaidAmount = invoice.TypeCredit, 212 }},
		billingtest.CreditFee(tx.ID))
	return f
}
