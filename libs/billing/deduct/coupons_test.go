package deduct_test

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

func fixedApplied(freq coupon.Frequency, amount int64, remaining *int64, duration *int) *coupon.AppliedCoupon {
	return &coupon.AppliedCoupon{
		Amount: &amount, Currency: billingtest.USD, Frequency: freq, RemainingAmount: remaining,
		FrequencyDuration: duration, FrequencyDurationRemaining: duration, Status: coupon.StatusActive,
	}
}

func percentApplied(freq coupon.Frequency, rate string, duration *int) *coupon.AppliedCoupon {
	r := money.MustParse(rate)
	return &coupon.AppliedCoupon{
		PercentageRate: &r, Frequency: freq, FrequencyDuration: duration,
		FrequencyDurationRemaining: duration, Status: coupon.StatusActive,
	}
}

func TestCouponAmount(t *testing.T) {
	tests := []struct {
		name    string
		applied *coupon.AppliedCoupon
		base    string
		want    int64
	}{
		// source: spec/services/applied_coupons/amount_service_spec.rb:17
		{"fixed once", fixedApplied(coupon.FrequencyOnce, 12, ptr[int64](12), nil), "300", 12},
		// source: spec/services/applied_coupons/amount_service_spec.rb:27
		{"zero base", fixedApplied(coupon.FrequencyOnce, 12, ptr[int64](12), nil), "0", 0},
		// source: spec/services/applied_coupons/amount_service_spec.rb:38
		{"amount above base", fixedApplied(coupon.FrequencyOnce, 12, ptr[int64](12), nil), "6", 6},
		// source: spec/services/applied_coupons/amount_service_spec.rb:55
		{"partially used", fixedApplied(coupon.FrequencyOnce, 12, ptr[int64](6), nil), "300", 6},
		// source: spec/services/applied_coupons/amount_service_spec.rb:70
		{"percentage", percentApplied(coupon.FrequencyOnce, "20", nil), "300", 60},
		// source: spec/services/applied_coupons/amount_service_spec.rb:92
		{"recurring fixed", fixedApplied(coupon.FrequencyRecurring, 12, nil, ptr(3)), "300", 12},
		// source: spec/services/applied_coupons/amount_service_spec.rb:102
		{"recurring fixed above base", fixedApplied(coupon.FrequencyRecurring, 12, nil, ptr(3)), "6", 6},
		// source: spec/services/applied_coupons/amount_service_spec.rb:125
		{"forever fixed", fixedApplied(coupon.FrequencyForever, 12, nil, nil), "300", 12},
		// source: spec/services/applied_coupons/amount_service_spec.rb:135
		{"forever fixed above base", fixedApplied(coupon.FrequencyForever, 12, nil, nil), "6", 6},
		// source: spec/services/applied_coupons/amount_service_spec.rb:160
		{"recurring percentage", percentApplied(coupon.FrequencyRecurring, "20", ptr(3)), "300", 60},
		{"fractional base floors the cap", fixedApplied(coupon.FrequencyForever, 12, nil, nil), "6.7", 6},
		{"percentage rounds half up", percentApplied(coupon.FrequencyOnce, "10", nil), "125", 13},
		{"percentage above 100 caps at base", percentApplied(coupon.FrequencyOnce, "150", nil), "40", 40},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deduct.CouponAmount(tt.applied, money.MustParse(tt.base))
			eq(t, "amount", got, tt.want)
		})
	}
}

// couponCase is one case of the applied coupon credit spec: an invoice of
// base split as base/3*2 and base/3 over two fees.
type couponCase struct {
	name         string
	base         int64
	coupon       func(h *billingtest.Harness) *coupon.Coupon
	applied      func(*coupon.AppliedCoupon)
	invoiceCcy   string
	wantCredit   int64 // -1: no credit
	wantFees     [2]string
	wantStatus   coupon.Status
	wantDuration *int
}

func TestApplyCouponCredit(t *testing.T) {
	fixed := func(h *billingtest.Harness) *coupon.Coupon { return h.FixedAmountCoupon(12) }
	recurring := func(freq coupon.Frequency, d *int) func(h *billingtest.Harness) *coupon.Coupon {
		return func(h *billingtest.Harness) *coupon.Coupon {
			return h.FixedAmountCoupon(12, func(c *coupon.Coupon) { c.Frequency, c.FrequencyDuration = freq, d })
		}
	}
	percent := func(freq coupon.Frequency, d *int) func(h *billingtest.Harness) *coupon.Coupon {
		return func(h *billingtest.Harness) *coupon.Coupon {
			return h.PercentageCoupon("20", func(c *coupon.Coupon) { c.Frequency, c.FrequencyDuration = freq, d })
		}
	}
	tests := []couponCase{
		// source: spec/services/credits/applied_coupon_service_spec.rb:52
		{name: "fixed once", base: 300, coupon: fixed, wantCredit: 12, wantFees: [2]string{"8", "4"}, wantStatus: coupon.StatusTerminated},
		// source: spec/services/credits/applied_coupon_service_spec.rb:87
		{name: "amount above invoice", base: 6, coupon: fixed, wantCredit: 6, wantFees: [2]string{"4", "2"}, wantStatus: coupon.StatusActive},
		// source: spec/services/credits/applied_coupon_service_spec.rb:131
		{
			name: "partially used", base: 300, coupon: fixed,
			applied:    func(a *coupon.AppliedCoupon) { a.RemainingAmount = ptr[int64](6) },
			wantCredit: 6, wantFees: [2]string{"4", "2"}, wantStatus: coupon.StatusTerminated,
		},
		// source: spec/services/credits/applied_coupon_service_spec.rb:159
		{name: "percentage", base: 300, coupon: percent(coupon.FrequencyOnce, nil), wantCredit: 60, wantFees: [2]string{"40", "20"}, wantStatus: coupon.StatusTerminated},
		// source: spec/services/credits/applied_coupon_service_spec.rb:194
		{
			name: "recurring fixed", base: 300, coupon: recurring(coupon.FrequencyRecurring, ptr(3)),
			wantCredit: 12, wantFees: [2]string{"8", "4"}, wantStatus: coupon.StatusActive, wantDuration: ptr(2),
		},
		// source: spec/services/credits/applied_coupon_service_spec.rb:219
		{
			name: "recurring fixed above invoice", base: 6, coupon: recurring(coupon.FrequencyRecurring, ptr(3)),
			wantCredit: 6, wantFees: [2]string{"4", "2"}, wantStatus: coupon.StatusActive, wantDuration: ptr(2),
		},
		// source: spec/services/credits/applied_coupon_service_spec.rb:245
		{name: "forever fixed", base: 300, coupon: recurring(coupon.FrequencyForever, nil), wantCredit: 12, wantFees: [2]string{"8", "4"}, wantStatus: coupon.StatusActive},
		// source: spec/services/credits/applied_coupon_service_spec.rb:270
		{name: "forever fixed above invoice", base: 6, coupon: recurring(coupon.FrequencyForever, nil), wantCredit: 6, wantFees: [2]string{"4", "2"}, wantStatus: coupon.StatusActive},
		// source: spec/services/credits/applied_coupon_service_spec.rb:298
		{
			name: "recurring percentage", base: 300, coupon: percent(coupon.FrequencyRecurring, ptr(3)),
			wantCredit: 60, wantFees: [2]string{"40", "20"}, wantStatus: coupon.StatusActive, wantDuration: ptr(2),
		},
		// source: spec/services/credits/applied_coupon_service_spec.rb:332
		{
			name: "recurring percentage last period", base: 300, coupon: percent(coupon.FrequencyRecurring, ptr(3)),
			applied:    func(a *coupon.AppliedCoupon) { a.FrequencyDurationRemaining = ptr(1) },
			wantCredit: 60, wantFees: [2]string{"40", "20"}, wantStatus: coupon.StatusTerminated, wantDuration: ptr(0),
		},
		// source: spec/services/credits/applied_coupon_service_spec.rb:366
		{
			name: "currency mismatch", base: 300, coupon: fixed,
			applied:    func(a *coupon.AppliedCoupon) { a.Currency = money.MustCurrency("NOK") },
			wantCredit: -1, wantFees: [2]string{"0", "0"}, wantStatus: coupon.StatusActive,
		},
		// source: spec/services/credits/applied_coupon_service_spec.rb:385
		{
			name: "percentage regardless of currency", base: 300, coupon: percent(coupon.FrequencyOnce, nil),
			invoiceCcy: "EUR", wantCredit: 60, wantFees: [2]string{"40", "20"}, wantStatus: coupon.StatusTerminated,
		},
		// source: spec/services/credits/applied_coupon_service_spec.rb:525
		{
			name: "recurring with no period left", base: 300, coupon: recurring(coupon.FrequencyRecurring, ptr(3)),
			applied:    func(a *coupon.AppliedCoupon) { a.FrequencyDurationRemaining = ptr(0) },
			wantCredit: 12, wantFees: [2]string{"8", "4"}, wantStatus: coupon.StatusTerminated, wantDuration: ptr(0),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			h := f.h
			c := tt.coupon(h)
			var opts []func(*coupon.AppliedCoupon)
			if tt.applied != nil {
				opts = append(opts, tt.applied)
			}
			a := h.ApplyCoupon(c, f.cust, opts...)
			inv := h.StoredInvoice(f.cust, func(inv *invoice.Invoice) {
				if tt.invoiceCcy != "" {
					inv.Currency = money.MustCurrency(tt.invoiceCcy)
				}
			})
			f1 := h.StoredFee(inv, tt.base/3*2)
			f2 := h.StoredFee(inv, tt.base/3)
			out, fees, err := f.before(inv, deduct.Options{})
			mustOK(t, err)
			credits := h.InvoiceCredits(inv.ID)
			if tt.wantCredit < 0 {
				eq(t, "credits", len(credits), 0)
			} else {
				if len(credits) != 1 {
					t.Fatalf("credits: got = %d, want = 1", len(credits))
				}
				cr := credits[0]
				eq(t, "credit amount", cr.Amount, tt.wantCredit)
				eq(t, "credit currency", cr.Currency.Code(), inv.Currency.Code())
				eq(t, "credit invoice", cr.InvoiceID, inv.ID)
				eq(t, "credit applied coupon", cr.AppliedCouponID, a.ID)
				eq(t, "before taxes", cr.BeforeTaxes, true)
				eq(t, "result coupons", out.Coupons, tt.wantCredit)
				eq(t, "invoice coupons", inv.CouponsAmount, tt.wantCredit)
				eq(t, "invoice sub total", inv.SubTotalExcludingTaxesAmount, tt.base/3*2+tt.base/3-tt.wantCredit)
			}
			byID := map[core.FeeID]*invoice.Fee{}
			for _, fe := range fees {
				byID[fe.ID] = fe
			}
			eqDec(t, "fee1 coupons", byID[f1.ID].PreciseCouponsAmount, tt.wantFees[0])
			eqDec(t, "fee2 coupons", byID[f2.ID].PreciseCouponsAmount, tt.wantFees[1])
			got := h.ReloadAppliedCoupon(a.ID)
			eq(t, "applied coupon status", got.Status, tt.wantStatus)
			if tt.wantDuration != nil {
				if got.FrequencyDurationRemaining == nil {
					t.Fatalf("duration remaining: got = nil, want = %d", *tt.wantDuration)
				}
				eq(t, "duration remaining", *got.FrequencyDurationRemaining, *tt.wantDuration)
			}
		})
	}
}

// source: spec/services/credits/applied_coupon_service_spec.rb:76
func TestApplyCouponZeroBase(t *testing.T) {
	f := newFixture(t)
	a := f.h.ApplyCoupon(f.h.FixedAmountCoupon(12), f.cust)
	inv := f.h.StoredInvoice(f.cust)
	f.h.StoredFee(inv, 0)
	out, _, err := f.before(inv, deduct.Options{})
	mustOK(t, err)
	// lago creates a 0 credit; Tanvo skips the coupon (SKIPPED.md).
	eq(t, "coupons", out.Coupons, 0)
	eq(t, "credits", len(f.h.InvoiceCredits(inv.ID)), 0)
	eq(t, "applied coupon status", f.h.ReloadAppliedCoupon(a.ID).Status, coupon.StatusActive)
}

// source: spec/services/credits/applied_coupon_service_spec.rb:116
func TestApplyCouponAlreadyCredited(t *testing.T) {
	f := newFixture(t)
	f.h.ApplyCoupon(f.h.FixedAmountCoupon(12, func(c *coupon.Coupon) { c.Frequency = coupon.FrequencyForever }), f.cust)
	inv := f.h.StoredInvoice(f.cust)
	f.h.StoredFee(inv, 300)
	_, _, err := f.before(inv, deduct.Options{})
	mustOK(t, err)
	inv.CouponsAmount = 0
	_, _, err = f.before(inv, deduct.Options{})
	mustOK(t, err)
	eq(t, "credits", len(f.h.InvoiceCredits(inv.ID)), 1)
}

func TestApplyCouponLimitations(t *testing.T) {
	tests := []struct {
		name       string
		limit      string // plan, other_plan, metric, other_metric
		wantCredit int64
		wantFees   [2]string
	}{
		// source: spec/services/credits/applied_coupon_service_spec.rb:405
		{"plan", "plan", 12, [2]string{"12", "0"}},
		// source: spec/services/credits/applied_coupon_service_spec.rb:422
		{"other plan", "other_plan", 0, [2]string{"0", "0"}},
		// source: spec/services/credits/applied_coupon_service_spec.rb:444
		{"metric", "metric", 12, [2]string{"12", "0"}},
		// source: spec/services/credits/applied_coupon_service_spec.rb:502
		{"other metric", "other_metric", 0, [2]string{"0", "0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			h := f.h
			p := h.Plan(period.Monthly, 1000)
			other := h.Plan(period.Monthly, 1000)
			m := h.Metric("api", catalog.AggregationTypeCount, "")
			om := h.Metric("other", catalog.AggregationTypeCount, "")
			ch := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
			och := h.Charge(p, om, catalog.ChargeModelStandard, `{"amount":"1"}`)
			sub := h.Subscription(f.cust, p)
			c := h.FixedAmountCoupon(12, func(c *coupon.Coupon) {
				switch tt.limit {
				case "plan":
					c.LimitedPlanIDs = []core.PlanID{p.ID}
				case "other_plan":
					c.LimitedPlanIDs = []core.PlanID{other.ID}
				case "metric":
					c.LimitedBillableMetricIDs = []core.BillableMetricID{m.ID}
				case "other_metric":
					c.LimitedBillableMetricIDs = []core.BillableMetricID{om.ID}
				}
			})
			h.ApplyCoupon(c, f.cust)
			inv := h.StoredInvoice(f.cust)
			var f1 *invoice.Fee
			if tt.limit == "plan" || tt.limit == "other_plan" {
				f1 = h.StoredFee(inv, 300, billingtest.SubscriptionFee(sub))
			} else {
				f1 = h.StoredFee(inv, 300, billingtest.ChargeFee(sub, ch.ID))
			}
			_ = och
			f2 := h.StoredFee(inv, 100)
			out, fees, err := f.before(inv, deduct.Options{})
			mustOK(t, err)
			eq(t, "coupons", out.Coupons, tt.wantCredit)
			for _, fe := range fees {
				switch fe.ID {
				case f1.ID:
					eqDec(t, "fee1 coupons", fe.PreciseCouponsAmount, tt.wantFees[0])
				case f2.ID:
					eqDec(t, "fee2 coupons", fe.PreciseCouponsAmount, tt.wantFees[1])
				}
			}
		})
	}
}

// source: spec/services/credits/applied_coupon_service_spec.rb:483
func TestApplyCouponMetricAfterProgressiveCredits(t *testing.T) {
	f := newFixture(t)
	h := f.h
	p := h.Plan(period.Monthly, 1000)
	m := h.Metric("api", catalog.AggregationTypeCount, "")
	ch := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	sub := h.Subscription(f.cust, p)
	h.ApplyCoupon(h.FixedAmountCoupon(12, func(c *coupon.Coupon) { c.LimitedBillableMetricIDs = []core.BillableMetricID{m.ID} }), f.cust)
	inv := h.StoredInvoice(f.cust, func(inv *invoice.Invoice) { inv.ProgressiveBillingCreditAmount = 60 })
	f1 := h.StoredFee(inv, 50, billingtest.ChargeFee(sub, ch.ID), func(fe *invoice.Fee) { fe.PreciseCouponsAmount = money.NewFromInt(15) })
	f2 := h.StoredFee(inv, 150, billingtest.ChargeFee(sub, ch.ID), func(fe *invoice.Fee) { fe.PreciseCouponsAmount = money.NewFromInt(45) })
	f3 := h.StoredFee(inv, 100, billingtest.SubscriptionFee(sub))
	out, fees, err := f.before(inv, deduct.Options{})
	mustOK(t, err)
	eq(t, "coupons", out.Coupons, 12)
	want := map[core.FeeID]string{f1.ID: "18", f2.ID: "54", f3.ID: "0"}
	for _, fe := range fees {
		eqDec(t, "fee coupons", fe.PreciseCouponsAmount, want[fe.ID])
	}
}

type couponSpec struct {
	percent string // percentage rate, else fixed amount
	amount  int64
	limit   string // "", "plan", "other_plan", "metric"
	ccy     string
	status  coupon.Status
}

type couponsCase struct {
	name        string
	fee         int64
	chargeFee   int64 // a charge fee of the metric, on another plan
	coupons     []couponSpec
	wantCoupons int64
	wantCredits int
}

func couponsCases() []couponsCase {
	return []couponsCase{
		// source: spec/services/credits/applied_coupons_service_spec.rb:63
		{name: "fixed then percentage", fee: 100, coupons: []couponSpec{{amount: 10}, {percent: "20"}}, wantCoupons: 28, wantCredits: 2},
		// source: spec/services/credits/applied_coupons_service_spec.rb:83
		{name: "first coupon covers the invoice", fee: 5, coupons: []couponSpec{{amount: 10}, {percent: "20"}}, wantCoupons: 5, wantCredits: 1},
		// source: spec/services/credits/applied_coupons_service_spec.rb:106
		{name: "both fixed", fee: 100, coupons: []couponSpec{{amount: 10}, {amount: 20}}, wantCoupons: 30, wantCredits: 2},
		// source: spec/services/credits/applied_coupons_service_spec.rb:128
		{name: "both percentage", fee: 100, coupons: []couponSpec{{percent: "15"}, {percent: "20"}}, wantCoupons: 32, wantCredits: 2},
		// source: spec/services/credits/applied_coupons_service_spec.rb:148
		{
			name: "other currency", fee: 100,
			coupons:     []couponSpec{{amount: 10, ccy: "NOK"}, {percent: "20", status: coupon.StatusTerminated}},
			wantCoupons: 0, wantCredits: 0,
		},
		// source: spec/services/credits/applied_coupons_service_spec.rb:184
		{
			name: "no plan limitation applies", fee: 100,
			coupons:     []couponSpec{{amount: 10, limit: "other_plan"}, {amount: 20, limit: "other_plan"}},
			wantCoupons: 0, wantCredits: 0,
		},
		// source: spec/services/credits/applied_coupons_service_spec.rb:226
		{
			name: "one plan limitation applies", fee: 100,
			coupons:     []couponSpec{{amount: 10, limit: "other_plan"}, {amount: 20, limit: "plan"}},
			wantCoupons: 20, wantCredits: 1,
		},
		// source: spec/services/credits/applied_coupons_service_spec.rb:268
		{
			name: "both plan limitations apply", fee: 100,
			coupons:     []couponSpec{{amount: 10, limit: "plan"}, {amount: 20, limit: "plan"}},
			wantCoupons: 30, wantCredits: 2,
		},
		// source: spec/services/credits/applied_coupons_service_spec.rb:350
		// The lago invoice states fees of 100 over fees of 87; Tanvo sums the fees.
		{
			name: "metric limitation first", fee: 75, chargeFee: 12,
			coupons:     []couponSpec{{amount: 82, limit: "plan"}, {amount: 5, limit: "metric"}, {amount: 20, limit: "other_plan"}},
			wantCoupons: 80, wantCredits: 2,
		},
	}
}

// runCoupons applies the coupons of tt to an invoice with one subscription
// fee; in a preview the subscription is not stored and its plan is passed.
func runCoupons(t *testing.T, tt couponsCase, preview bool) {
	t.Helper()
	f := newFixture(t)
	h := f.h
	p := h.Plan(period.Monthly, 100)
	other := h.Plan(period.Monthly, 100)
	m := h.Metric("api", catalog.AggregationTypeCount, "")
	ch := h.Charge(other, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	var subID core.SubscriptionID
	opts := deduct.Options{Preview: preview}
	if preview {
		subID = core.NewID[core.SubscriptionID](h.IDs)
		opts.Plans = map[core.SubscriptionID]*catalog.Plan{subID: p}
	} else {
		subID = h.Subscription(f.cust, p).ID
	}
	var applied []*coupon.AppliedCoupon
	for i, sp := range tt.coupons {
		limit := func(c *coupon.Coupon) {
			switch sp.limit {
			case "plan":
				c.LimitedPlanIDs = []core.PlanID{p.ID}
			case "other_plan":
				c.LimitedPlanIDs = []core.PlanID{other.ID}
			case "metric":
				c.LimitedBillableMetricIDs = []core.BillableMetricID{m.ID}
			}
		}
		var c *coupon.Coupon
		if sp.percent != "" {
			c = h.PercentageCoupon(sp.percent, limit)
		} else {
			c = h.FixedAmountCoupon(sp.amount, limit)
		}
		applied = append(applied, h.ApplyCoupon(c, f.cust, func(a *coupon.AppliedCoupon) {
			a.CreatedAt = a.CreatedAt.Add(time.Duration(i) * time.Hour)
			if sp.ccy != "" {
				a.Currency = money.MustCurrency(sp.ccy)
			}
			if sp.status != "" {
				a.Status = sp.status
			}
		}))
	}
	inv := h.StoredInvoice(f.cust)
	h.StoredFee(inv, tt.fee, func(fe *invoice.Fee) { fe.SubscriptionID = subID })
	if tt.chargeFee > 0 {
		h.StoredFee(inv, tt.chargeFee, func(fe *invoice.Fee) { fe.Type, fe.ChargeID = core.FeeTypeCharge, ch.ID })
	}
	out, _, err := f.before(inv, opts)
	mustOK(t, err)
	eq(t, "coupons", out.Coupons, tt.wantCoupons)
	eq(t, "invoice coupons", inv.CouponsAmount, tt.wantCoupons)
	eq(t, "sub total", inv.SubTotalExcludingTaxesAmount, tt.fee+tt.chargeFee-tt.wantCoupons)
	eq(t, "result credits", len(out.Credits), tt.wantCredits)
	stored := tt.wantCredits
	if preview {
		stored = 0
	}
	eq(t, "stored credits", len(h.InvoiceCredits(inv.ID)), stored)
	if preview {
		for _, a := range applied {
			got := h.ReloadAppliedCoupon(a.ID)
			eq(t, "untouched status", got.Status, a.Status)
			if a.RemainingAmount != nil {
				eq(t, "untouched remaining", *got.RemainingAmount, *a.RemainingAmount)
			}
		}
	}
}

func TestApplyCoupons(t *testing.T) {
	for _, tt := range couponsCases() {
		t.Run(tt.name, func(t *testing.T) { runCoupons(t, tt, false) })
	}
}

// The coupon preview runs the same application in memory (lago
// Coupons::PreviewService).
func TestPreviewCoupons(t *testing.T) {
	sources := map[string]bool{
		// source: spec/services/coupons/preview_service_spec.rb:66
		"fixed then percentage": true,
		// source: spec/services/coupons/preview_service_spec.rb:85
		"first coupon covers the invoice": true,
		// source: spec/services/coupons/preview_service_spec.rb:108
		"both fixed": true,
		// source: spec/services/coupons/preview_service_spec.rb:129
		"both percentage": true,
		// source: spec/services/coupons/preview_service_spec.rb:148
		"other currency": true,
		// source: spec/services/coupons/preview_service_spec.rb:184
		"no plan limitation applies": true,
		// source: spec/services/coupons/preview_service_spec.rb:227
		"one plan limitation applies": true,
		// source: spec/services/coupons/preview_service_spec.rb:269
		"both plan limitations apply": true,
	}
	for _, tt := range couponsCases() {
		if !sources[tt.name] {
			continue
		}
		t.Run(tt.name, func(t *testing.T) { runCoupons(t, tt, true) })
	}
}

// A limited coupon capped at its fractional base spreads that base, not
// its truncated credit, so that each targeted fee is fully discounted:
// the base is 3 × 99.5 = 298.5, the credit 298, and each fee's coupons
// reach its amount.
//
// source: app/services/credits/applied_coupon_service.rb:34 (weighting on credit_amount, the fractional base)
// source: app/services/applied_coupons/amount_service.rb:33
func TestApplyCouponCappedSpreadsFractionalBase(t *testing.T) {
	f := newFixture(t)
	h := f.h
	p := h.Plan(period.Monthly, 1000)
	m := h.Metric("api", catalog.AggregationTypeCount, "")
	ch := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	sub := h.Subscription(f.cust, p)
	c := h.FixedAmountCoupon(1000, func(c *coupon.Coupon) { c.LimitedBillableMetricIDs = []core.BillableMetricID{m.ID} })
	h.ApplyCoupon(c, f.cust)
	inv := h.StoredInvoice(f.cust)
	for range 3 {
		h.StoredFee(inv, 100, billingtest.ChargeFee(sub, ch.ID), func(fe *invoice.Fee) { fe.PreciseCouponsAmount = money.MustParse("0.5") })
	}
	out, fees, err := f.before(inv, deduct.Options{})
	mustOK(t, err)
	eq(t, "coupons", out.Coupons, 298)
	for _, fe := range fees {
		eqDec(t, "fee coupons", fe.PreciseCouponsAmount, "100")
	}
}
