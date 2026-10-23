package creditnotes_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// voider records the credit notes whose wallet credits it voids.
type voider struct{ notes []*creditnote.CreditNote }

func (v *voider) VoidCreditNoteCredits(_ context.Context, _ port.Repos, _ *wallet.Wallet, _ *wallet.Transaction, cn *creditnote.CreditNote) error {
	v.notes = append(v.notes, cn)
	return nil
}

func TestCreate(t *testing.T) {
	tests := []struct {
		name   string
		source string
		// in builds the input; nil uses items 10 and 5, credit 12, refund 6.
		in    func(t *testing.T, f *fixture) creditnotes.CreateInput
		check func(t *testing.T, f *fixture, cn *creditnote.CreditNote, err error)
	}{
		{
			name:   "creates a credit note",
			source: "spec/services/credit_notes/create_service_spec.rb:73",
			check: func(t *testing.T, f *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "invoice", cn.InvoiceID, f.inv.ID)
				eq(t, "customer", cn.CustomerID, f.cust.ID)
				eq(t, "issuing date", cn.IssuingDate, core.DateOf(now, time.UTC))
				eq(t, "coupons adjustment", cn.CouponsAdjustmentAmount, 0)
				eq(t, "taxes", cn.TaxesAmount, 3)
				decEq(t, "taxes rate", cn.TaxesRate, "20")
				eq(t, "applied taxes", len(cn.AppliedTaxes), 1)
				eq(t, "total", cn.TotalAmount, 18)
				eq(t, "credit", cn.CreditAmount, 12)
				eq(t, "balance", cn.BalanceAmount, 12)
				eq(t, "credit status", cn.CreditStatus, creditnote.CreditStatusAvailable)
				eq(t, "refund", cn.RefundAmount, 6)
				eq(t, "refund status", cn.RefundStatus, core.RefundStatusPending)
				eq(t, "reason", cn.Reason, creditnote.ReasonOther)
				eq(t, "currency", cn.Currency, f.inv.Currency)
				eq(t, "status", cn.Status, creditnote.StatusFinalized)
				eq(t, "items", len(cn.Items), 2)
				eq(t, "item 1 fee", cn.Items[0].FeeID, f.fees[0].ID)
				eq(t, "item 1", cn.Items[0].Amount, 10)
				eq(t, "item 2 fee", cn.Items[1].FeeID, f.fees[1].ID)
				eq(t, "item 2", cn.Items[1].Amount, 5)
				stored := f.h.CreditNotes(f.inv.ID)
				eq(t, "stored", len(stored), 1)
				eq(t, "number", stored[0].Number, f.inv.Number+"-CN001")
			},
		},
		{
			name:   "a fee tax without invoice tax fails",
			source: "spec/services/credit_notes/create_service_spec.rb:286",
			in: func(t *testing.T, f *fixture) creditnotes.CreateInput {
				f.h.Do(func(ctx context.Context, r port.Repos) error {
					return r.InvoiceAppliedTaxes.ReplaceForInvoice(ctx, f.h.OrgID(), f.inv.ID, nil)
				})
				return f.input(12, 6, item(f.fees[0], "10"), item(f.fees[1], "5"))
			},
			check: func(t *testing.T, _ *fixture, _ *creditnote.CreditNote, err error) {
				wantRule(t, err, creditnotes.CodeInvoiceAppliedTaxNotFound)
			},
		},
		{
			name:   "without metadata",
			source: "spec/services/credit_notes/create_service_spec.rb:292",
			check: func(t *testing.T, f *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "metadata", len(f.h.ReloadCreditNote(cn.ID).Metadata), 0)
			},
		},
		{
			name:   "with metadata",
			source: "spec/services/credit_notes/create_service_spec.rb:303",
			in: func(t *testing.T, f *fixture) creditnotes.CreateInput {
				in := f.input(12, 6, item(f.fees[0], "10"), item(f.fees[1], "5"))
				in.Metadata = core.Metadata{{Key: "key1", Value: "value1"}, {Key: "key2", Value: "value2"}}
				return in
			},
			check: func(t *testing.T, f *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				md := f.h.ReloadCreditNote(cn.ID).Metadata
				eq(t, "metadata", len(md), 2)
				eq(t, "key1", md[0].Value, "value1")
			},
		},
		{
			name:   "delivers a webhook and enqueues the document",
			source: "spec/services/credit_notes/create_service_spec.rb:333 (also :340)",
			check: func(t *testing.T, f *fixture, _ *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "webhook", contains(f.h.Webhooks(), webhook.EventTypeCreditNoteCreated), true)
				eq(t, "document job", contains(f.h.JobKinds(), kernel.JobGenerateCreditNoteDocument), true)
			},
		},
		{
			name:   "reports to the tax provider",
			source: "spec/services/credit_notes/create_service_spec.rb:366",
			check: func(t *testing.T, f *fixture, _ *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "report job", contains(f.h.JobKinds(), kernel.JobReportCreditNoteTaxes), true)
			},
		},
		{
			name:   "an item above the fee fails",
			source: "spec/services/credit_notes/create_service_spec.rb:397",
			in: func(t *testing.T, f *fixture) creditnotes.CreateInput {
				return f.input(10, 15, item(f.fees[0], "10"), item(f.fees[1], "15"))
			},
			check: func(t *testing.T, _ *fixture, _ *creditnote.CreditNote, err error) {
				wantFields(t, err, "items[1].amount:"+creditnote.CodeHigherThanRemainingFeeAmount)
			},
		},
		{
			name:   "missing items fail",
			source: "spec/services/credit_notes/create_service_spec.rb:414",
			in:     func(t *testing.T, f *fixture) creditnotes.CreateInput { return f.input(12, 6) },
			check: func(t *testing.T, _ *fixture, _ *creditnote.CreditNote, err error) {
				wantFields(t, err, "items:"+core.CodeRequired)
			},
		},
		{
			name:   "a refund of a paid invoice enqueues the refund",
			source: "spec/services/credit_notes/create_service_spec.rb:433 (also :452, the GoCardless variant)",
			in: func(t *testing.T, f *fixture) creditnotes.CreateInput {
				f.h.StoredPayment(payment.InvoicePayable(f.inv.ID), f.cust.ID, 6, payment.StateSucceeded, core.IntegrationID{})
				return f.input(12, 6, item(f.fees[0], "10"), item(f.fees[1], "5"))
			},
			check: func(t *testing.T, f *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "refund job", contains(f.h.JobKinds(), kernel.JobRefundCreditNote), true)
				// One refund job per credit note (lago CreditNotes::RefundJob
				// runs once per note, its refund keyed on the note).
				for _, j := range f.h.Store.Jobs() {
					if j.Kind == kernel.JobRefundCreditNote {
						eq(t, "refund job key", j.UniqueKey, "refund:"+cn.ID.String())
					}
				}
			},
		},
		{
			name:   "a refund of an invoice paid by a payment request enqueues the refund",
			source: "spec/services/credit_notes/create_service_spec.rb:463",
			in: func(t *testing.T, f *fixture) creditnotes.CreateInput {
				req := f.h.StoredPaymentRequest(f.cust.ID, core.PaymentStatusSucceeded, f.inv)
				f.h.StoredPayment(payment.RequestPayable(req.ID), f.cust.ID, 6, payment.StateSucceeded, core.IntegrationID{})
				return f.input(12, 6, item(f.fees[0], "10"), item(f.fees[1], "5"))
			},
			check: func(t *testing.T, f *fixture, _ *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "refund job", contains(f.h.JobKinds(), kernel.JobRefundCreditNote), true)
			},
		},
		{
			name:   "a failed payment enqueues no refund",
			source: "spec/services/credit_notes/create_service_spec.rb:473",
			in: func(t *testing.T, f *fixture) creditnotes.CreateInput {
				f.h.StoredPayment(payment.InvoicePayable(f.inv.ID), f.cust.ID, 6, payment.StateFailed, core.IntegrationID{})
				return f.input(12, 6, item(f.fees[0], "10"), item(f.fees[1], "5"))
			},
			check: func(t *testing.T, f *fixture, _ *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "refund job", contains(f.h.JobKinds(), kernel.JobRefundCreditNote), false)
			},
		},
		{
			name:   "no refund amount enqueues no refund",
			source: "spec/services/credit_notes/create_service_spec.rb:482",
			in: func(t *testing.T, f *fixture) creditnotes.CreateInput {
				f.h.StoredPayment(payment.InvoicePayable(f.inv.ID), f.cust.ID, 6, payment.StateSucceeded, core.IntegrationID{})
				return f.input(18, 0, item(f.fees[0], "10"), item(f.fees[1], "5"))
			},
			check: func(t *testing.T, f *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "refund job", contains(f.h.JobKinds(), kernel.JobRefundCreditNote), false)
				eq(t, "refund status", cn.RefundStatus, core.RefundStatus(""))
			},
		},
		{
			name:   "the issuing date is in the customer time zone",
			source: "spec/services/credit_notes/create_service_spec.rb:493",
			in: func(t *testing.T, f *fixture) creditnotes.CreateInput {
				f.h.UpdateCustomer(f.cust, func(c *customer.Customer) { loc, _ := time.LoadLocation("America/Los_Angeles"); c.Timezone = loc })
				f.h.Clock.Set(billingtest.Date(2026, time.March, 10, 1))
				return f.input(12, 6, item(f.fees[0], "10"), item(f.fees[1], "5"))
			},
			check: func(t *testing.T, _ *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "issuing date", cn.IssuingDate.String(), "2026-03-09")
			},
		},
		{
			name:   "an unknown invoice is not found",
			source: "spec/services/credit_notes/create_service_spec.rb:506",
			in: func(t *testing.T, f *fixture) creditnotes.CreateInput {
				in := f.input(12, 6, item(f.fees[0], "10"))
				in.InvoiceID = core.NewID[core.InvoiceID](f.h.IDs)
				return in
			},
			check: func(t *testing.T, _ *fixture, _ *creditnote.CreditNote, err error) {
				if !errors.Is(err, core.ErrNotFound) {
					t.Fatalf("got = %v, want = %v", err, core.ErrNotFound)
				}
			},
		},
		{
			name:   "a manual credit note succeeds",
			source: "spec/services/credit_notes/create_service_spec.rb:531",
			in: func(t *testing.T, f *fixture) creditnotes.CreateInput {
				return f.input(18, 0, item(f.fees[0], "10"), item(f.fees[1], "5"))
			},
			check: func(t *testing.T, _ *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "total", cn.TotalAmount, 18)
			},
		},
		{
			name:   "a draft invoice makes a draft credit note without webhook",
			source: "spec/services/credit_notes/create_service_spec.rb:551 (also :558)",
			in: func(t *testing.T, f *fixture) creditnotes.CreateInput {
				f.inv.Status = invoice.StatusDraft
				f.h.SaveInvoice(f.inv)
				return f.input(18, 0, item(f.fees[0], "10"), item(f.fees[1], "5"))
			},
			check: func(t *testing.T, f *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "status", cn.Status, creditnote.StatusDraft)
				eq(t, "webhook", contains(f.h.Webhooks(), webhook.EventTypeCreditNoteCreated), false)
				eq(t, "document job", contains(f.h.JobKinds(), kernel.JobGenerateCreditNoteDocument), false)
			},
		},
		{
			name:   "coupons are taken into account",
			source: "spec/services/credit_notes/create_service_spec.rb:634",
			in: func(t *testing.T, f *fixture) creditnotes.CreateInput {
				f.inv, f.fees = f.h.CreditableInvoice(f.cust, []int64{10, 10}, []*catalog.Tax{f.tax}, nil, billingtest.Coupons("5"))
				return f.input(6, 3, item(f.fees[0], "10"), item(f.fees[1], "5"))
			},
			check: func(t *testing.T, f *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "sub total", cn.SubTotalExcludingTaxesAmount, 8)
				eq(t, "total", cn.TotalAmount, 10)
				eq(t, "credit", cn.CreditAmount, 7)
				eq(t, "refund", cn.RefundAmount, 3)
				eq(t, "balance", cn.BalanceAmount, 7)
				eq(t, "coupons adjustment", cn.CouponsAdjustmentAmount, 8)
				eq(t, "taxes", cn.TaxesAmount, 2)
				eq(t, "applied taxes", len(cn.AppliedTaxes), 1)
				decEq(t, "item 1 precise", cn.Items[0].PreciseAmount, "10")
			},
		},
		{
			name:   "a voided invoice makes a finalized credit note",
			source: "spec/services/credit_notes/create_service_spec.rb:851",
			in: func(t *testing.T, f *fixture) creditnotes.CreateInput {
				f.inv.Status = invoice.StatusVoided
				f.h.SaveInvoice(f.inv)
				return f.input(12, 6, item(f.fees[0], "10"), item(f.fees[1], "5"))
			},
			check: func(t *testing.T, f *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "status", cn.Status, creditnote.StatusFinalized)
				eq(t, "invoice status", f.h.Invoice(f.inv.ID).Status, invoice.StatusVoided)
			},
		},
		{
			name:   "a preview builds without storing",
			source: "spec/services/credit_notes/create_service_spec.rb:865",
			in: func(t *testing.T, f *fixture) creditnotes.CreateInput {
				in := f.input(12, 6, item(f.fees[0], "10"), item(f.fees[1], "5"))
				in.Preview = true
				in.Metadata = core.Metadata{{Key: "key1", Value: "value1"}}
				return in
			},
			check: func(t *testing.T, f *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "taxes", cn.TaxesAmount, 3)
				eq(t, "total", cn.TotalAmount, 18)
				eq(t, "credit", cn.CreditAmount, 12)
				eq(t, "balance", cn.BalanceAmount, 12)
				eq(t, "refund", cn.RefundAmount, 6)
				eq(t, "refund status", cn.RefundStatus, core.RefundStatusPending)
				eq(t, "items", len(cn.Items), 2)
				eq(t, "metadata", len(cn.Metadata), 1)
				// source: spec/services/credit_notes/create_service_spec.rb:908 (and :912, :916, :920, :927, :931, :938)
				eq(t, "stored", len(f.h.CreditNotes(f.inv.ID)), 0)
				eq(t, "webhooks", len(f.h.Webhooks()), 0)
				eq(t, "jobs", len(f.h.JobKinds()), 0)
			},
		},
		{
			name:   "a zero total fails",
			source: "spec/services/credit_notes/create_service_spec.rb:969",
			in: func(t *testing.T, f *fixture) creditnotes.CreateInput {
				return f.input(0, 0, item(f.fees[0], "0"), item(f.fees[1], "0"))
			},
			check: func(t *testing.T, f *fixture, _ *creditnote.CreditNote, err error) {
				wantFields(t, err, "base:"+creditnote.CodeTotalAmountMustBePositive)
				eq(t, "stored", len(f.h.CreditNotes(f.inv.ID)), 0)
			},
		},
		{
			name:   "an invalid reason fails",
			source: "spec/services/credit_notes/create_service_spec.rb:982",
			in: func(t *testing.T, f *fixture) creditnotes.CreateInput {
				in := f.input(12, 6, item(f.fees[0], "10"), item(f.fees[1], "5"))
				in.Reason = "invalid"
				return in
			},
			check: func(t *testing.T, f *fixture, _ *creditnote.CreditNote, err error) {
				wantFields(t, err, "reason:"+core.CodeInvalid)
				eq(t, "stored", len(f.h.CreditNotes(f.inv.ID)), 0)
			},
		},
		{
			name:   "a refund only note of a fractional item",
			source: "spec/services/credit_notes/create_service_spec.rb:1030",
			in: func(t *testing.T, f *fixture) creditnotes.CreateInput {
				tax := f.h.Tax("vat25", "25")
				f.inv, f.fees = f.h.CreditableInvoice(f.cust, []int64{20000}, []*catalog.Tax{tax}, nil)
				return f.input(0, 24166, item(f.fees[0], "19333.33"))
			},
			check: func(t *testing.T, _ *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "sub total", cn.SubTotalExcludingTaxesAmount, 19333)
				eq(t, "credit", cn.CreditAmount, 0)
				eq(t, "refund", cn.RefundAmount, 24166)
				eq(t, "coupons adjustment", cn.CouponsAdjustmentAmount, 0)
				eq(t, "taxes", cn.TaxesAmount, 4833)
				decEq(t, "taxes rate", cn.TaxesRate, "25")
			},
		},
		{
			name:   "an offset settles the invoice",
			source: "spec/services/credit_notes/create_service_spec.rb:1053",
			in: func(t *testing.T, f *fixture) creditnotes.CreateInput {
				in := f.input(0, 0, item(f.fees[0], "10"), item(f.fees[1], "5"))
				in.OffsetAmount = 18
				return in
			},
			check: func(t *testing.T, f *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "offset", cn.OffsetAmount, 18)
				eq(t, "total", cn.TotalAmount, 18)
				s := f.h.Settlements(f.inv.ID)
				if len(s) != 1 {
					t.Fatalf("settlements: got = %d, want = 1", len(s))
				}
				eq(t, "settlement amount", s[0].Amount, 18)
				eq(t, "settlement type", s[0].Type, invoice.SettlementTypeCreditNote)
				eq(t, "settlement note", s[0].CreditNoteID, cn.ID)
				eq(t, "offsetted", f.h.Invoice(f.inv.ID).OffsettedAmount, 18)
			},
		},
		{
			name:   "no offset makes no settlement",
			source: "spec/services/credit_notes/create_service_spec.rb:1067",
			in: func(t *testing.T, f *fixture) creditnotes.CreateInput {
				return f.input(10, 0, item(f.fees[0], "10"), item(f.fees[1], "5"))
			},
			check: func(t *testing.T, f *fixture, _ *creditnote.CreditNote, _ error) {
				eq(t, "settlements", len(f.h.Settlements(f.inv.ID)), 0)
			},
		},
		{
			name:   "a preview makes no settlement",
			source: "spec/services/credit_notes/create_service_spec.rb:1075",
			in: func(t *testing.T, f *fixture) creditnotes.CreateInput {
				in := f.input(0, 0, item(f.fees[0], "10"), item(f.fees[1], "5"))
				in.OffsetAmount, in.Preview = 18, true
				return in
			},
			check: func(t *testing.T, f *fixture, _ *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "settlements", len(f.h.Settlements(f.inv.ID)), 0)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			f := newFixture(t)
			in := f.input(12, 6, item(f.fees[0], "10"), item(f.fees[1], "5"))
			if tt.in != nil {
				in = tt.in(t, f)
			}
			cn, err := f.svc.Create(f.h.Ctx, in)
			tt.check(t, f, cn, err)
		})
	}
}

// creditFixture is a credit invoice of 1000 + 20% taxes buying credits in
// a wallet of rate 10 with 1200 left.
func creditFixture(t *testing.T, status core.PaymentStatus, opts ...func(*wallet.Wallet)) (*fixture, *voider) {
	t.Helper()
	h, svc := newHarness(t)
	f := &fixture{h: h, svc: svc, cust: h.Customer(), tax: h.Tax("vat", "20")}
	w := h.Wallet(f.cust, "120", append([]func(*wallet.Wallet){func(w *wallet.Wallet) { w.RateAmount = money.NewFromInt(10) }}, opts...)...)
	tx := h.InboundTransaction(w, wallet.TransactionStatusPurchased, "120")
	f.inv, f.fees = h.CreditableInvoice(f.cust, []int64{1000}, []*catalog.Tax{f.tax},
		[]func(*invoice.Invoice){func(inv *invoice.Invoice) { inv.Type, inv.PaymentStatus = invoice.TypeCredit, status }},
		billingtest.CreditFee(tx.ID))
	v := &voider{}
	svc.SetWalletVoider(v)
	return f, v
}

func TestCreateOnCreditInvoice(t *testing.T) {
	tests := []struct {
		name   string
		source string
		status core.PaymentStatus
		wallet func(*wallet.Wallet)
		credit int64
		refund int64
		offset int64
		// want is the rule code, or the field errors, or nothing.
		rule   string
		fields []string
	}{
		{
			name: "voids the refunded credits", source: "spec/services/credit_notes/create_service_spec.rb:714",
			status: core.PaymentStatusSucceeded, refund: 1200,
		},
		{
			name: "a terminated wallet fails", source: "spec/services/credit_notes/create_service_spec.rb:799",
			status: core.PaymentStatusSucceeded, refund: 1200,
			wallet: func(w *wallet.Wallet) { w.Status = wallet.StatusTerminated },
			rule:   creditnotes.CodeInvalidTypeOrStatus,
		},
		{
			name: "a wallet balance below the item fails", source: "spec/services/credit_notes/create_service_spec.rb:812",
			status: core.PaymentStatusSucceeded, refund: 1200,
			wallet: func(w *wallet.Wallet) { w.BalanceAmount = 500 },
			fields: []string{"items[0].amount:" + creditnote.CodeHigherThanWalletBalance},
		},
		{
			name: "a credit amount fails", source: "spec/services/credit_notes/create_service_spec.rb:825",
			status: core.PaymentStatusSucceeded, credit: 10, refund: 1200,
			fields: []string{"credit_amount:" + creditnote.CodeCannotCreditInvoice},
		},
		{
			name: "a pending payment allows an offset", source: "spec/services/credit_notes/create_service_spec.rb:1100",
			status: core.PaymentStatusPending, offset: 1200,
		},
		{
			name: "a pending payment rejects a credit", source: "spec/services/credit_notes/create_service_spec.rb:1114",
			status: core.PaymentStatusPending, credit: 500, rule: creditnotes.CodeInvalidTypeOrStatus,
		},
		{
			name: "a pending payment rejects a refund", source: "spec/services/credit_notes/create_service_spec.rb:1126",
			status: core.PaymentStatusPending, refund: 500, rule: creditnotes.CodeInvalidTypeOrStatus,
		},
		{
			name: "a failed payment allows an offset", source: "spec/services/credit_notes/create_service_spec.rb:1145",
			status: core.PaymentStatusFailed, offset: 1200,
		},
		{
			name: "a failed payment rejects a credit", source: "spec/services/credit_notes/create_service_spec.rb:1159",
			status: core.PaymentStatusFailed, credit: 500, rule: creditnotes.CodeInvalidTypeOrStatus,
		},
		{
			name: "a failed payment rejects a refund", source: "spec/services/credit_notes/create_service_spec.rb:1171",
			status: core.PaymentStatusFailed, refund: 500, rule: creditnotes.CodeInvalidTypeOrStatus,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			var opts []func(*wallet.Wallet)
			if tt.wallet != nil {
				opts = append(opts, tt.wallet)
			}
			f, v := creditFixture(t, tt.status, opts...)
			if tt.status != core.PaymentStatusSucceeded {
				f.inv.TotalPaidAmount = 0
				f.h.SaveInvoice(f.inv)
			}
			in := f.input(tt.credit, tt.refund, item(f.fees[0], "1000"))
			in.OffsetAmount = tt.offset
			cn, err := f.svc.Create(f.h.Ctx, in)
			switch {
			case tt.rule != "":
				wantRule(t, err, tt.rule)
			case tt.fields != nil:
				wantFieldIncluded(t, err, tt.fields[0])
			default:
				must(t, err)
				eq(t, "credit", cn.CreditAmount, tt.credit)
				eq(t, "refund", cn.RefundAmount, tt.refund)
				eq(t, "offset", cn.OffsetAmount, tt.offset)
				eq(t, "voided", len(v.notes), 1)
			}
		})
	}
}
