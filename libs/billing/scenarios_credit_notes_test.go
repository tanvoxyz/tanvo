package billing_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// invPay records a manual payment of amount on invoice id (lago
// Payments::ManualCreateService): the paid amount grows and the invoice is
// paid once it covers the total. Manual payments live outside billing.
func (s *scenario) invPay(id core.InvoiceID, amount int64) *invoice.Invoice {
	s.T.Helper()
	inv := s.Invoice(id)
	inv.TotalPaidAmount += amount
	if inv.TotalPaidAmount >= inv.TotalAmount {
		inv.PaymentStatus = core.PaymentStatusSucceeded
	}
	s.SaveInvoice(inv)
	return inv
}

// invSetPaymentStatus sets the payment status of invoice id (lago
// update_invoice with payment_status).
func (s *scenario) invSetPaymentStatus(id core.InvoiceID, st core.PaymentStatus) {
	s.T.Helper()
	inv := s.Invoice(id)
	inv.PaymentStatus = st
	s.SaveInvoice(inv)
}

// invApplyCoupon applies cp to customer c with the amount override.
func (s *scenario) invApplyCoupon(cp *coupon.Coupon, c *customer.Customer, amount int64) {
	s.T.Helper()
	a := coupon.Apply(cp, core.NewID[core.AppliedCouponID](s.IDs), c.ID, coupon.Overrides{Amount: &amount}, s.Now())
	s.tx(func(ctx context.Context, r port.Repos) error { return r.AppliedCoupons.Create(ctx, a) })
}

// invForeverCoupon is a coupon option for a reusable coupon applied every
// period.
func invForeverCoupon(c *coupon.Coupon) { c.Frequency, c.Reusable = coupon.FrequencyForever, true }

// invItems returns the credit note items of the fee amounts, given as fee,
// amount pairs.
func invItems(pairs ...any) []creditnotes.ItemInput {
	var out []creditnotes.ItemInput
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, creditnotes.ItemInput{FeeID: pairs[i].(*invoice.Fee).ID, Amount: money.NewFromInt(int64(pairs[i+1].(int)))})
	}
	return out
}

// invEstimate estimates the credit note of the items on invoice id.
func (s *scenario) invEstimate(id core.InvoiceID, items []creditnotes.ItemInput) (*creditnote.CreditNote, error) {
	s.T.Helper()
	return s.E.CreditNotes.Estimate(s.Ctx, s.OrgID(), id, items)
}

// invFeeOfAmount returns the fee of invoice id with the amount.
func (s *scenario) invFeeOfAmount(id core.InvoiceID, amount int64) *invoice.Fee {
	s.T.Helper()
	for _, f := range s.Fees(id) {
		if f.Amount == amount {
			return f
		}
	}
	s.T.Fatalf("no fee of %d on invoice %s", amount, id)
	return nil
}

// invCN are the lago attributes of a credit note or an estimate; a nil
// pointer is not checked. Creditable and Refundable are the estimate's
// max_creditable_amount_cents and max_refundable_amount_cents.
type invCN struct {
	SubTotal, Taxes, Coupons, Refund, Credit, Total *int64
	Creditable, Refundable                          *int64
	PreciseTaxes, PreciseCoupons, PreciseTotal      string
	RoundingAdjustment, TaxesRate                   string
}

// invPreciseTolerance is the precision of lago's precise amounts.
var invPreciseTolerance = money.MustParse("0.00001")

// i64 returns a pointer to v.
func i64(v int64) *int64 { return &v }

// invCheckCN checks cn against want.
func invCheckCN(t *testing.T, step string, cn *creditnote.CreditNote, want invCN) {
	t.Helper()
	ints := []struct {
		name string
		got  int64
		want *int64
	}{
		{"sub_total_excluding_taxes", cn.SubTotalExcludingTaxesAmount, want.SubTotal},
		{"taxes", cn.TaxesAmount, want.Taxes},
		{"coupons_adjustment", cn.CouponsAdjustmentAmount, want.Coupons},
		{"refund", cn.RefundAmount, want.Refund},
		{"credit", cn.CreditAmount, want.Credit},
		{"total", cn.TotalAmount, want.Total},
		{"max_creditable", cn.CreditAmount, want.Creditable},
		{"max_refundable", cn.RefundAmount, want.Refundable},
	}
	for _, c := range ints {
		if c.want != nil && c.got != *c.want {
			t.Errorf("%s: %s = %d, want = %d", step, c.name, c.got, *c.want)
		}
	}
	items := money.Zero
	for _, it := range cn.Items {
		items = items.Add(it.PreciseAmount)
	}
	decs := []struct {
		name string
		got  money.Decimal
		want string
	}{
		{"precise_taxes", cn.PreciseTaxesAmount, want.PreciseTaxes},
		{"precise_coupons_adjustment", cn.PreciseCouponsAdjustmentAmount, want.PreciseCoupons},
		{"precise_total", items.Sub(cn.PreciseCouponsAdjustmentAmount).Add(cn.PreciseTaxesAmount), want.PreciseTotal},
		{"taxes_rounding_adjustment", money.NewFromInt(cn.TaxesAmount).Sub(cn.PreciseTaxesAmount), want.RoundingAdjustment},
		{"taxes_rate", cn.TaxesRate, want.TaxesRate},
	}
	for _, d := range decs {
		// lago stores precise amounts with 5 decimal places, rounded
		// through floats: a difference within 0.00001 is the same amount.
		if d.want != "" && d.got.Sub(money.MustParse(d.want)).Abs().Cmp(invPreciseTolerance) > 0 {
			t.Errorf("%s: %s = %s, want = %s", step, d.name, d.got, d.want)
		}
	}
}

// invCreditable returns what credit notes may still credit on invoice id:
// its total less the totals of its credit notes (lago
// creditable_amount_cents).
func (s *scenario) invCreditable(id core.InvoiceID) int64 {
	s.T.Helper()
	left := s.Invoice(id).TotalAmount
	for _, cn := range s.CreditNotes(id) {
		left -= cn.TotalAmount
	}
	return left
}

func TestScenarioCreditNotesRounding(t *testing.T) {
	for _, tc := range []struct {
		source             string
		interval           period.Interval
		amount             int64
		rate               string
		start, paid, ended time.Time
		calendar           bool
		fees, taxes, total int64
		item               int64
		itemPrecise        string
		want               invCN
		appliedTax         int64
	}{
		{
			"spec/scenarios/credit_notes/credit_note_rounding_spec.rb:20", period.Monthly, 20000, "25",
			invAt("2025-09-18T16:00:00Z"), invAt("2025-09-18T16:30:00Z"), invAt("2025-09-18T16:40:00Z"), false,
			20000, 5000, 25000, -1, "",
			invCN{SubTotal: i64(19333), Taxes: i64(4833), Credit: i64(24166), Total: i64(24166)},
			-1,
		},
		{
			"spec/scenarios/credit_notes/credit_note_rounding_spec.rb:63", period.Monthly, 16000, "25",
			invAt("2025-09-18T16:00:00Z"), invAt("2025-09-18T16:30:00Z"), invAt("2025-09-18T16:40:00Z"), false,
			16000, 4000, 20000, 15467, "15466.66666",
			invCN{SubTotal: i64(15467), Taxes: i64(3867), Credit: i64(19334), Total: i64(19334)},
			3867,
		},
		{
			"spec/scenarios/credit_notes/credit_note_rounding_spec.rb:113", period.Weekly, 2999, "23.33",
			invAt("2025-10-06T16:00:00Z"), invAt("2025-10-06T16:30:00Z"), invAt("2025-10-06T16:30:00Z"), true,
			2999, 700, 3699, 2571, "2570.57142",
			invCN{SubTotal: i64(2571), Taxes: i64(600), Credit: i64(3171), Total: i64(3171)},
			600,
		},
	} {
		t.Run(tc.source, func(t *testing.T) {
			// source: spec/scenarios/credit_notes/credit_note_rounding_spec.rb:20
			s := newScenario(t, tc.start)
			s.invBETax("tax", tc.rate)
			c := s.Customer()
			p := s.Plan(tc.interval, tc.amount, invAdvance)
			opts := []func(*subscription.Subscription){invAnniversary}
			if tc.calendar {
				opts = nil
			}
			sub := s.invCreateSub(c, p, opts...)
			inv := s.invCustomerInvoices(c.ID)[0]
			if inv.FeesAmount != tc.fees || inv.TaxesAmount != tc.taxes || inv.TotalAmount != tc.total {
				t.Errorf("invoice fees, taxes, total = %d, %d, %d, want = %d, %d, %d", inv.FeesAmount, inv.TaxesAmount, inv.TotalAmount, tc.fees, tc.taxes, tc.total)
			}
			s.travelTo(tc.paid)
			s.invSetPaymentStatus(inv.ID, core.PaymentStatusSucceeded)
			s.travelTo(tc.ended)
			s.invTerminate(sub)
			cns := s.invCustomerCreditNotes(c.ID)
			if len(cns) != 1 {
				t.Fatalf("credit notes = %d, want = 1", len(cns))
			}
			cn := cns[0]
			invCheckCN(t, "credit note", cn, tc.want)
			if tc.item >= 0 {
				if len(cn.Items) != 1 || cn.Items[0].Amount != tc.item || !cn.Items[0].PreciseAmount.Round(5, money.HalfUp).Equal(money.MustParse(tc.itemPrecise)) {
					t.Errorf("items = %+v, want = one of %d (%s)", cn.Items, tc.item, tc.itemPrecise)
				}
			}
			if tc.appliedTax >= 0 && (len(cn.AppliedTaxes) != 1 || cn.AppliedTaxes[0].Amount != tc.appliedTax) {
				t.Errorf("applied taxes = %+v, want = one of %d", cn.AppliedTaxes, tc.appliedTax)
			}
		})
	}
}

func TestScenarioCreditNotesRoundingWithExistingCreditNote(t *testing.T) {
	// source: spec/scenarios/credit_notes/credit_note_rounding_spec.rb:146
	s := newScenario(t, invAt("2025-09-18T16:00:00Z"))
	s.invBETax("tax", "20")
	c := s.Customer()
	p := s.Plan(period.Monthly, 76900, invAdvance)
	s.invCreateSub(c, p, invAnniversary)
	inv := s.invCustomerInvoices(c.ID)[0]
	if inv.FeesAmount != 76900 || inv.TaxesAmount != 15380 || inv.TotalAmount != 92280 {
		t.Errorf("invoice fees, taxes, total = %d, %d, %d, want = 76900, 15380, 92280", inv.FeesAmount, inv.TaxesAmount, inv.TotalAmount)
	}
	s.travelTo(invAt("2025-09-19T16:30:00Z"))
	s.invSetPaymentStatus(inv.ID, core.PaymentStatusSucceeded)
	fee := s.Fees(inv.ID)[0]
	s.travelTo(invAt("2025-09-20T16:30:00Z"))
	cn := s.invCreditNote(creditnotes.CreateInput{InvoiceID: inv.ID, CreditAmount: 87224, Items: invItems(fee, 72686)})
	invCheckCN(t, "first", cn, invCN{SubTotal: i64(72686), Taxes: i64(14537), Refund: i64(0), Total: i64(87223), Coupons: i64(0)})

	// The note predates lago's rounding fix, which credited one cent more.
	cn.TotalAmount, cn.CreditAmount = 87224, 87224
	s.tx(func(ctx context.Context, r port.Repos) error { return r.CreditNotes.Update(ctx, cn) })

	est, err := s.invEstimate(inv.ID, invItems(fee, 4214))
	s.must(err)
	s.travelTo(invAt("2025-09-22T16:30:00Z"))
	cn = s.invCreditNote(creditnotes.CreateInput{InvoiceID: inv.ID, CreditAmount: est.CreditAmount, Items: invItems(fee, 4214)})
	invCheckCN(t, "second", cn, invCN{SubTotal: i64(4214), Taxes: i64(843), Refund: i64(0), Total: i64(5057), Coupons: i64(0)})
}

func TestScenarioCreditNotesPartialWithCoupons(t *testing.T) {
	// source: spec/scenarios/credit_notes/credit_note_spec.rb:58
	s := newScenario(t, invAt("2022-12-19T12:00:00Z"))
	tx := s.invBETax("tax", "10")
	planTax := s.Tax("plan_tax", "10")
	c := s.Customer()
	p1 := s.Plan(period.Monthly, 17900, invAdvance)
	p2 := s.Plan(period.Monthly, 39900, invAdvance, func(p *catalog.Plan) { p.TaxIDs = []core.TaxID{planTax.ID, tx.ID} })
	cp := s.FixedAmountCoupon(20000, invForeverCoupon, func(cp *coupon.Coupon) {
		cp.Reusable, cp.LimitedPlanIDs = false, []core.PlanID{p2.ID}
	})
	s.invCreateSub(c, p1, invAnniversary, invExternalID(c.ExternalID+"_1"))
	s.invCreateSub(c, p2, invAnniversary, invExternalID(c.ExternalID+"_2"))
	s.travelTo(invAt("2023-08-29T12:00:00Z"))
	s.invApplyCoupon(cp, c, 25000)
	s.travelTo(invAt("2023-10-19T12:00:00Z"))
	s.performBilling()

	invs := s.invCustomerInvoices(c.ID)
	inv := invs[len(invs)-1]
	if inv.FeesAmount != 57800 || inv.CouponsAmount != 25000 || !inv.TaxesRate.Equal(money.MustParse("14.54268")) || inv.TaxesAmount != 4770 || inv.TotalAmount != 37570 {
		t.Errorf("invoice fees, coupons, rate, taxes, total = %d, %d, %s, %d, %d, want = 57800, 25000, 14.54268, 4770, 37570",
			inv.FeesAmount, inv.CouponsAmount, inv.TaxesRate, inv.TaxesAmount, inv.TotalAmount)
	}
	fee1, fee2 := s.invFeeOfAmount(inv.ID, 17900), s.invFeeOfAmount(inv.ID, 39900)
	if !fee1.PreciseCouponsAmount.IsZero() || !fee2.PreciseCouponsAmount.Equal(money.NewFromInt(25000)) {
		t.Errorf("fee coupons = %s, %s, want = 0, 25000", fee1.PreciseCouponsAmount, fee2.PreciseCouponsAmount)
	}

	s.travelTo(invAt("2023-10-23T12:00:00Z"))
	s.invPay(inv.ID, 1200)
	est, err := s.invEstimate(inv.ID, invItems(fee1, 17900, fee2, 39900))
	s.must(err)
	invCheckCN(t, "full estimate", est, invCN{
		Taxes: i64(4770), SubTotal: i64(32800), Creditable: i64(37570), Refundable: i64(1200), Coupons: i64(25000), TaxesRate: "14.54268",
	})
	est, err = s.invEstimate(inv.ID, invItems(fee2, 26260))
	s.must(err)
	invCheckCN(t, "partial estimate", est, invCN{
		Taxes: i64(1961), SubTotal: i64(9806), Creditable: i64(11767), Refundable: i64(1200), Coupons: i64(16454), TaxesRate: "20",
	})
	s.invPay(inv.ID, 10567)
	cn := s.invCreditNote(creditnotes.CreateInput{InvoiceID: inv.ID, RefundAmount: 11767, Items: invItems(fee2, 26260)})
	invCheckCN(t, "credit note", cn, invCN{
		SubTotal: i64(9806), Taxes: i64(1961), Refund: i64(11767), Total: i64(11767), Coupons: i64(16454),
	})
}

func TestScenarioCreditNotesSameCouponTwice(t *testing.T) {
	// source: spec/scenarios/credit_notes/credit_note_spec.rb:271
	s := newScenario(t, invAt("2022-12-19T12:00:00Z"))
	s.invBETax("tax", "10")
	c := s.Customer()
	p := s.Plan(period.Monthly, 1999)
	mins := []int64{99290, 299770, 3130, 6460, 3130}
	for i, min := range mins {
		m := s.Metric(fmt.Sprintf("metric_%d", i), catalog.AggregationTypeCount, "")
		s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(ch *catalog.Charge) { ch.MinAmount = min })
	}
	cp := s.FixedAmountCoupon(1000, invForeverCoupon)
	s.invCreateSub(c, p, invAnniversary, invExternalID(c.ExternalID+"_1"))
	s.travelTo(invAt("2023-08-29T00:00:00Z"))
	s.invApplyCoupon(cp, c, 1000)
	s.invApplyCoupon(cp, c, 1000)
	s.travelTo(invAt("2023-10-19T00:00:00Z"))
	s.performBilling()

	invs := s.invCustomerInvoices(c.ID)
	inv := invs[len(invs)-1]
	if inv.FeesAmount != 413779 || inv.CouponsAmount != 2000 || !inv.TaxesRate.Equal(money.NewFromInt(10)) || inv.TaxesAmount != 41178 || inv.TotalAmount != 452957 {
		t.Errorf("invoice fees, coupons, rate, taxes, total = %d, %d, %s, %d, %d, want = 413779, 2000, 10, 41178, 452957",
			inv.FeesAmount, inv.CouponsAmount, inv.TaxesRate, inv.TaxesAmount, inv.TotalAmount)
	}
	for _, w := range []struct {
		amount  int64
		coupons string
	}{
		{99290, "479.91802"}, {299770, "1448.93772"}, {3130, "15.12884"}, {6460, "31.2244"}, {1999, "9.66216"},
	} {
		if got := s.invFeeOfAmount(inv.ID, w.amount).PreciseCouponsAmount; got.Sub(money.MustParse(w.coupons)).Abs().Cmp(invPreciseTolerance) > 0 {
			t.Errorf("fee %d coupons = %s, want = %s", w.amount, got, w.coupons)
		}
	}

	s.travelTo(invAt("2023-10-23T00:00:00Z"))
	s.invPay(inv.ID, 40)
	var items []any
	threes := 0
	for _, f := range s.Fees(inv.ID) {
		switch f.Amount {
		case 1999, 299770, 6460:
			items = append(items, f, 100)
		case 3130:
			if threes < 2 {
				items = append(items, f, 100)
			}
			threes++
		}
	}
	est, err := s.invEstimate(inv.ID, invItems(items...))
	s.must(err)
	invCheckCN(t, "estimate", est, invCN{
		Coupons: i64(2), SubTotal: i64(498), Taxes: i64(49), Creditable: i64(547), Refundable: i64(40), TaxesRate: "10",
	})
}

// invOneOffAddOns bills a one-off invoice to c of the add-ons with tax.
func (s *scenario) invOneOffAddOns(c *customer.Customer, tax string, addOns ...*catalog.AddOn) *invoice.Invoice {
	s.T.Helper()
	var fs []fees.OneOff
	for _, a := range addOns {
		fs = append(fs, fees.OneOff{AddOnID: a.ID, TaxCodes: []string{tax}})
	}
	inv, err := s.E.Invoices.CreateOneOff(s.Ctx, invoicing.OneOffInput{OrganizationID: s.OrgID(), CustomerID: c.ID, Currency: c.Currency, Fees: fs})
	s.must(err)
	s.drain()
	return s.Invoice(inv.ID)
}

func TestScenarioCreditNotesSmallItemsRounding(t *testing.T) {
	t.Run("two items refunded separately", func(t *testing.T) {
		// source: spec/scenarios/credit_notes/credit_note_spec.rb:395
		s := newScenario(t, invAt("2024-01-10T10:00:00Z"))
		s.invBETax("tax", "20")
		c := s.Customer()
		inv := s.invOneOffAddOns(c, "tax", s.AddOn(6833), s.AddOn(6833))
		if inv.TotalAmount != 16399 || inv.TaxesAmount != 2733 {
			t.Errorf("invoice total, taxes = %d, %d, want = 16399, 2733", inv.TotalAmount, inv.TaxesAmount)
		}
		fs := s.Fees(inv.ID)
		s.invPay(inv.ID, 500)
		est, err := s.invEstimate(inv.ID, invItems(fs[0], 6833))
		s.must(err)
		invCheckCN(t, "first estimate", est, invCN{
			Taxes: i64(1367), PreciseTaxes: "1366.6", SubTotal: i64(6833), Creditable: i64(8200), Refundable: i64(500), TaxesRate: "20",
		})
		s.invPay(inv.ID, 7700)
		cn := s.invCreditNote(creditnotes.CreateInput{InvoiceID: inv.ID, RefundAmount: 8200, Items: invItems(fs[0], 6833)})
		invCheckCN(t, "first", cn, invCN{
			SubTotal: i64(6833), Taxes: i64(1367), Refund: i64(8200), Total: i64(8200),
			PreciseTaxes: "1366.6", PreciseTotal: "8199.6", RoundingAdjustment: "0.4",
		})
		s.invPay(inv.ID, 8000)
		est, err = s.invEstimate(inv.ID, invItems(fs[1], 6833))
		s.must(err)
		invCheckCN(t, "second estimate", est, invCN{
			Taxes: i64(1366), PreciseTaxes: "1366.2", SubTotal: i64(6833), Creditable: i64(8199), Refundable: i64(8000), TaxesRate: "20",
		})
		s.invPay(inv.ID, 199)
		cn = s.invCreditNote(creditnotes.CreateInput{InvoiceID: inv.ID, RefundAmount: 8199, Items: invItems(fs[1], 6833)})
		invCheckCN(t, "second", cn, invCN{
			SubTotal: i64(6833), Refund: i64(8199), Total: i64(8199), Taxes: i64(1366),
			PreciseTaxes: "1366.2", PreciseTotal: "8199.2", RoundingAdjustment: "-0.2",
		})
	})

	t.Run("four items, some refunded in parts", func(t *testing.T) {
		// source: spec/scenarios/credit_notes/credit_note_spec.rb:521
		s := newScenario(t, invAt("2024-01-10T10:00:00Z"))
		s.invBETax("tax", "20")
		c := s.Customer()
		inv := s.invOneOffAddOns(c, "tax", s.AddOn(6833), s.AddOn(6833), s.AddOn(6833), s.AddOn(6833))
		if inv.TotalAmount != 32798 || inv.TaxesAmount != 5466 {
			t.Errorf("invoice total, taxes = %d, %d, want = 32798, 5466", inv.TotalAmount, inv.TaxesAmount)
		}
		fs := s.Fees(inv.ID)
		s.invSetPaymentStatus(inv.ID, core.PaymentStatusSucceeded)
		s.invPay(inv.ID, 30000)
		for i := range 3 {
			est, err := s.invEstimate(inv.ID, invItems(fs[i], 6833))
			s.must(err)
			invCheckCN(t, fmt.Sprintf("estimate %d", i), est, invCN{
				Taxes: i64(1367), PreciseTaxes: "1366.6", SubTotal: i64(6833), Creditable: i64(8200), Refundable: i64(8200), TaxesRate: "20",
			})
			cn := s.invCreditNote(creditnotes.CreateInput{InvoiceID: inv.ID, RefundAmount: 8200, Items: invItems(fs[i], 6833)})
			invCheckCN(t, fmt.Sprintf("credit note %d", i), cn, invCN{
				Refund: i64(8200), Total: i64(8200), Taxes: i64(1367), PreciseTaxes: "1366.6", PreciseTotal: "8199.6", RoundingAdjustment: "0.4",
			})
		}
		if got := s.invCreditable(inv.ID); got != 8198 {
			t.Errorf("creditable = %d, want = 8198", got)
		}
		for _, st := range []struct {
			item, taxes              int64
			precise                  string
			creditable, refundable   int64
			pay                      int64
			preciseTotal, adjustment string
			left                     int64
		}{
			{1367, 273, "273.4", 1640, 1640, 0, "1640.4", "-0.4", 6558},
			{2233, 447, "446.6", 2680, 2680, 0, "2679.6", "0.4", 3878},
			{3233, 645, "645.4", 3878, 1080, 2798, "3878.4", "-0.4", 0},
		} {
			est, err := s.invEstimate(inv.ID, invItems(fs[3], int(st.item)))
			s.must(err)
			step := fmt.Sprintf("part %d", st.item)
			invCheckCN(t, step+" estimate", est, invCN{
				Taxes: i64(st.taxes), PreciseTaxes: st.precise, SubTotal: i64(st.item),
				Creditable: i64(st.creditable), Refundable: i64(st.refundable), TaxesRate: "20",
			})
			if st.pay > 0 {
				s.invPay(inv.ID, st.pay)
			}
			cn := s.invCreditNote(creditnotes.CreateInput{InvoiceID: inv.ID, RefundAmount: st.creditable, Items: invItems(fs[3], int(st.item))})
			invCheckCN(t, step, cn, invCN{
				SubTotal: i64(st.item), Refund: i64(st.creditable), Total: i64(st.creditable), Taxes: i64(st.taxes),
				PreciseTaxes: st.precise, PreciseTotal: st.preciseTotal, RoundingAdjustment: st.adjustment,
			})
			if got := s.invCreditable(inv.ID); got != st.left {
				t.Errorf("%s: creditable = %d, want = %d", step, got, st.left)
			}
		}
	})

	t.Run("small items with coupons", func(t *testing.T) {
		// source: spec/scenarios/credit_notes/credit_note_spec.rb:797
		s := newScenario(t, invAt("2022-12-19T12:00:00Z"))
		s.invBETax("tax", "20")
		c := s.Customer()
		p := s.Plan(period.Monthly, 1999)
		for i, min := range []int64{6833, 20033} {
			m := s.Metric(fmt.Sprintf("metric_%d", i), catalog.AggregationTypeCount, "")
			s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(ch *catalog.Charge) { ch.MinAmount = min })
		}
		cp := s.FixedAmountCoupon(1000, invForeverCoupon)
		s.invCreateSub(c, p, invAnniversary, invExternalID(c.ExternalID+"_1"))
		s.travelTo(invAt("2023-08-29T00:00:00Z"))
		s.invApplyCoupon(cp, c, 1000)
		s.travelTo(invAt("2023-10-19T00:00:00Z"))
		s.performBilling()
		invs := s.invCustomerInvoices(c.ID)
		inv := invs[len(invs)-1]
		if inv.TotalAmount != 33438 {
			t.Errorf("invoice total = %d, want = 33438", inv.TotalAmount)
		}
		var subFee *invoice.Fee
		for _, f := range s.Fees(inv.ID) {
			if f.Type == core.FeeTypeSubscription {
				subFee = f
			}
		}
		for _, st := range []struct {
			fee                  *invoice.Fee
			taxes, sub, credit   int64
			coupons              int64
			preciseTaxes         string
			preciseCoupons       string
			preciseTotal, adjust string
			left                 int64
		}{
			{subFee, 386, 1930, 2316, 69, "385.94932", "69.25342", "2315.6959", "0.05068", 31122},
			{s.invFeeOfAmount(inv.ID, 6833), 1319, 6596, 7915, 237, "1319.25547", "236.72267", "7915.5328", "-0.25547", 23207},
			// lago's 3868.00001, 23206.97611 and -0.00001 come from amounts it
			// stored with 5 decimal places; the exact values are kept.
			{s.invFeeOfAmount(inv.ID, 20033), 3868, 19339, 23207, 694, "3868", "694.0239", "23206.97609", "0", 0},
		} {
			step := fmt.Sprintf("fee %d", st.fee.Amount)
			est, err := s.invEstimate(inv.ID, invItems(st.fee, int(st.fee.Amount)))
			s.must(err)
			invCheckCN(t, step+" estimate", est, invCN{
				Taxes: i64(st.taxes), PreciseTaxes: st.preciseTaxes, SubTotal: i64(st.sub),
				Creditable: i64(st.credit), Coupons: i64(st.coupons), TaxesRate: "20",
			})
			cn := s.invCreditNote(creditnotes.CreateInput{InvoiceID: inv.ID, CreditAmount: st.credit, Items: invItems(st.fee, int(st.fee.Amount))})
			invCheckCN(t, step, cn, invCN{
				Credit: i64(st.credit), Total: i64(st.credit), Taxes: i64(st.taxes), PreciseTaxes: st.preciseTaxes,
				PreciseCoupons: st.preciseCoupons, PreciseTotal: st.preciseTotal, RoundingAdjustment: st.adjust,
			})
			if got := s.invCreditable(inv.ID); got != st.left {
				t.Errorf("%s: creditable = %d, want = %d", step, got, st.left)
			}
		}
	})
}

func TestScenarioCreditNotesPrepaidCreditInvoice(t *testing.T) {
	// source: spec/scenarios/credit_notes/credit_note_spec.rb:922
	s := newScenario(t, invAt("2024-01-10T10:00:00Z"))
	c := s.Customer()
	name := "Wallet1"
	w, err := s.E.Wallets.Create(s.Ctx, s.OrgID(), wallets.CreateInput{CustomerID: c.ID, Name: &name, Currency: c.Currency, RateAmount: money.One})
	s.must(err)
	s.drain()
	paid := money.MustParse("15")
	txs, err := s.E.Wallets.CreateTransactions(s.Ctx, s.OrgID(), wallets.TransactionInput{WalletID: w.ID, PaidCredits: &paid})
	s.must(err)
	s.drain()
	if len(txs) != 1 {
		t.Fatalf("transactions = %d, want = one pending purchase", len(txs))
	}
	// The purchase stays pending until the invoice is paid.
	if txs[0].Status != wallet.SettlementStatusPending || txs[0].TransactionStatus != wallet.TransactionStatusPurchased {
		t.Fatalf("transaction = %s %s, want = pending purchased", txs[0].Status, txs[0].TransactionStatus)
	}
	credits := s.invOfType(c.ID, invoice.TypeCredit)
	if len(credits) != 1 {
		t.Fatalf("credit invoices = %d, want = 1", len(credits))
	}
	inv := credits[0]
	if inv.Status != invoice.StatusFinalized {
		t.Errorf("credit invoice status = %s, want = finalized", inv.Status)
	}
	fee := s.Fees(inv.ID)[0]
	refused := func(step, code string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), code) {
			t.Errorf("%s: error = %v, want = %s", step, err, code)
		}
	}
	_, err = s.invEstimate(inv.ID, invItems(fee, 15))
	refused("pending estimate", creditnotes.CodeInvalidTypeOrStatus, err)
	_, err = s.E.CreditNotes.Create(s.Ctx, creditnotes.CreateInput{OrganizationID: s.OrgID(), InvoiceID: inv.ID, RefundAmount: 15, Items: invItems(fee, 15)})
	refused("pending credit note", creditnotes.CodeInvalidTypeOrStatus, err)

	// Paying the invoice runs lago's Invoices::PrepaidCreditJob.
	s.invSetPaymentStatus(inv.ID, core.PaymentStatusSucceeded)
	s.must(s.E.Wallets.HandleCreditInvoicePayment(s.Ctx, s.OrgID(), inv.ID, true))
	if got := s.ReloadWallet(w.ID).BalanceAmount; got != 1500 {
		t.Errorf("balance = %d, want = 1500", got)
	}
	s.invPay(inv.ID, 1500)

	est, err := s.invEstimate(inv.ID, invItems(fee, 10))
	s.must(err)
	invCheckCN(t, "estimate", est, invCN{SubTotal: i64(10), Refundable: i64(10), Creditable: i64(0)})
	cn := s.invCreditNote(creditnotes.CreateInput{InvoiceID: inv.ID, RefundAmount: 500, Items: invItems(fee, 500)})
	invCheckCN(t, "credit note", cn, invCN{Refund: i64(500), Total: i64(500)})
	var last *wallet.Transaction
	s.tx(func(ctx context.Context, r port.Repos) error {
		page, err := r.WalletTransactions.List(ctx, s.OrgID(), wallet.TransactionFilter{WalletID: w.ID}, core.PageRequest{Size: 100})
		for _, x := range page.Items {
			if last == nil || !x.CreatedAt.Before(last.CreatedAt) {
				last = x
			}
		}
		return err
	})
	if last == nil || last.Status != wallet.SettlementStatusSettled || last.TransactionStatus != wallet.TransactionStatusVoided || last.CreditNoteID != cn.ID {
		t.Errorf("last transaction = %+v, want = settled void of the credit note", last)
	}
	if got := s.ReloadWallet(w.ID).BalanceAmount; got != 1000 {
		t.Errorf("balance = %d, want = 1000", got)
	}

	voided := money.MustParse("9.95")
	_, err = s.E.Wallets.CreateTransactions(s.Ctx, s.OrgID(), wallets.TransactionInput{WalletID: w.ID, VoidedCredits: &voided})
	s.must(err)
	s.drain()
	if got := s.ReloadWallet(w.ID).BalanceAmount; got != 5 {
		t.Errorf("balance = %d, want = 5", got)
	}
	_, err = s.invEstimate(inv.ID, invItems(fee, 10))
	refused("estimate over balance", creditnote.CodeHigherThanWalletBalance, err)
	_, err = s.E.CreditNotes.Create(s.Ctx, creditnotes.CreateInput{OrganizationID: s.OrgID(), InvoiceID: inv.ID, RefundAmount: 10, Items: invItems(fee, 10)})
	refused("credit note over balance", creditnote.CodeHigherThanWalletBalance, err)
	if got := s.ReloadWallet(w.ID).BalanceAmount; got != 5 {
		t.Errorf("balance = %d, want = 5", got)
	}

	_, err = s.E.Wallets.Terminate(s.Ctx, s.OrgID(), w.ID)
	s.must(err)
	s.drain()
	_, err = s.invEstimate(inv.ID, invItems(fee, 1))
	refused("terminated estimate", creditnotes.CodeInvalidTypeOrStatus, err)
	_, err = s.E.CreditNotes.Create(s.Ctx, creditnotes.CreateInput{OrganizationID: s.OrgID(), InvoiceID: inv.ID, RefundAmount: 1, Items: invItems(fee, 1)})
	refused("terminated credit note", creditnotes.CodeInvalidTypeOrStatus, err)
}
