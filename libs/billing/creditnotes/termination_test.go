package creditnotes_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// terminationSetup is the lago create_from_termination_spec fixture: a
// calendar monthly plan paid in advance of 31.00 taxed at 20%, subscribed
// 2022-09-01 10:00 and terminated 2022-10-15 10:00, with the invoice of its
// first subscription fee.
type terminationSetup struct {
	planAmount   int64
	feeAmount    int64 // the subscription fee; zero means planAmount
	planDetails  int64 // amount_details.plan_amount_cents of the fee, if not zero
	trial        int
	taxRate      string
	coupons      int64
	paid         int64
	second       bool // a second subscription's fee on the same invoice
	tz           string
	start, end   time.Time
	anniversary  bool
	earlierItem  int64 // an earlier credit note's item on the fee
	invoiceState func(*invoice.Invoice)
	precise      string // the fee's precise amount, when not its amount
}

// terminationFixture is the stored terminationSetup.
type terminationFixture struct {
	h   *billingtest.Harness
	svc *creditnotes.Service
	sub *subscription.Subscription
	tax *catalog.Tax
	inv *invoice.Invoice
	fee *invoice.Fee
}

// defaults fills the zero fields of the setup with the spec's lets.
func (s terminationSetup) defaults() terminationSetup {
	if s.planAmount == 0 {
		s.planAmount = 3100
	}
	if s.planAmount < 0 {
		s.planAmount = 0
	}
	if s.feeAmount == 0 {
		s.feeAmount = s.planAmount
	}
	if s.taxRate == "" {
		s.taxRate = "20"
	}
	if s.start.IsZero() {
		s.start = billingtest.Date(2022, time.September, 1, 10)
	}
	if s.end.IsZero() {
		s.end = billingtest.Date(2022, time.October, 15, 10)
	}
	return s
}

// newTermination stores the setup with the clock at the termination.
func newTermination(t *testing.T, s terminationSetup) *terminationFixture {
	t.Helper()
	s = s.defaults()
	h := billingtest.New(t, s.end)
	f := &terminationFixture{h: h, svc: creditnotes.New(h.Deps), tax: h.Tax("vat", s.taxRate)}
	cust := h.Customer(func(c *customer.Customer) {
		if s.tz == "" {
			return
		}
		loc, err := time.LoadLocation(s.tz)
		if err != nil {
			t.Fatalf("load location: %v", err)
		}
		c.Timezone = loc
	})
	plan := h.Plan(period.Monthly, s.planAmount, func(p *catalog.Plan) {
		p.PayInAdvance, p.TrialPeriodDays = true, s.trial
	})
	f.sub = h.Subscription(cust, plan, billingtest.SubscribedAt(s.start), func(sub *subscription.Subscription) {
		if s.anniversary {
			billingtest.Anniversary(sub)
		}
	})
	h.UpdateSubscription(f.sub, func(sub *subscription.Subscription) {
		end := s.end.UTC()
		sub.Status, sub.TerminatedAt = subscription.StatusTerminated, &end
	})
	f.inv, f.fee = f.invoice(t, cust, s, s.start)
	if s.earlierItem != 0 {
		h.StoredCreditNote(f.inv, s.earlierItem, func(cn *creditnote.CreditNote) {
			cn.Items = []creditnote.Item{{
				ID: core.NewID[core.CreditNoteItemID](h.IDs), FeeID: f.fee.ID,
				Amount: s.earlierItem, PreciseAmount: money.NewFromInt(s.earlierItem),
			}}
		})
	}
	return f
}

// invoice stores an invoice of the subscription fee created at at, as the
// spec's generate_invoice_and_fee does.
func (f *terminationFixture) invoice(t *testing.T, cust *customer.Customer, s terminationSetup, at time.Time) (*invoice.Invoice, *invoice.Fee) {
	t.Helper()
	h := f.h
	inv := h.StoredInvoice(cust, func(inv *invoice.Invoice) {
		inv.Status, inv.TotalPaidAmount, inv.CreatedAt = invoice.StatusFinalized, s.paid, at
		if s.invoiceState != nil {
			s.invoiceState(inv)
		}
	})
	opts := []func(*invoice.Fee){
		billingtest.SubscriptionFee(f.sub),
		billingtest.Coupons(fmt.Sprint(s.coupons)),
		h.FeeTax(f.tax),
		func(fee *invoice.Fee) { fee.CreatedAt = at },
	}
	if s.planDetails != 0 {
		details, err := json.Marshal(map[string]int64{"plan_amount_cents": s.planDetails})
		if err != nil {
			t.Fatal(err)
		}
		opts = append(opts, func(fee *invoice.Fee) { fee.AmountDetails = details })
	}
	if s.precise != "" {
		opts = append(opts, func(fee *invoice.Fee) { fee.PreciseAmount = money.MustParse(s.precise) })
	}
	fee := h.StoredFee(inv, s.feeAmount, opts...)
	fees := []*invoice.Fee{fee}
	if s.second {
		other := h.Subscription(cust, h.Plan(period.Monthly, s.planAmount), billingtest.SubscribedAt(s.start))
		fees = append(fees, h.StoredFee(inv, s.feeAmount, billingtest.SubscriptionFee(other), h.FeeTax(f.tax),
			func(fee *invoice.Fee) { fee.CreatedAt = at }))
	}
	h.InvoiceTaxes(inv, fees, f.tax)
	h.Totals(inv, fees)
	if s.paid == inv.TotalAmount && s.paid != 0 {
		inv.PaymentStatus = core.PaymentStatusSucceeded
		h.SaveInvoice(inv)
	}
	return inv, fee
}

// terminationWant is the expected termination credit note.
type terminationWant struct {
	total, item, taxes, refund, credit, offset int64
	preciseItem                                string // when the item is not whole
}

func TestCreateFromTermination(t *testing.T) {
	const refund, offset = subscription.TerminationCreditNoteRefund, subscription.TerminationCreditNoteOffset
	tests := []struct {
		name    string
		source  string
		setup   terminationSetup
		in      creditnotes.TerminationInput
		prepare func(t *testing.T, f *terminationFixture) *invoice.Fee // returns the credited fee when not the first
		want    *terminationWant                                       // nil: no credit note
		code    string                                                 // a rule error
	}{
		{
			name: "credits the unused days", source: "spec/services/credit_notes/create_from_termination_spec.rb:196",
			want: &terminationWant{total: 1920, credit: 1920, item: 1600, taxes: 320},
		},
		{
			name: "uses the plan amount of the fee's amount details", source: "spec/services/credit_notes/create_from_termination_spec.rb:232",
			setup: terminationSetup{feeAmount: 6200, planDetails: 6200},
			want:  &terminationWant{total: 3840, credit: 3840, item: 3200, taxes: 640},
		},
		// Refund.
		{
			name: "refund: a fully paid invoice refunds everything", source: "spec/services/credit_notes/create_from_termination_spec.rb:258",
			setup: terminationSetup{second: true, paid: 7440}, in: creditnotes.TerminationInput{OnTermination: refund},
			want: &terminationWant{total: 1920, item: 1600, taxes: 320, refund: 1920},
		},
		{
			name: "refund: a payment covering the fee refunds the unused paid part", source: "spec/services/credit_notes/create_from_termination_spec.rb:301",
			setup: terminationSetup{second: true, paid: 5580}, in: creditnotes.TerminationInput{OnTermination: refund},
			want: &terminationWant{total: 1920, item: 1600, taxes: 320, refund: 990, credit: 930},
		},
		{
			// The paid share of the fee is weighted on its precise amount:
			// (3100.8 + 620) / 7440 × 5580 − 1800 = 990.6, refunded as 991.
			name: "refund: the paid share uses the fee's precise amount", source: "app/services/credit_notes/create_from_termination.rb:192",
			setup: terminationSetup{second: true, paid: 5580, precise: "3100.8"}, in: creditnotes.TerminationInput{OnTermination: refund},
			want: &terminationWant{total: 1920, item: 1600, taxes: 320, refund: 991, credit: 929},
		},
		{
			name: "refund: a payment not covering the used part refunds nothing", source: "spec/services/credit_notes/create_from_termination_spec.rb:343",
			setup: terminationSetup{second: true, paid: 930}, in: creditnotes.TerminationInput{OnTermination: refund},
			want: &terminationWant{total: 1920, item: 1600, taxes: 320, credit: 1920},
		},
		{
			name: "refund: an earlier credit note on the fee", source: "spec/services/credit_notes/create_from_termination_spec.rb:395",
			setup: terminationSetup{second: true, paid: 5580, earlierItem: 200}, in: creditnotes.TerminationInput{OnTermination: refund},
			want: &terminationWant{total: 1680, item: 1400, taxes: 280, refund: 990, credit: 690},
		},
		{
			name: "refund: an earlier credit note and coupons", source: "spec/services/credit_notes/create_from_termination_spec.rb:446",
			setup: terminationSetup{second: true, paid: 5580, earlierItem: 200, coupons: 500}, in: creditnotes.TerminationInput{OnTermination: refund},
			want: &terminationWant{total: 1409, item: 1400, taxes: 235, refund: 1035, credit: 374},
		},
		{
			name: "refund: an unpaid invoice refunds nothing", source: "spec/services/credit_notes/create_from_termination_spec.rb:493",
			setup: terminationSetup{second: true}, in: creditnotes.TerminationInput{OnTermination: refund},
			want: &terminationWant{total: 1920, item: 1600, taxes: 320, credit: 1920},
		},
		{
			name: "refund: an upgrade is unsupported", source: "spec/services/credit_notes/create_from_termination_spec.rb:525",
			setup: terminationSetup{second: true}, in: creditnotes.TerminationInput{OnTermination: refund, Upgrade: true},
			code: creditnotes.CodeUpgradeRefundUnsupported,
		},
		{
			name: "refund: the trial is not used", source: "spec/services/credit_notes/create_from_termination_spec.rb:535",
			setup: terminationSetup{trial: 41, paid: 1920}, in: creditnotes.TerminationInput{OnTermination: refund},
			want: &terminationWant{total: 1920, item: 1600, taxes: 320, refund: 1440, credit: 480},
		},
		// Offset.
		{
			name: "offset: a fully paid invoice refunds everything", source: "spec/services/credit_notes/create_from_termination_spec.rb:581",
			setup: terminationSetup{second: true, paid: 7440}, in: creditnotes.TerminationInput{OnTermination: offset},
			want: &terminationWant{total: 1920, item: 1600, taxes: 320, refund: 1920},
		},
		{
			name: "offset: a payment covering the fee refunds the paid part", source: "spec/services/credit_notes/create_from_termination_spec.rb:624",
			setup: terminationSetup{second: true, paid: 5580}, in: creditnotes.TerminationInput{OnTermination: offset},
			want: &terminationWant{total: 1920, item: 1600, taxes: 320, refund: 990, offset: 930},
		},
		{
			name: "offset: a payment not covering the used part offsets everything", source: "spec/services/credit_notes/create_from_termination_spec.rb:666",
			setup: terminationSetup{second: true, paid: 930}, in: creditnotes.TerminationInput{OnTermination: offset},
			want: &terminationWant{total: 1920, item: 1600, taxes: 320, offset: 1920},
		},
		{
			name: "offset: an earlier credit note on the fee", source: "spec/services/credit_notes/create_from_termination_spec.rb:718",
			setup: terminationSetup{second: true, paid: 5580, earlierItem: 200}, in: creditnotes.TerminationInput{OnTermination: offset},
			want: &terminationWant{total: 1680, item: 1400, taxes: 280, refund: 990, offset: 690},
		},
		{
			name: "offset: an earlier credit note and coupons", source: "spec/services/credit_notes/create_from_termination_spec.rb:769",
			setup: terminationSetup{second: true, paid: 5580, earlierItem: 200, coupons: 500}, in: creditnotes.TerminationInput{OnTermination: offset},
			want: &terminationWant{total: 1409, item: 1400, taxes: 235, refund: 1035, offset: 374},
		},
		{
			name: "offset: an unpaid invoice is offset", source: "spec/services/credit_notes/create_from_termination_spec.rb:816",
			setup: terminationSetup{}, in: creditnotes.TerminationInput{OnTermination: offset},
			want: &terminationWant{total: 1920, item: 1600, taxes: 320, offset: 1920},
		},
		{
			name: "offset: an upgrade is unsupported", source: "spec/services/credit_notes/create_from_termination_spec.rb:848",
			in:   creditnotes.TerminationInput{OnTermination: offset, Upgrade: true},
			code: creditnotes.CodeUpgradeRefundUnsupported,
		},
		{
			name: "offset: the trial is not used", source: "spec/services/credit_notes/create_from_termination_spec.rb:858",
			setup: terminationSetup{trial: 41, paid: 1920}, in: creditnotes.TerminationInput{OnTermination: offset},
			want: &terminationWant{total: 1920, item: 1600, taxes: 320, refund: 1440, offset: 480},
		},
		// Edge cases.
		{
			name: "a voided invoice credits nothing", source: "spec/services/credit_notes/create_from_termination_spec.rb:899",
			setup: terminationSetup{invoiceState: func(inv *invoice.Invoice) { inv.Status = invoice.StatusVoided }},
		},
		{
			name: "a zero fee credits nothing", source: "spec/services/credit_notes/create_from_termination_spec.rb:907",
			setup: terminationSetup{planAmount: -1},
		},
		{
			name: "the last fee is the reference", source: "spec/services/credit_notes/create_from_termination_spec.rb:923",
			prepare: func(t *testing.T, f *terminationFixture) *invoice.Fee {
				s := terminationSetup{feeAmount: 6200, planDetails: 6200}.defaults()
				_, fee := f.invoice(t, f.h.ReloadCustomer(f.sub.CustomerID), s, billingtest.Date(2022, time.October, 1, 10))
				return fee
			},
			want: &terminationWant{total: 3840, credit: 3840, item: 3200, taxes: 640},
		},
		{
			name: "an earlier credit note leaves the remaining amount", source: "spec/services/credit_notes/create_from_termination_spec.rb:959",
			setup: terminationSetup{earlierItem: 1000},
			want:  &terminationWant{total: 720, credit: 720, item: 600, taxes: 120},
		},
		{
			name: "a trial ending after the termination is excluded", source: "spec/services/credit_notes/create_from_termination_spec.rb:981",
			setup: terminationSetup{trial: 46},
			want:  &terminationWant{total: 1800, credit: 1800, item: 1500, taxes: 300},
		},
		{
			name: "a trial ending after the period credits nothing", source: "spec/services/credit_notes/create_from_termination_spec.rb:1001",
			setup: terminationSetup{trial: 120},
		},
		{
			name: "an upgrade credits the termination day", source: "spec/services/credit_notes/create_from_termination_spec.rb:1010",
			in:   creditnotes.TerminationInput{Upgrade: true},
			want: &terminationWant{total: 2040, credit: 2040, item: 1700, taxes: 340},
		},
		{
			name: "a time zone behind UTC", source: "spec/services/credit_notes/create_from_termination_spec.rb:1035",
			setup: terminationSetup{
				tz: "America/Los_Angeles", start: billingtest.Date(2022, time.September, 1, 12), end: billingtest.Date(2022, time.October, 15, 1),
			},
			want: &terminationWant{total: 2040, credit: 2040, item: 1700, taxes: 340},
		},
		{
			name: "a time zone ahead of UTC", source: "spec/services/credit_notes/create_from_termination_spec.rb:1056",
			setup: terminationSetup{
				tz: "Europe/Paris", start: billingtest.Date(2022, time.September, 1, 12), end: billingtest.Date(2022, time.October, 15, 1),
			},
			want: &terminationWant{total: 1920, credit: 1920, item: 1600, taxes: 320},
		},
		{
			name: "rounding at the maximum precision", source: "spec/services/credit_notes/create_from_termination_spec.rb:1096",
			setup: terminationSetup{
				planAmount: 999, taxRate: "0", anniversary: true,
				start: billingtest.Date(2023, time.January, 30, 10), end: billingtest.Date(2023, time.March, 14, 10),
			},
			// lago gets 499.49999 (4.99) from the float day price 999.fdiv(30);
			// the decimal day price is exactly 33.3, so 15 days are 499.5.
			want: &terminationWant{total: 500, credit: 500, item: 500, preciseItem: "499.5", taxes: 0},
		},
		{
			name: "coupons covering the fee credit nothing", source: "spec/services/credit_notes/create_from_termination_spec.rb:1117",
			setup: terminationSetup{coupons: 3100},
		},
		{
			name: "coupons reduce the credit", source: "spec/services/credit_notes/create_from_termination_spec.rb:1141",
			setup: terminationSetup{coupons: 1000},
			want:  &terminationWant{total: 1301, credit: 1301, item: 1600, taxes: 217},
		},
		{
			name: "a preview stores nothing", source: "spec/services/credit_notes/create_from_termination_spec.rb:1163 (also :1186, :1190)",
			in:   creditnotes.TerminationInput{Preview: true},
			want: &terminationWant{total: 1920, credit: 1920, item: 1600, taxes: 320},
		},
		{
			name: "a preview of a tax pending invoice", source: "spec/services/credit_notes/create_from_termination_spec.rb:1203",
			setup: terminationSetup{invoiceState: func(inv *invoice.Invoice) {
				inv.Status, inv.TaxStatus = invoice.StatusGenerating, invoice.TaxStatusPending
			}},
			in:   creditnotes.TerminationInput{Preview: true},
			want: &terminationWant{total: 1920, credit: 1920, item: 1600, taxes: 320},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			f := newTermination(t, tt.setup)
			fee := f.fee
			if tt.prepare != nil {
				fee = tt.prepare(t, f)
			}
			before := len(f.h.CreditNotes(fee.InvoiceID))
			in := tt.in
			in.OrganizationID, in.SubscriptionID = f.h.OrgID(), f.sub.ID
			var cn *creditnote.CreditNote
			err := f.h.Deps.Tx.InTx(f.h.Ctx, func(ctx context.Context, r port.Repos) error {
				var err error
				cn, err = f.svc.CreateFromTermination(ctx, r, in)
				return err
			})
			if tt.code != "" {
				wantRule(t, err, tt.code)
				return
			}
			must(t, err)
			stored := len(f.h.CreditNotes(fee.InvoiceID)) - before
			if tt.want == nil {
				if cn != nil {
					t.Fatalf("credit note: got = %+v, want = nil", cn)
				}
				eq(t, "stored", stored, 0)
				return
			}
			if cn == nil {
				t.Fatal("credit note: got = nil, want = a credit note")
			}
			w := tt.want
			eq(t, "reason", cn.Reason, creditnote.ReasonOrderChange)
			eq(t, "total", cn.TotalAmount, w.total)
			eq(t, "refund", cn.RefundAmount, w.refund)
			eq(t, "credit", cn.CreditAmount, w.credit)
			eq(t, "offset", cn.OffsetAmount, w.offset)
			eq(t, "taxes", cn.TaxesAmount, w.taxes)
			eq(t, "balance", cn.BalanceAmount, w.credit)
			eq(t, "applied taxes", len(cn.AppliedTaxes), 1)
			if len(cn.AppliedTaxes) == 1 {
				eq(t, "tax code", cn.AppliedTaxes[0].TaxCode, f.tax.Code)
			}
			if len(cn.Items) != 1 {
				t.Fatalf("items: got = %d, want = 1", len(cn.Items))
			}
			eq(t, "item fee", cn.Items[0].FeeID, fee.ID)
			eq(t, "item", cn.Items[0].Amount, w.item)
			precise := w.preciseItem
			if precise == "" {
				precise = fmt.Sprint(w.item)
			}
			decEq(t, "precise item", cn.Items[0].PreciseAmount, precise)
			if in.Preview {
				eq(t, "stored", stored, 0)
			} else {
				eq(t, "stored", stored, 1)
				eq(t, "status", cn.Status, creditnote.StatusFinalized)
			}
		})
	}
}

// TestCreateFromTerminationSkip covers on_termination skip, which issues
// nothing.
func TestCreateFromTerminationSkip(t *testing.T) {
	f := newTermination(t, terminationSetup{})
	err := f.h.Deps.Tx.InTx(f.h.Ctx, func(ctx context.Context, r port.Repos) error {
		cn, err := f.svc.CreateFromTermination(ctx, r, creditnotes.TerminationInput{
			OrganizationID: f.h.OrgID(), SubscriptionID: f.sub.ID, OnTermination: subscription.TerminationCreditNoteSkip,
		})
		if cn != nil {
			return errors.New("got a credit note, want = nil")
		}
		return err
	})
	must(t, err)
}
