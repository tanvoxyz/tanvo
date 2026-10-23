package billing_test

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// TestRootTerminatePayInAdvance ports spec/scenarios/terminate_pay_in_advance_spec.rb:
// an in-advance plan subscribed and terminated on 8 Feb 2023 is credited
// for its unused days.
func TestRootTerminatePayInAdvance(t *testing.T) {
	tests := []struct {
		source      string
		tz          string
		anniversary bool
		subTotal    int64
		cnSubTotal  int64
		cnTaxes     int64
		cnTotal     int64
	}{
		{source: "spec/scenarios/terminate_pay_in_advance_spec.rb:14", subTotal: 4500, cnSubTotal: 3571, cnTaxes: 714, cnTotal: 4285},
		{source: "spec/scenarios/terminate_pay_in_advance_spec.rb:55", tz: "Asia/Tokyo", subTotal: 4500, cnSubTotal: 3571, cnTaxes: 714, cnTotal: 4285},
		{source: "spec/scenarios/terminate_pay_in_advance_spec.rb:97", tz: "America/Los_Angeles", subTotal: 4715, cnSubTotal: -1, cnTaxes: -1, cnTotal: 4500},
		{source: "spec/scenarios/terminate_pay_in_advance_spec.rb:132", anniversary: true, subTotal: 6000, cnSubTotal: 4821, cnTaxes: 964, cnTotal: 5785},
		{source: "spec/scenarios/terminate_pay_in_advance_spec.rb:174", tz: "Asia/Tokyo", anniversary: true, subTotal: 6000, cnSubTotal: 4821, cnTaxes: 964, cnTotal: 5785},
		{source: "spec/scenarios/terminate_pay_in_advance_spec.rb:217", tz: "America/Los_Angeles", anniversary: true, subTotal: 6000, cnSubTotal: 4821, cnTaxes: 964, cnTotal: 5785},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			s := newScenario(t, billingtest.Date(2023, 2, 8))
			tax := s.Tax("vat", "20")
			s.UpdateBillingEntity(func(be *organization.BillingEntity) { be.TaxIDs = []core.TaxID{tax.ID} })
			var copts []func(*customer.Customer)
			if tt.tz != "" {
				copts = append(copts, rootTimezone(tt.tz))
			}
			c := s.Customer(copts...)
			p := s.Plan(period.Monthly, 5000, rootInAdvance)
			var sopts []func(*subscription.Subscription)
			if tt.anniversary {
				sopts = append(sopts, billingtest.Anniversary)
			}
			sub := s.rootSubscribe(c, p, sopts...)
			invs := s.rootInvoices(c, sub)
			if len(invs) != 1 {
				t.Fatalf("invoices after create = %d, want = 1", len(invs))
			}
			first := invs[0]
			if got := first.TotalAmount; got != tt.subTotal {
				t.Errorf("subscription invoice total = %d, want = %d", got, tt.subTotal)
			}
			sub = s.rootTerminate(sub)
			if sub.Status != subscription.StatusTerminated {
				t.Errorf("status = %s, want = terminated", sub.Status)
			}
			invs = s.rootInvoices(c, sub)
			if len(invs) != 2 {
				t.Fatalf("invoices after terminate = %d, want = 2", len(invs))
			}
			if got := invs[1].TotalAmount; got != 0 {
				t.Errorf("termination invoice total = %d, want = 0", got)
			}
			cns := s.CreditNotes(first.ID)
			if len(cns) != 1 {
				t.Fatalf("credit notes = %d, want = 1", len(cns))
			}
			cn := cns[0]
			if tt.cnSubTotal >= 0 && (cn.SubTotalExcludingTaxesAmount != tt.cnSubTotal || cn.TaxesAmount != tt.cnTaxes || cn.CreditAmount != tt.cnTotal) {
				t.Errorf("credit note sub total, taxes, credit = %d, %d, %d, want = %d, %d, %d",
					cn.SubTotalExcludingTaxesAmount, cn.TaxesAmount, cn.CreditAmount, tt.cnSubTotal, tt.cnTaxes, tt.cnTotal)
			}
			if cn.TotalAmount != tt.cnTotal {
				t.Errorf("credit note total = %d, want = %d", cn.TotalAmount, tt.cnTotal)
			}
		})
	}
}

// rootVAT applies a 20% tax to the default billing entity.
func rootVAT(s *scenario) {
	tax := s.Tax("vat", "20")
	s.UpdateBillingEntity(func(be *organization.BillingEntity) { be.TaxIDs = []core.TaxID{tax.ID} })
}

// source: spec/scenarios/spending_minimum_spec.rb:17
func TestRootSpendingMinimumGracePeriod(t *testing.T) {
	s := newScenario(t, billingtest.Date(2023, 1, 8, 8))
	rootVAT(s)
	c := s.Customer(rootGrace(3))
	p := s.Plan(period.Monthly, 5000, rootInAdvance)
	m := s.Metric("api", catalog.AggregationTypeCount, "")
	sub := s.rootSubscribe(c, p)
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"8"}`, rootMinAmount(1000))
	invs := s.rootInvoices(c, sub)
	if got, want := invs[0].TotalAmount, int64(4645); got != want {
		t.Errorf("subscription invoice total = %d, want = %d", got, want)
	}
	s.travelTo(billingtest.Date(2023, 2, 1, 6))
	s.performBilling()
	invs = s.rootInvoices(c, sub)
	last := invs[len(invs)-1]

	s.travelTo(billingtest.Date(2023, 2, 25, 6))
	// The period is half-open: an event at the termination instant falls
	// outside it (lago counts it), so the event is a second earlier.
	s.ingestAt(sub, "api", s.Now().Add(-time.Second))
	sub = s.rootTerminate(sub)
	if sub.Status != subscription.StatusTerminated {
		t.Errorf("status = %s, want = terminated", sub.Status)
	}
	invs = s.rootInvoices(c, sub)
	if len(invs) != 3 {
		t.Fatalf("invoices = %d, want = 3", len(invs))
	}
	term := invs[2]
	if term.Status != invoice.StatusDraft {
		t.Errorf("termination invoice status = %s, want = draft", term.Status)
	}
	usage, trueUps := s.rootFeesSplit(term.ID)
	if len(usage) != 1 || len(trueUps) != 1 {
		t.Fatalf("fees = %d + %d true-up, want = 1 + 1", len(usage), len(trueUps))
	}
	rootWantFee(t, "usage fee", usage[0], 800, 160, "1")
	rootWantFee(t, "true-up fee", trueUps[0], 93, 19, "1")
	rootWantInvoice(t, term, 893, 179, 0, 1072)

	last = s.rootRefresh(last)
	cns := s.CreditNotes(last.ID)
	if len(cns) != 1 {
		t.Fatalf("credit notes = %d, want = 1", len(cns))
	}
	cn := cns[0]
	if cn.Status != creditnote.StatusDraft {
		t.Errorf("credit note status = %s, want = draft", cn.Status)
	}
	if cn.SubTotalExcludingTaxesAmount != 536 || cn.CreditAmount != 643 || cn.TaxesAmount != 107 || cn.TotalAmount != 643 {
		t.Errorf("credit note = %d/%d/%d/%d, want = 536/643/107/643", cn.SubTotalExcludingTaxesAmount, cn.CreditAmount, cn.TaxesAmount, cn.TotalAmount)
	}
	if got := s.rootRefresh(term).TotalAmount; got != 1072 {
		t.Errorf("refreshed termination total = %d, want = 1072", got)
	}
	if got := s.rootFinalize(last).Status; got != invoice.StatusFinalized {
		t.Errorf("last invoice status = %s, want = finalized", got)
	}
	if got := s.ReloadCreditNote(cn.ID).Status; got != creditnote.StatusFinalized {
		t.Errorf("credit note status = %s, want = finalized", got)
	}
	term = s.rootFinalize(term)
	if term.Status != invoice.StatusFinalized {
		t.Errorf("termination status = %s, want = finalized", term.Status)
	}
	rootWantInvoice(t, term, 893, 179, 643, 429)
}

// source: spec/scenarios/spending_minimum_spec.rb:137
func TestRootSpendingMinimumFilters(t *testing.T) {
	s := newScenario(t, billingtest.Date(2023, 1, 8))
	rootVAT(s)
	c := s.Customer()
	p := s.Plan(period.Monthly, 5000, rootInAdvance)
	m := s.Metric("api", catalog.AggregationTypeCount, "", func(m *catalog.BillableMetric) {
		m.Filters = []catalog.MetricFilter{{ID: core.NewID[core.BillableMetricFilterID](s.IDs), Key: "region", Values: []string{"europe", "usa"}}}
	})
	sub := s.rootSubscribe(c, p)
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"0"}`, rootMinAmount(10_000), func(ch *catalog.Charge) {
		ch.Filters = []catalog.ChargeFilter{
			billingtest.ChargeFilter(map[string][]string{"region": {"europe"}}, `{"amount":"20"}`),
			billingtest.ChargeFilter(map[string][]string{"region": {"usa"}}, `{"amount":"50"}`),
		}
	})
	if got, want := s.rootInvoices(c, sub)[0].TotalAmount, int64(4645); got != want {
		t.Errorf("subscription invoice total = %d, want = %d", got, want)
	}
	s.travelTo(billingtest.Date(2023, 2, 1, 6))
	s.performBilling()
	s.travelTo(billingtest.Date(2023, 2, 25, 8))
	s.ingestAt(sub, "api", s.Now().Add(-time.Second), "region", "usa")
	s.ingestAt(sub, "api", s.Now().Add(-time.Second), "region", "europe")
	sub = s.rootTerminate(sub)
	invs := s.rootInvoices(c, sub)
	if len(invs) != 3 {
		t.Fatalf("invoices = %d, want = 3", len(invs))
	}
	term := invs[2]
	if term.Status != invoice.StatusFinalized {
		t.Errorf("termination status = %s, want = finalized", term.Status)
	}
	usage, trueUps := s.rootFeesSplit(term.ID)
	var amounts []int64
	for _, f := range usage {
		amounts = append(amounts, f.Amount)
	}
	slices.Sort(amounts)
	if !slices.Equal(amounts, []int64{0, 2000, 5000}) || len(trueUps) != 1 {
		t.Fatalf("usage fees = %v + %d true-up, want = [0 2000 5000] + 1", amounts, len(trueUps))
	}
	rootWantFee(t, "true-up fee", trueUps[0], 1929, 386, "1")
	rootWantInvoice(t, term, 8929, 1786, 643, 10_072)
}

// rootWantFee checks a fee's amount, taxes and units.
func rootWantFee(t *testing.T, name string, f *invoice.Fee, amount, taxes int64, units string) {
	t.Helper()
	if f.Amount != amount || f.TaxesAmount != taxes || f.Units.String() != units {
		t.Errorf("%s amount/taxes/units = %d/%d/%s, want = %d/%d/%s", name, f.Amount, f.TaxesAmount, f.Units, amount, taxes, units)
	}
}

// rootWantInvoice checks an invoice's fees, taxes, credit notes and total
// amounts.
func rootWantInvoice(t *testing.T, inv *invoice.Invoice, fees, taxes, creditNotes, total int64) {
	t.Helper()
	if inv.FeesAmount != fees || inv.TaxesAmount != taxes || inv.CreditNotesAmount != creditNotes || inv.TotalAmount != total {
		t.Errorf("invoice fees/taxes/credit notes/total = %d/%d/%d/%d, want = %d/%d/%d/%d",
			inv.FeesAmount, inv.TaxesAmount, inv.CreditNotesAmount, inv.TotalAmount, fees, taxes, creditNotes, total)
	}
}

// rootEUR is the euro.
var rootEUR = money.MustCurrency("EUR")

// rootEURCustomer creates a euro customer.
func rootEURCustomer(s *scenario) *customer.Customer {
	return s.Customer(func(c *customer.Customer) { c.Currency = rootEUR })
}

// rootEURPlan creates a monthly in-arrears euro plan with taxes.
func rootEURPlan(s *scenario, amount int64, taxes ...*catalog.Tax) *catalog.Plan {
	return s.Plan(period.Monthly, amount, func(p *catalog.Plan) {
		p.Currency = rootEUR
		for _, t := range taxes {
			p.TaxIDs = append(p.TaxIDs, t.ID)
		}
	})
}

// rootEURCoupon creates and applies a fixed euro coupon used once.
func rootEURCoupon(s *scenario, c *customer.Customer, amount int64) {
	cp := s.FixedAmountCoupon(amount, func(cp *coupon.Coupon) { cp.Currency = rootEUR })
	s.ApplyCoupon(cp, c)
}

// rootFeeOf returns the fees of an invoice of a type, charge or
// subscription.
func rootFeesOf(fees []*invoice.Fee, keep func(*invoice.Fee) bool) []*invoice.Fee {
	var out []*invoice.Fee
	for _, f := range fees {
		if keep(f) {
			out = append(out, f)
		}
	}
	return out
}

// rootWantTaxedFee checks a fee's amount, taxes, tax rate and precise
// coupons amount.
func rootWantTaxedFee(t *testing.T, name string, fees []*invoice.Fee, amount, taxes int64, rate, coupons string) {
	t.Helper()
	if len(fees) != 1 {
		t.Errorf("%s fees = %d, want = 1", name, len(fees))
		return
	}
	f := fees[0]
	if f.Amount != amount || f.TaxesAmount != taxes || !rootDecEq(f.TaxesRate, rate) || !rootDecEq(f.PreciseCouponsAmount, coupons) {
		t.Errorf("%s amount/taxes/rate/coupons = %d/%d/%s/%s, want = %d/%d/%s/%s",
			name, f.Amount, f.TaxesAmount, f.TaxesRate, f.PreciseCouponsAmount, amount, taxes, rate, coupons)
	}
}

// rootDecEq reports whether d equals the decimal s.
func rootDecEq(d money.Decimal, s string) bool { return d.Cmp(money.MustParse(s)) == 0 }

// source: spec/scenarios/taxes_on_invoice_spec.rb:14
func TestRootTaxesOnInvoice(t *testing.T) {
	s := newScenario(t, billingtest.Date(2023, 1, 1))
	banking := s.Tax("banking_rates", "10")
	fr := s.Tax("sales_tax_fr", "0")
	sales := s.Tax("sales_tax", "20")
	c := rootEURCustomer(s)
	fx := s.Metric("fx_transfers", catalog.AggregationTypeSum, "total")
	cards := s.Metric("cards", catalog.AggregationTypeCount, "")
	p := rootEURPlan(s, 10_000, banking)
	fxCharge := s.Charge(p, fx, catalog.ChargeModelStandard, `{"amount":"1"}`, func(ch *catalog.Charge) { ch.TaxIDs = []core.TaxID{fr.ID} })
	cardsCharge := s.Charge(p, cards, catalog.ChargeModelStandard, `{"amount":"30"}`, rootMinAmount(5000), func(ch *catalog.Charge) { ch.TaxIDs = []core.TaxID{sales.ID} })
	sub := s.rootSubscribe(c, p)
	rootEURCoupon(s, c, 1000)
	s.ingest(sub, "fx_transfers", "total", "50")
	s.ingest(sub, "cards")

	s.travelTo(billingtest.Date(2023, 2, 1))
	s.performBilling()
	invs := s.Invoices(c.ID)
	if len(invs) != 1 {
		t.Fatalf("invoices = %d, want = 1", len(invs))
	}
	inv := invs[0]
	fees := s.Fees(inv.ID)
	if len(fees) != 4 {
		t.Fatalf("fees = %d, want = 4", len(fees))
	}
	rootWantTaxedFee(t, "subscription", rootFeesOf(fees, func(f *invoice.Fee) bool { return f.Type == core.FeeTypeSubscription }), 10_000, 950, "10", "500")
	fxFees := rootFeesOf(fees, func(f *invoice.Fee) bool { return f.ChargeID == fxCharge.ID })
	rootWantTaxedFee(t, "fx transfers", fxFees, 5000, 0, "0", "250")
	if len(fxFees) == 1 && (fxFees[0].EventsCount != 1 || fxFees[0].Units.String() != "50") {
		t.Errorf("fx transfers events/units = %d/%s, want = 1/50", fxFees[0].EventsCount, fxFees[0].Units)
	}
	cardFees := rootFeesOf(fees, func(f *invoice.Fee) bool { return f.ChargeID == cardsCharge.ID && f.TrueUpParentFeeID.IsZero() })
	rootWantTaxedFee(t, "cards", cardFees, 3000, 570, "20", "150")
	trueUps := rootFeesOf(fees, func(f *invoice.Fee) bool { return f.ChargeID == cardsCharge.ID && !f.TrueUpParentFeeID.IsZero() })
	rootWantTaxedFee(t, "cards true-up", trueUps, 2000, 380, "20", "100")
	if len(trueUps) == 1 && (trueUps[0].EventsCount != 0 || trueUps[0].Units.String() != "1") {
		t.Errorf("true-up events/units = %d/%s, want = 0/1", trueUps[0].EventsCount, trueUps[0].Units)
	}
	if inv.FeesAmount != 20_000 || inv.CouponsAmount != 1000 || inv.TaxesAmount != 1900 ||
		inv.SubTotalExcludingTaxesAmount != 19_000 || inv.SubTotalIncludingTaxesAmount != 20_900 || inv.TotalAmount != 20_900 {
		t.Errorf("invoice fees/coupons/taxes/excl/incl/total = %d/%d/%d/%d/%d/%d, want = 20000/1000/1900/19000/20900/20900",
			inv.FeesAmount, inv.CouponsAmount, inv.TaxesAmount, inv.SubTotalExcludingTaxesAmount, inv.SubTotalIncludingTaxesAmount, inv.TotalAmount)
	}
}

func TestRootTaxesCoveredByCoupons(t *testing.T) {
	tests := []struct {
		source  string
		plans   []int64
		coupons []int64
	}{
		{source: "spec/scenarios/taxes_on_invoice_spec.rb:163", plans: []int64{10_000}, coupons: []int64{1000, 11_000}},
		{source: "spec/scenarios/taxes_on_invoice_spec.rb:244", plans: []int64{10_000, 5000}, coupons: []int64{10_000, 10_000}},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			s := newScenario(t, billingtest.Date(2023, 1, 1))
			banking := s.Tax("banking_rates", "10")
			c := rootEURCustomer(s)
			var subs []*subscription.Subscription
			for _, amount := range tt.plans {
				subs = append(subs, s.rootSubscribe(c, rootEURPlan(s, amount, banking)))
			}
			for _, amount := range tt.coupons {
				rootEURCoupon(s, c, amount)
			}
			s.travelTo(billingtest.Date(2023, 2, 1))
			s.performBilling()
			invs := s.Invoices(c.ID)
			if len(invs) != 1 {
				t.Fatalf("invoices = %d, want = 1", len(invs))
			}
			fees := s.Fees(invs[0].ID)
			if len(fees) != len(tt.plans) {
				t.Fatalf("fees = %d, want = %d", len(fees), len(tt.plans))
			}
			for i, sub := range subs {
				amount := tt.plans[i]
				rootWantTaxedFee(t, "subscription", rootFeesOf(fees, func(f *invoice.Fee) bool { return f.SubscriptionID == sub.ID }),
					amount, 0, "10", money.NewFromInt(amount).String())
			}
		})
	}
}

func TestRootRecurringGraduatedProratedCrossPeriod(t *testing.T) {
	periodOne := time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC)
	periodTwo := time.Date(2026, 5, 8, 0, 1, 0, 0, time.UTC)
	inPeriodTwo := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	setup := func(t *testing.T) (*scenario, *subscription.Subscription) {
		s := newScenario(t, periodOne)
		c := s.Customer()
		p := s.Plan(period.Monthly, 0)
		m := s.Metric("amount", catalog.AggregationTypeSum, "amount", func(m *catalog.BillableMetric) { m.Recurring = true })
		s.Charge(p, m, catalog.ChargeModelGraduated,
			`{"graduated_ranges":[{"from_value":0,"to_value":2,"per_unit_amount":"0","flat_amount":"0"},{"from_value":3,"to_value":null,"per_unit_amount":"150","flat_amount":"0"}]}`,
			func(ch *catalog.Charge) { ch.Prorated = true })
		return s, s.rootSubscribe(c, p, billingtest.Anniversary)
	}
	usageOf := func(t *testing.T, s *scenario, sub *subscription.Subscription) *invoice.Fee {
		t.Helper()
		u := s.rootUsage(sub, usage.Options{})
		if len(u.Fees) != 1 {
			t.Fatalf("usage fees = %d, want = 1", len(u.Fees))
		}
		return u.Fees[0]
	}

	// source: spec/scenarios/recurring_graduated_prorated_cross_period_spec.rb:54
	t.Run("net unit priced in the free tier", func(t *testing.T) {
		s, sub := setup(t)
		s.travelTo(periodTwo.Add(-time.Hour))
		for range 3 {
			s.ingest(sub, "amount", "amount", "-1")
		}
		s.travelTo(periodTwo)
		s.rootBill(sub)
		invs := s.rootInvoices(s.ReloadCustomer(sub.CustomerID), sub)
		if len(invs) == 0 || invs[len(invs)-1].TotalAmount != 0 {
			t.Fatalf("period one invoice total != 0 (%d invoices)", len(invs))
		}
		s.travelTo(inPeriodTwo)
		for range 4 {
			s.ingest(sub, "amount", "amount", "1")
		}
		f := usageOf(t, s, sub)
		if f.Units.String() != "1" || f.Amount != 0 {
			t.Errorf("units/amount = %s/%d, want = 1/0", f.Units, f.Amount)
		}
	})

	// source: spec/scenarios/recurring_graduated_prorated_cross_period_spec.rb:96
	t.Run("backdated events of period one", func(t *testing.T) {
		s, sub := setup(t)
		s.travelTo(periodOne.AddDate(0, 0, 1))
		for range 3 {
			s.ingest(sub, "amount", "amount", "-1")
		}
		s.travelTo(periodTwo)
		s.rootBill(sub)
		s.travelTo(inPeriodTwo)
		for range 4 {
			s.ingest(sub, "amount", "amount", "1")
		}
		s.ingestAt(sub, "amount", periodOne.AddDate(0, 0, 5), "amount", "1")
		if f := usageOf(t, s, sub); f.Units.String() != "2" {
			t.Errorf("units = %s, want = 2", f.Units)
		}
	})
}

// TestRootCustomerUsagePeriod ports the usage periods of a yearly
// subscription started in the past; lago's inclusive to_datetime
// (23:59:59) is the exclusive end one second later.
func TestRootCustomerUsagePeriod(t *testing.T) {
	tests := []struct {
		source string
		tz     string
		to     time.Time
	}{
		{source: "spec/scenarios/customer_usage_spec.rb:14", tz: "UTC", to: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
		{source: "spec/scenarios/customer_usage_spec.rb:36", tz: "Europe/Berlin", to: time.Date(2023, 12, 31, 23, 0, 0, 0, time.UTC)},
		{source: "spec/scenarios/customer_usage_spec.rb:59", tz: "America/Los_Angeles", to: time.Date(2024, 1, 1, 8, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			s := newScenario(t, billingtest.Date(2023, 8, 8, 9, 30))
			c := s.Customer(rootTimezone(tt.tz), func(c *customer.Customer) { c.Currency = rootEUR })
			p := s.Plan(period.Yearly, 700, func(p *catalog.Plan) { p.Currency = rootEUR })
			sub := s.rootSubscribe(c, p, billingtest.SubscribedAt(billingtest.Date(2023, 1, 1, 9, 30)))
			u := s.rootUsage(sub, usage.Options{})
			if want := billingtest.Date(2023, 1, 1, 9, 30); !u.From.Equal(want) || !u.To.Equal(tt.to) {
				t.Errorf("usage period = %s..%s, want = %s..%s", u.From, u.To, want, tt.to)
			}
		})
	}
}

// rootGroupedUsage sets up the grouped usage of customer_usage_spec.rb:80
// or :207: a monthly anniversary subscription of 1 Mar 2024 and events of
// 5 Mar, read on 10 Mar.
func rootGroupedUsage(t *testing.T, multiLevel bool) (*scenario, *subscription.Subscription, []*catalog.Charge) {
	s := newScenario(t, billingtest.Date(2024, 3, 1, 10))
	c := rootEURCustomer(s)
	p := rootEURPlan(s, 0)
	var charges []*catalog.Charge
	if multiLevel {
		m := s.Metric("m", catalog.AggregationTypeSum, "units")
		charges = append(charges, s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"10","pricing_group_keys":["workspace","user"]}`))
	} else {
		m1 := s.Metric("m1", catalog.AggregationTypeSum, "units")
		m2 := s.Metric("m2", catalog.AggregationTypeSum, "units")
		charges = append(charges,
			s.Charge(p, m1, catalog.ChargeModelStandard, `{"amount":"10","pricing_group_keys":["user"]}`),
			s.Charge(p, m2, catalog.ChargeModelStandard, `{"amount":"5","pricing_group_keys":["user"]}`))
	}
	sub := s.rootSubscribe(c, p, billingtest.Anniversary)
	s.travelTo(billingtest.Date(2024, 3, 5, 10))
	for i := range 10 {
		user := strconv.Itoa(i)
		if multiLevel {
			ws := "workspace_a"
			if i >= 5 {
				ws = "workspace_b"
			}
			s.ingest(sub, "m", "workspace", ws, "user", user, "units", "3")
			continue
		}
		s.ingest(sub, "m1", "user", user, "units", "5")
	}
	if !multiLevel {
		for i := range 10 {
			s.ingest(sub, "m2", "user", strconv.Itoa(i), "units", "3")
		}
	}
	s.travelTo(billingtest.Date(2024, 3, 10, 10))
	return s, sub, charges
}

// source: spec/scenarios/customer_usage_spec.rb:177
func TestRootCustomerUsageFilterByCharge(t *testing.T) {
	s, sub, charges := rootGroupedUsage(t, false)
	u := s.rootUsage(sub, usage.Options{SkipTaxes: true, ChargeIDs: []core.ChargeID{charges[0].ID}})
	if len(u.Fees) != 10 {
		t.Fatalf("fees = %d, want = 10", len(u.Fees))
	}
	var users []string
	for _, f := range u.Fees {
		if f.ChargeID != charges[0].ID || f.Units.String() != "5" || f.EventsCount != 1 || f.Amount != 5000 {
			t.Errorf("fee charge/units/events/amount = %s/%s/%d/%d, want = %s/5/1/5000", f.ChargeID, f.Units, f.EventsCount, f.Amount, charges[0].ID)
		}
		users = append(users, f.GroupedBy["user"])
	}
	slices.Sort(users)
	if want := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}; !slices.Equal(users, want) {
		t.Errorf("users = %v, want = %v", users, want)
	}
}

// source: spec/scenarios/customer_usage_spec.rb:313
func TestRootCustomerUsageMultiLevelGroups(t *testing.T) {
	s, sub, charges := rootGroupedUsage(t, true)
	u := s.rootUsage(sub, usage.Options{SkipTaxes: true})
	if len(u.Fees) != 10 {
		t.Fatalf("fees = %d, want = 10", len(u.Fees))
	}
	for _, f := range u.Fees {
		if f.ChargeID != charges[0].ID || f.Units.String() != "3" || f.EventsCount != 1 || f.Amount != 3000 || len(f.GroupedBy) != 2 || f.GroupedBy["workspace"] == "" || f.GroupedBy["user"] == "" {
			t.Errorf("fee units/events/amount/grouped = %s/%d/%d/%v, want = 3/1/3000/workspace+user", f.Units, f.EventsCount, f.Amount, f.GroupedBy)
		}
	}
}

// rootDecNear reports whether d is within 0.00001 of the decimal s (lago
// rounds precise amounts to 5 places).
func rootDecNear(d money.Decimal, s string) bool {
	return d.Sub(money.MustParse(s)).Abs().LessThanOrEqual(money.MustParse("0.00001"))
}

// source: spec/scenarios/coupons_breakdown_spec.rb:14
func TestRootCouponsBreakdown(t *testing.T) {
	s := newScenario(t, billingtest.Date(2023, 1, 1))
	tax1 := s.Tax("banking_rates1", "10")
	tax2 := s.Tax("banking_rates2", "20")
	c := rootEURCustomer(s)
	var metrics []*catalog.BillableMetric
	for i := 1; i <= 8; i++ {
		metrics = append(metrics, s.Metric(fmt.Sprintf("bm%d", i), catalog.AggregationTypeSum, fmt.Sprintf("total%d", i)))
	}
	taxes := []*catalog.Tax{tax1, tax2, tax1, tax1}
	var plans []*catalog.Plan
	var subs []*subscription.Subscription
	for k := range 2 {
		p := rootEURPlan(s, 0)
		for i := range 4 {
			s.Charge(p, metrics[4*k+i], catalog.ChargeModelStandard, `{"amount":"1"}`, func(ch *catalog.Charge) { ch.TaxIDs = []core.TaxID{taxes[i].ID} })
		}
		plans = append(plans, p)
		subs = append(subs, s.rootSubscribe(c, p))
	}
	expires := s.Now().AddDate(0, 0, 50)
	for _, cp := range []struct {
		amount  int64
		metrics []core.BillableMetricID
		plans   []core.PlanID
	}{
		{amount: 2000, metrics: []core.BillableMetricID{metrics[0].ID, metrics[1].ID}},
		{amount: 1000, plans: []core.PlanID{plans[1].ID}},
		{amount: 500},
	} {
		coup := s.FixedAmountCoupon(cp.amount, func(x *coupon.Coupon) {
			x.Currency, x.LimitedBillableMetricIDs, x.LimitedPlanIDs = rootEUR, cp.metrics, cp.plans
			x.Expiration, x.ExpirationAt = coupon.ExpirationTimeLimit, &expires
		})
		s.ApplyCoupon(coup, c)
	}
	for k, sub := range subs {
		for i := range 4 {
			n := 4*k + i + 1
			s.ingest(sub, fmt.Sprintf("bm%d", n), fmt.Sprintf("total%d", n), strconv.Itoa(10*(i+1)))
		}
	}
	s.travelTo(billingtest.Date(2023, 2, 1))
	s.performBilling()
	invs := s.Invoices(c.ID)
	if len(invs) != 1 {
		t.Fatalf("invoices = %d, want = 1", len(invs))
	}
	inv := invs[0]
	fees := s.Fees(inv.ID)
	want := []struct {
		amount, taxes int64
		rate, coupons string
	}{
		{1000, 32, "10", "676.47059"},
		{2000, 129, "20", "1352.94117"},
		{3000, 291, "10", "88.23529"},
		{4000, 388, "10", "117.64706"},
		{1000, 87, "10", "126.47059"},
		{2000, 349, "20", "252.94118"},
		{3000, 262, "10", "379.41176"},
		{4000, 349, "10", "505.88235"},
	}
	for i, w := range want {
		var f *invoice.Fee
		for _, x := range fees {
			if x.Type == core.FeeTypeCharge && x.SubscriptionID == subs[i/4].ID && rootChargeMetric(s, x) == metrics[i].ID {
				f = x
			}
		}
		if f == nil {
			t.Errorf("bm%d fee missing", i+1)
			continue
		}
		if f.Amount != w.amount || f.TaxesAmount != w.taxes || !rootDecEq(f.TaxesRate, w.rate) || !rootDecNear(f.PreciseCouponsAmount, w.coupons) {
			t.Errorf("bm%d amount/taxes/rate/coupons = %d/%d/%s/%s, want = %d/%d/%s/%s",
				i+1, f.Amount, f.TaxesAmount, f.TaxesRate, f.PreciseCouponsAmount, w.amount, w.taxes, w.rate, w.coupons)
		}
	}
	if inv.FeesAmount != 20_000 || inv.CouponsAmount != 3500 || inv.SubTotalExcludingTaxesAmount != 16_500 || inv.TaxesAmount != 1889 || inv.TotalAmount != 18_389 {
		t.Errorf("invoice fees/coupons/excl/taxes/total = %d/%d/%d/%d/%d, want = 20000/3500/16500/1889/18389",
			inv.FeesAmount, inv.CouponsAmount, inv.SubTotalExcludingTaxesAmount, inv.TaxesAmount, inv.TotalAmount)
	}
}

// rootChargeMetric returns the billable metric of a charge fee.
func rootChargeMetric(s *scenario, f *invoice.Fee) core.BillableMetricID {
	var id core.BillableMetricID
	s.tx(func(ctx context.Context, r port.Repos) error {
		ch, err := r.Charges.Get(ctx, s.OrgID(), f.ChargeID)
		if err == nil {
			id = ch.BillableMetricID
		}
		return err
	})
	return id
}

// rootPBCase is a case of the progressive billing and coupons matrix of
// coupons_breakdown_spec.rb:340.
type rootPBCase struct {
	source     string
	thresholds []int64
	usage      []rootUsageEvent
	frequency  coupon.Frequency
	percentage bool
	limited    bool
	paid       int64
	refund     int64
	remaining  int
}

// rootUsageEvent is an event of n units days after the start.
type rootUsageEvent struct {
	days int
	n    int
}

func TestRootProgressiveBillingCoupons(t *testing.T) {
	sc1 := []rootUsageEvent{{5, 5}, {15, 30}}
	sc2 := []rootUsageEvent{{5, 30}, {15, 5}}
	sc3 := []rootUsageEvent{{5, 5}, {10, 25}, {15, 20}}
	sc4 := []rootUsageEvent{{5, 40}, {5, -40}}
	sc5 := []rootUsageEvent{{5, 40}, {10, -20}}
	const (
		once      = coupon.FrequencyOnce
		forever   = coupon.FrequencyForever
		recurring = coupon.FrequencyRecurring
	)
	f := "spec/scenarios/coupons_breakdown_spec.rb:"
	tests := []rootPBCase{
		{source: f + "418", thresholds: []int64{500}, usage: sc1, frequency: once, paid: 1500, remaining: -1},
		{source: f + "419", thresholds: []int64{500}, usage: sc1, frequency: forever, paid: 1000, remaining: -1},
		{source: f + "420", thresholds: []int64{500}, usage: sc1, frequency: forever, percentage: true, paid: 1750, remaining: -1},
		{source: f + "421", thresholds: []int64{500}, usage: sc1, frequency: forever, percentage: true, limited: true, paid: 1750, remaining: -1},
		{source: f + "432", thresholds: []int64{3000}, usage: sc2, frequency: once, paid: 1500, remaining: -1},
		{source: f + "433", thresholds: []int64{3000}, usage: sc2, frequency: forever, paid: 1000, remaining: -1},
		{source: f + "434", thresholds: []int64{3000}, usage: sc2, frequency: forever, percentage: true, paid: 1750, remaining: -1},
		{source: f + "435", thresholds: []int64{3000}, usage: sc2, frequency: forever, percentage: true, limited: true, paid: 1750, remaining: -1},
		{source: f + "446", thresholds: []int64{3000}, usage: []rootUsageEvent{{5, 30}, {15, 10}}, frequency: forever, paid: 1000, remaining: -1},
		{source: f + "574", thresholds: []int64{500, 3000}, usage: sc3, frequency: once, paid: 3000, remaining: -1},
		{source: f + "575", thresholds: []int64{500, 3000}, usage: sc3, frequency: forever, paid: 500, remaining: -1},
		{source: f + "576", thresholds: []int64{500, 3000}, usage: sc3, frequency: recurring, percentage: true, paid: 3500, remaining: 0},
		{source: f + "577", thresholds: []int64{500, 3000}, usage: sc3, frequency: recurring, percentage: true, limited: true, paid: 3500, remaining: 0},
		{source: f + "587", thresholds: []int64{4000}, usage: []rootUsageEvent{{5, 40}}, frequency: recurring, percentage: true, paid: 2000, remaining: 1},
		{source: f + "588", thresholds: []int64{4000}, usage: []rootUsageEvent{{5, 40}}, frequency: recurring, percentage: true, limited: true, paid: 2000, remaining: 1},
		{source: f + "600", thresholds: []int64{4000}, usage: sc4, frequency: once, paid: 2000, refund: 2000, remaining: -1},
		{source: f + "601", thresholds: []int64{4000}, usage: sc4, frequency: forever, paid: 2000, refund: 2000, remaining: -1},
		{source: f + "615", thresholds: []int64{4000}, usage: sc5, frequency: once, paid: 2000, refund: 1000, remaining: -1},
		{source: f + "616", thresholds: []int64{4000}, usage: sc5, frequency: forever, paid: 2000, refund: 1000, remaining: -1},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			start := billingtest.Date(2025, 1, 1)
			s := newScenario(t, start)
			m := s.Metric("u", catalog.AggregationTypeSum, "n")
			p := rootEURPlan(s, 0)
			s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
			for _, a := range tt.thresholds {
				s.UsageThreshold(p, a, false)
			}
			c := rootEURCustomer(s)
			opt := func(x *coupon.Coupon) {
				x.Frequency, x.Expiration = tt.frequency, coupon.ExpirationNoExpiration
				if tt.frequency == recurring {
					two := 2
					x.FrequencyDuration = &two
				}
				if tt.limited {
					x.LimitedBillableMetricIDs = []core.BillableMetricID{m.ID}
				}
			}
			var cp *coupon.Coupon
			if tt.percentage {
				cp = s.PercentageCoupon("50", opt)
			} else {
				cp = s.FixedAmountCoupon(2000, func(x *coupon.Coupon) { x.Currency = rootEUR; opt(x) })
			}
			applied := s.ApplyCoupon(cp, c)
			sub := s.rootSubscribe(c, p)
			for _, e := range tt.usage {
				s.travelTo(start.AddDate(0, 0, e.days))
				s.ingest(sub, "u", "n", strconv.Itoa(e.n))
			}
			s.travelTo(start.AddDate(0, 1, 0))
			s.performBilling()
			var paid, refund int64
			for _, inv := range s.Invoices(c.ID) {
				paid += inv.TotalAmount
				for _, cn := range s.CreditNotes(inv.ID) {
					refund += cn.CreditAmount
				}
			}
			if paid != tt.paid || refund != tt.refund {
				t.Errorf("paid/refund = %d/%d, want = %d/%d", paid, refund, tt.paid, tt.refund)
			}
			if tt.remaining >= 0 {
				a := s.ReloadAppliedCoupon(applied.ID)
				if a.FrequencyDurationRemaining == nil || *a.FrequencyDurationRemaining != tt.remaining {
					t.Errorf("frequency duration remaining = %v, want = %d", a.FrequencyDurationRemaining, tt.remaining)
				}
			}
		})
	}
}

// rootPBSetup creates the plan, coupon and subscription of the explicit
// progressive billing cases of coupons_breakdown_spec.rb: a monthly plan
// with a standard charge on u, one threshold and a forever $20 coupon.
func rootPBSetup(t *testing.T, threshold int64, cur money.Currency) (*scenario, *customer.Customer, *catalog.Plan, *catalog.BillableMetric, *catalog.Charge, *subscription.Subscription) {
	s := newScenario(t, billingtest.Date(2025, 1, 1))
	m := s.Metric("u", catalog.AggregationTypeSum, "n")
	p := s.Plan(period.Monthly, 0, func(p *catalog.Plan) { p.Currency = cur })
	ch := s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	s.UsageThreshold(p, threshold, false)
	c := s.Customer(func(c *customer.Customer) { c.Currency = cur })
	cp := s.FixedAmountCoupon(2000, func(x *coupon.Coupon) {
		x.Currency, x.Frequency, x.Expiration = cur, coupon.FrequencyForever, coupon.ExpirationNoExpiration
	})
	s.ApplyCoupon(cp, c)
	return s, c, p, m, ch, s.rootSubscribe(c, p)
}

// rootRecreateCharge replaces the plan's charge on m by a new one on day
// 20 (lago update_plan with no charges, then with one again).
func rootRecreateCharge(s *scenario, p *catalog.Plan, m *catalog.BillableMetric, old *catalog.Charge) *catalog.Charge {
	s.travelTo(billingtest.Date(2025, 1, 21))
	s.tx(func(ctx context.Context, r port.Repos) error {
		return r.Charges.Discard(ctx, s.OrgID(), old.ID, s.Now())
	})
	return s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
}

// rootOfType returns the invoices of a type.
func rootOfType(invs []*invoice.Invoice, ty invoice.Type) []*invoice.Invoice {
	var out []*invoice.Invoice
	for _, inv := range invs {
		if inv.Type == ty {
			out = append(out, inv)
		}
	}
	return out
}

// source: spec/scenarios/coupons_breakdown_spec.rb:448
func TestRootPBCreditNoteAfterChargeRecreated(t *testing.T) {
	usd := money.MustCurrency("USD")
	s, c, p, m, ch, sub := rootPBSetup(t, 3000, usd)
	s.travelTo(billingtest.Date(2025, 1, 6))
	s.ingest(sub, "u", "n", "30")
	s.travelTo(billingtest.Date(2025, 1, 16))
	s.ingest(sub, "u", "n", "10")
	pbs := rootOfType(s.Invoices(c.ID), invoice.TypeProgressiveBilling)
	if len(pbs) != 1 {
		t.Fatalf("progressive invoices = %d, want = 1", len(pbs))
	}
	pb := pbs[0]
	if pb.Currency.Code() != "USD" || pb.FeesAmount != 3000 || pb.CouponsAmount != 2000 || pb.TotalAmount != 1000 {
		t.Errorf("progressive invoice currency/fees/coupons/total = %s/%d/%d/%d, want = USD/3000/2000/1000", pb.Currency.Code(), pb.FeesAmount, pb.CouponsAmount, pb.TotalAmount)
	}
	pbFees := s.Fees(pb.ID)
	replacement := rootRecreateCharge(s, p, m, ch)
	s.travelTo(billingtest.Date(2025, 2, 1))
	s.performBilling()
	invs := rootOfType(s.Invoices(c.ID), invoice.TypeSubscription)
	if len(invs) != 1 {
		t.Fatalf("subscription invoices = %d, want = 1", len(invs))
	}
	inv := invs[0]
	if inv.Status != invoice.StatusFinalized || inv.FeesAmount != 4000 || inv.CouponsAmount != 2000 || inv.ProgressiveBillingCreditAmount != 0 ||
		inv.CreditNotesAmount != 1000 || inv.SubTotalExcludingTaxesAmount != 2000 || inv.TotalAmount != 1000 {
		t.Errorf("invoice status/fees/coupons/pb/cn/excl/total = %s/%d/%d/%d/%d/%d/%d, want = finalized/4000/2000/0/1000/2000/1000",
			inv.Status, inv.FeesAmount, inv.CouponsAmount, inv.ProgressiveBillingCreditAmount, inv.CreditNotesAmount, inv.SubTotalExcludingTaxesAmount, inv.TotalAmount)
	}
	for _, f := range s.Fees(inv.ID) {
		if f.Type == core.FeeTypeCharge && f.ChargeID != replacement.ID {
			t.Errorf("charge fee charge = %s, want = %s", f.ChargeID, replacement.ID)
		}
	}
	cns := s.CreditNotes(pb.ID)
	if len(cns) != 1 {
		t.Fatalf("progressive credit notes = %d, want = 1", len(cns))
	}
	cn := cns[0]
	if cn.CreditAmount != 1000 || cn.CouponsAdjustmentAmount != 2000 || cn.RefundAmount != 0 || cn.BalanceAmount != 0 || cn.CreditStatus != creditnote.CreditStatusConsumed {
		t.Errorf("credit note credit/coupons/refund/balance/status = %d/%d/%d/%d/%s, want = 1000/2000/0/0/consumed",
			cn.CreditAmount, cn.CouponsAdjustmentAmount, cn.RefundAmount, cn.BalanceAmount, cn.CreditStatus)
	}
	if len(cn.Items) != 1 || len(pbFees) != 1 || cn.Items[0].FeeID != pbFees[0].ID || cn.Items[0].Amount != 3000 {
		t.Errorf("credit note items = %+v, want = one of 3000 on the progressive fee", cn.Items)
	}
	var cnCredits []int64
	for _, cr := range s.InvoiceCredits(inv.ID) {
		if cr.CreditNoteID == cn.ID {
			cnCredits = append(cnCredits, cr.Amount)
		}
	}
	if !slices.Equal(cnCredits, []int64{1000}) {
		t.Errorf("credit note credits = %v, want = [1000]", cnCredits)
	}
	var paid int64
	for _, x := range s.Invoices(c.ID) {
		paid += x.TotalAmount
	}
	if paid != 2000 {
		t.Errorf("paid = %d, want = 2000", paid)
	}
}

// source: spec/scenarios/coupons_breakdown_spec.rb:504
func TestRootPBFullyDiscountedDraftFinalized(t *testing.T) {
	s, c, p, m, ch, sub := rootPBSetup(t, 2000, rootEUR)
	s.travelTo(billingtest.Date(2025, 1, 6))
	s.ingest(sub, "u", "n", "20")
	s.travelTo(billingtest.Date(2025, 1, 16))
	s.ingest(sub, "u", "n", "10")
	pbs := rootOfType(s.Invoices(c.ID), invoice.TypeProgressiveBilling)
	if len(pbs) != 1 {
		t.Fatalf("progressive invoices = %d, want = 1", len(pbs))
	}
	pb := pbs[0]
	if pb.FeesAmount != 2000 || pb.CouponsAmount != 2000 || pb.TotalAmount != 0 {
		t.Errorf("progressive fees/coupons/total = %d/%d/%d, want = 2000/2000/0", pb.FeesAmount, pb.CouponsAmount, pb.TotalAmount)
	}
	rootRecreateCharge(s, p, m, ch)
	s.UpdateCustomer(s.ReloadCustomer(c.ID), rootGrace(2))
	s.travelTo(billingtest.Date(2025, 2, 1))
	s.performBilling()
	invs := rootOfType(s.Invoices(c.ID), invoice.TypeSubscription)
	if len(invs) != 1 || invs[0].Status != invoice.StatusDraft {
		t.Fatalf("subscription invoices = %d (want 1 draft)", len(invs))
	}
	s.travelTo(billingtest.Date(2025, 2, 3))
	inv := s.rootFinalize(invs[0])
	if inv.Status != invoice.StatusFinalized || inv.FeesAmount != 3000 || inv.CouponsAmount != 2000 || inv.ProgressiveBillingCreditAmount != 0 ||
		inv.CreditNotesAmount != 0 || inv.TotalAmount != 1000 {
		t.Errorf("invoice status/fees/coupons/pb/cn/total = %s/%d/%d/%d/%d/%d, want = finalized/3000/2000/0/0/1000",
			inv.Status, inv.FeesAmount, inv.CouponsAmount, inv.ProgressiveBillingCreditAmount, inv.CreditNotesAmount, inv.TotalAmount)
	}
	if n := len(s.CreditNotes(pb.ID)); n != 0 {
		t.Errorf("progressive credit notes = %d, want = 0", n)
	}
}

// source: spec/scenarios/coupons_breakdown_spec.rb:528
func TestRootPBRemainingGrossFeeReversed(t *testing.T) {
	s, c, p, m, ch, sub := rootPBSetup(t, 10_000, rootEUR)
	s.travelTo(billingtest.Date(2025, 1, 6))
	s.ingest(sub, "u", "n", "100")
	s.travelTo(billingtest.Date(2025, 1, 16))
	s.ingest(sub, "u", "n", "10")
	pbs := rootOfType(s.Invoices(c.ID), invoice.TypeProgressiveBilling)
	if len(pbs) != 1 {
		t.Fatalf("progressive invoices = %d, want = 1", len(pbs))
	}
	pb := pbs[0]
	pbFee := s.Fees(pb.ID)[0]
	s.travelTo(billingtest.Date(2025, 1, 17))
	existing, err := s.E.CreditNotes.Create(s.Ctx, creditnotes.CreateInput{
		OrganizationID: s.OrgID(), InvoiceID: pb.ID, Reason: creditnote.ReasonOther, CreditAmount: 2000,
		Items: []creditnotes.ItemInput{{FeeID: pbFee.ID, Amount: money.NewFromInt(2500)}},
	})
	s.must(err)
	s.drain()
	if existing.CreditAmount != 2000 || existing.CouponsAdjustmentAmount != 500 {
		t.Errorf("existing credit note credit/coupons = %d/%d, want = 2000/500", existing.CreditAmount, existing.CouponsAdjustmentAmount)
	}
	rootRecreateCharge(s, p, m, ch)
	s.travelTo(billingtest.Date(2025, 2, 1))
	s.performBilling()
	invs := rootOfType(s.Invoices(c.ID), invoice.TypeSubscription)
	if len(invs) != 1 {
		t.Fatalf("subscription invoices = %d, want = 1", len(invs))
	}
	inv := invs[0]
	var fresh *creditnote.CreditNote
	var credited int64
	for _, cn := range s.CreditNotes(pb.ID) {
		for _, it := range cn.Items {
			if it.FeeID == pbFee.ID {
				credited += it.Amount
			}
		}
		if cn.ID != existing.ID {
			fresh = cn
		}
	}
	if fresh == nil {
		t.Fatalf("no new credit note on the progressive invoice")
	}
	if fresh.CreditAmount != 6000 || fresh.CouponsAdjustmentAmount != 1500 || fresh.BalanceAmount != 0 || fresh.CreditStatus != creditnote.CreditStatusConsumed {
		t.Errorf("new credit note credit/coupons/balance/status = %d/%d/%d/%s, want = 6000/1500/0/consumed",
			fresh.CreditAmount, fresh.CouponsAdjustmentAmount, fresh.BalanceAmount, fresh.CreditStatus)
	}
	if len(fresh.Items) != 1 || fresh.Items[0].FeeID != pbFee.ID || fresh.Items[0].Amount != 7500 {
		t.Errorf("new credit note items = %+v, want = one of 7500 on the progressive fee", fresh.Items)
	}
	if inv.Status != invoice.StatusFinalized || inv.FeesAmount != 11_000 || inv.CouponsAmount != 2000 || inv.ProgressiveBillingCreditAmount != 0 ||
		inv.CreditNotesAmount != 8000 || inv.TotalAmount != 1000 {
		t.Errorf("invoice status/fees/coupons/pb/cn/total = %s/%d/%d/%d/%d/%d, want = finalized/11000/2000/0/8000/1000",
			inv.Status, inv.FeesAmount, inv.CouponsAmount, inv.ProgressiveBillingCreditAmount, inv.CreditNotesAmount, inv.TotalAmount)
	}
	if e := s.ReloadCreditNote(existing.ID); e.BalanceAmount != 0 || e.CreditStatus != creditnote.CreditStatusConsumed {
		t.Errorf("existing credit note balance/status = %d/%s, want = 0/consumed", e.BalanceAmount, e.CreditStatus)
	}
	if credited != 10_000 {
		t.Errorf("credited on the progressive fee = %d, want = 10000", credited)
	}
}

// rootAdvanceStep is an event of a pay-in-advance scenario and the fee it
// creates.
type rootAdvanceStep struct {
	at     time.Time
	props  []string
	units  string
	events int64
	amount int64
	// filtered expects the fee on the charge's first filter.
	filtered bool
}

// rootChargeFees returns the fees of a charge in creation order.
func (s *scenario) rootChargeFees(id core.ChargeID) []*invoice.Fee {
	s.T.Helper()
	var out []*invoice.Fee
	s.tx(func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.Fees.ListByCharge(ctx, s.OrgID(), id)
		return err
	})
	return out
}

// TestRootPayInAdvanceCharges ports spec/scenarios/pay_in_advance_charges_spec.rb:
// a monthly plan subscribed on 24 Jan 2023 gets a pay-in-advance charge
// whose events each create a fee; a later event stored beforehand never
// counts.
func TestRootPayInAdvanceCharges(t *testing.T) {
	feb := func(d int) time.Time { return billingtest.Date(2023, 2, d) }
	type metricDef struct {
		agg     catalog.AggregationType
		field   string
		filters []catalog.MetricFilter
	}
	tests := []struct {
		source      string
		metric      metricDef
		model       catalog.ChargeModel
		props       string
		invoiceable bool
		filters     []catalog.ChargeFilter
		later       *rootAdvanceStep
		steps       []rootAdvanceStep
	}{
		{
			source: "spec/scenarios/pay_in_advance_charges_spec.rb:15", metric: metricDef{agg: catalog.AggregationTypeCount},
			model: catalog.ChargeModelStandard, props: `{"amount":"10"}`, later: &rootAdvanceStep{at: feb(19)},
			steps: []rootAdvanceStep{{at: feb(15), units: "1", events: 1, amount: 1000}, {at: feb(17), units: "1", events: 1, amount: 1000}},
		},
		{
			source: "spec/scenarios/pay_in_advance_charges_spec.rb:164", metric: metricDef{agg: catalog.AggregationTypeUniqueCount, field: "unique_id"},
			model: catalog.ChargeModelStandard, props: `{"amount":"12"}`, later: &rootAdvanceStep{at: feb(25), props: []string{"unique_id", "id_1"}},
			steps: []rootAdvanceStep{
				{at: feb(15), props: []string{"unique_id", "id_1"}, units: "1", events: 1, amount: 1200},
				{at: feb(16), props: []string{"unique_id", "id_1", "operation_type", "remove"}, units: "0", events: 1, amount: 0},
				{at: feb(17), props: []string{"unique_id", "id_1"}, units: "1", events: 1, amount: 0},
				{at: feb(18), props: []string{"unique_id", "id_2"}, units: "1", events: 1, amount: 1200},
				{at: feb(19), props: []string{"unique_id", "id_2"}, units: "0", events: 1, amount: 0},
				{at: feb(20), props: []string{"unique_id", "id_3"}, units: "1", events: 1, amount: 1200},
				{at: feb(21), props: []string{"unique_id", "id_3", "operation_type", "remove"}, units: "0", events: 1, amount: 0},
			},
		},
		{
			source: "spec/scenarios/pay_in_advance_charges_spec.rb:374", metric: metricDef{agg: catalog.AggregationTypeSum, field: "amount"},
			model: catalog.ChargeModelStandard, props: `{"amount":"1"}`, invoiceable: true, later: &rootAdvanceStep{at: feb(19), props: []string{"amount", "5000"}},
			steps: []rootAdvanceStep{
				{at: feb(15), props: []string{"amount", "10"}, units: "10", events: 1, amount: 1000},
				{at: feb(17), props: []string{"amount", "-4"}, units: "0", events: 1, amount: 0},
				{at: feb(18), props: []string{"amount", "8"}, units: "4", events: 1, amount: 400},
			},
		},
		{
			source: "spec/scenarios/pay_in_advance_charges_spec.rb:472",
			metric: metricDef{agg: catalog.AggregationTypeSum, field: "amount", filters: []catalog.MetricFilter{{Key: "region", Values: []string{"europe"}}}},
			model:  catalog.ChargeModelStandard, props: `{"amount":"0"}`, invoiceable: true,
			filters: []catalog.ChargeFilter{billingtest.ChargeFilter(map[string][]string{"region": {"europe"}}, `{"amount":"20"}`)},
			later:   &rootAdvanceStep{at: feb(19), props: []string{"amount", "50", "region", "europe"}},
			steps:   []rootAdvanceStep{{at: feb(15), props: []string{"amount", "10", "region", "europe"}, units: "10", events: 1, amount: 20_000, filtered: true}},
		},
		{
			source: "spec/scenarios/pay_in_advance_charges_spec.rb:542",
			metric: metricDef{agg: catalog.AggregationTypeSum, field: "amount", filters: []catalog.MetricFilter{
				{Key: "cloud", Values: []string{"AWS"}}, {Key: "region", Values: []string{"europe"}},
			}},
			model: catalog.ChargeModelStandard, props: `{"amount":"10"}`, invoiceable: true,
			filters: []catalog.ChargeFilter{billingtest.ChargeFilter(map[string][]string{"region": {"europe"}, "cloud": {"AWS"}}, `{"amount":"20"}`)},
			later:   &rootAdvanceStep{at: feb(15), props: []string{"amount", "50", "region", "africa", "cloud", "AWS"}},
			steps:   []rootAdvanceStep{{at: feb(15), props: []string{"amount", "10", "region", "africa", "cloud", "AWS"}, units: "10", events: 1, amount: 10_000}},
		},
		{
			source: "spec/scenarios/pay_in_advance_charges_spec.rb:609", metric: metricDef{agg: catalog.AggregationTypeSum, field: "amount"},
			model: catalog.ChargeModelPackage, props: `{"amount":"100","free_units":3,"package_size":2}`, later: &rootAdvanceStep{at: feb(19), props: []string{"amount", "5"}},
			steps: []rootAdvanceStep{
				{at: feb(15), props: []string{"amount", "3"}, units: "3", events: 1, amount: 0},
				{at: feb(17), props: []string{"amount", "1"}, units: "1", events: 1, amount: 10_000},
				{at: feb(18), props: []string{"amount", "2"}, units: "2", events: 1, amount: 10_000},
			},
		},
		{
			source: "spec/scenarios/pay_in_advance_charges_spec.rb:706", metric: metricDef{agg: catalog.AggregationTypeSum, field: "amount"},
			model: catalog.ChargeModelGraduated,
			props: `{"graduated_ranges":[{"from_value":0,"to_value":5,"per_unit_amount":"0.02","flat_amount":"0.01"},{"from_value":6,"to_value":null,"per_unit_amount":"0.01","flat_amount":"0.01"}]}`,
			later: &rootAdvanceStep{at: feb(19), props: []string{"amount", "5"}},
			steps: []rootAdvanceStep{
				{at: feb(15), props: []string{"amount", "3"}, units: "3", events: 1, amount: 7},
				{at: feb(17), props: []string{"amount", "1"}, units: "1", events: 1, amount: 2},
				{at: feb(18), props: []string{"amount", "2"}, units: "2", events: 1, amount: 4},
			},
		},
		{
			source: "spec/scenarios/pay_in_advance_charges_spec.rb:820", metric: metricDef{agg: catalog.AggregationTypeSum, field: "amount"},
			model: catalog.ChargeModelPercentage, props: `{"rate":"5","fixed_amount":"1","free_units_per_events":2}`,
			later: &rootAdvanceStep{at: feb(17), props: []string{"amount", "5"}},
			steps: []rootAdvanceStep{
				{at: feb(14), props: []string{"amount", "8"}, units: "8", events: 1, amount: 0},
				{at: feb(15), props: []string{"amount", "5"}, units: "5", events: 1, amount: 0},
				{at: feb(16), props: []string{"amount", "3"}, units: "3", events: 1, amount: 115},
			},
		},
		{
			source: "spec/scenarios/pay_in_advance_charges_spec.rb:944", metric: metricDef{agg: catalog.AggregationTypeSum, field: "amount"},
			model: catalog.ChargeModelPercentage, props: `{"rate":"5","fixed_amount":"1","free_units_per_total_aggregation":"120.0"}`,
			later: &rootAdvanceStep{at: feb(19), props: []string{"amount", "50"}},
			steps: []rootAdvanceStep{
				{at: feb(14), props: []string{"amount", "100"}, units: "100", events: 1, amount: 0},
				{at: feb(15), props: []string{"amount", "10"}, units: "10", events: 1, amount: 0},
				{at: feb(16), props: []string{"amount", "20"}, units: "20", events: 1, amount: 150},
				{at: feb(17), props: []string{"amount", "20"}, units: "20", events: 1, amount: 200},
			},
		},
		{
			source: "spec/scenarios/pay_in_advance_charges_spec.rb:1107", metric: metricDef{agg: catalog.AggregationTypeSum, field: "amount"},
			model: catalog.ChargeModelPercentage, props: `{"rate":"1","fixed_amount":"0.5","per_transaction_max_amount":"2","per_transaction_min_amount":"1.75"}`,
			later: &rootAdvanceStep{at: feb(20), props: []string{"amount", "333"}},
			steps: []rootAdvanceStep{
				{at: feb(14), props: []string{"amount", "100"}, units: "100", events: 1, amount: 175},
				{at: feb(15), props: []string{"amount", "1000"}, units: "1000", events: 1, amount: 200},
				{at: feb(16), props: []string{"amount", "10000"}, units: "10000", events: 1, amount: 200},
			},
		},
		{
			source: "spec/scenarios/pay_in_advance_charges_spec.rb:1228", metric: metricDef{agg: catalog.AggregationTypeSum, field: "amount"},
			model: catalog.ChargeModelPercentage, props: `{"rate":"5","fixed_amount":"1","free_units_per_total_aggregation":"3.0"}`,
			steps: []rootAdvanceStep{
				{at: feb(15), props: []string{"amount", "5"}, units: "5", events: 1, amount: 110},
				{at: feb(17), props: []string{"amount", "1"}, units: "1", events: 1, amount: 105},
			},
		},
		{
			source: "spec/scenarios/pay_in_advance_charges_spec.rb:1385", metric: metricDef{agg: catalog.AggregationTypeCount},
			model: catalog.ChargeModelPercentage, props: `{"rate":"1","fixed_amount":"1","free_units_per_events":1}`,
			later: &rootAdvanceStep{at: feb(15).Add(time.Second), props: []string{"amount", "333"}},
			steps: []rootAdvanceStep{{at: feb(15), props: []string{"amount", "5"}, units: "1", events: 1, amount: 0}},
		},
		{
			source: "spec/scenarios/pay_in_advance_charges_spec.rb:1451", metric: metricDef{agg: catalog.AggregationTypeSum, field: "amount"},
			model: catalog.ChargeModelGraduatedPercentage,
			props: `{"graduated_percentage_ranges":[{"rate":"20","to_value":100,"from_value":0,"flat_amount":"10"},{"rate":"30","to_value":200,"from_value":101,"flat_amount":"7"},{"rate":"40","to_value":null,"from_value":201,"flat_amount":"5"}]}`,
			later: &rootAdvanceStep{at: feb(20), props: []string{"amount", "333"}},
			steps: []rootAdvanceStep{
				{at: feb(15), props: []string{"amount", "50"}, units: "50", events: 1, amount: 2000},
				{at: feb(17), props: []string{"amount", "100"}, units: "100", events: 1, amount: 3200},
				{at: feb(19), props: []string{"amount", "300"}, units: "300", events: 1, amount: 12_000},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			s := newScenario(t, billingtest.Date(2023, 1, 24))
			c := s.Customer()
			p := s.Plan(period.Monthly, 1000)
			sub := s.rootSubscribe(c, p)
			m := s.Metric("m", tt.metric.agg, tt.metric.field, func(m *catalog.BillableMetric) {
				for _, f := range tt.metric.filters {
					f.ID = core.NewID[core.BillableMetricFilterID](s.IDs)
					m.Filters = append(m.Filters, f)
				}
			})
			ch := s.Charge(p, m, tt.model, tt.props, func(ch *catalog.Charge) {
				ch.PayInAdvance, ch.Invoiceable, ch.Filters = true, tt.invoiceable, tt.filters
			})
			if tt.later != nil {
				s.Event(sub, "m", tt.later.at, tt.later.props...)
			}
			for i, st := range tt.steps {
				s.travelTo(st.at)
				s.ingest(sub, "m", st.props...)
				fees := s.rootChargeFees(ch.ID)
				if len(fees) != i+1 {
					t.Fatalf("step %d: fees = %d, want = %d", i, len(fees), i+1)
				}
				f := fees[i]
				if !f.InvoiceID.IsZero() && !tt.invoiceable {
					t.Errorf("step %d: fee invoice = %s, want = none", i, f.InvoiceID)
				}
				if !f.PayInAdvance || f.Units.String() != st.units || f.EventsCount != st.events || f.Amount != st.amount {
					t.Errorf("step %d: fee in advance/units/events/amount = %t/%s/%d/%d, want = true/%s/%d/%d",
						i, f.PayInAdvance, f.Units, f.EventsCount, f.Amount, st.units, st.events, st.amount)
				}
				var wantFilter core.ChargeFilterID
				if st.filtered {
					wantFilter = s.rootReloadCharge(ch.ID).Filters[0].ID
				}
				if f.ChargeFilterID != wantFilter {
					t.Errorf("step %d: fee filter = %s, want = %s", i, f.ChargeFilterID, wantFilter)
				}
			}
			if strings.HasSuffix(tt.source, ":1107") {
				// source: spec/scenarios/pay_in_advance_charges_spec.rb:1217
				u := s.rootUsage(sub, usage.Options{})
				if len(u.Fees) != 1 || u.Fees[0].Units.String() != "11433" || u.Fees[0].TotalAggregatedUnits.String() != "11433" || u.TotalAmount != 775 {
					t.Errorf("usage = %d fees, total %d, want = 1 fee of 11433 units, total 775", len(u.Fees), u.TotalAmount)
				}
			}
		})
	}
}

// rootReloadCharge reloads a charge with its filters.
func (s *scenario) rootReloadCharge(id core.ChargeID) *catalog.Charge {
	s.T.Helper()
	var out *catalog.Charge
	s.tx(func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.Charges.Get(ctx, s.OrgID(), id)
		return err
	})
	return out
}

// source: spec/scenarios/pay_in_advance_charges_spec.rb:96
func TestRootPayInAdvanceSameSecond(t *testing.T) {
	s := newScenario(t, billingtest.Date(2023, 1, 24))
	c := s.Customer()
	p := s.Plan(period.Monthly, 1000)
	sub := s.rootSubscribe(c, p)
	m := s.Metric("m", catalog.AggregationTypeCount, "")
	ch := s.Charge(p, m, catalog.ChargeModelGraduated,
		`{"graduated_ranges":[{"from_value":0,"to_value":2,"per_unit_amount":"0","flat_amount":"0"},{"from_value":3,"to_value":null,"per_unit_amount":"2","flat_amount":"0"}]}`,
		func(ch *catalog.Charge) { ch.PayInAdvance, ch.Invoiceable = true, false })
	feb15 := billingtest.Date(2023, 2, 15)
	s.travelTo(feb15)
	// The batch is stored before any fee is computed.
	evs := []string{s.Event(sub, "m", feb15).TransactionID, s.Event(sub, "m", feb15).TransactionID, s.Event(sub, "m", feb15).TransactionID}
	for _, id := range evs {
		_, err := s.E.Invoices.InAdvanceCharge(s.Ctx, s.OrgID(), kernel.InAdvanceChargeArgs{SubscriptionID: sub.ID, TransactionID: id, Timestamp: feb15, Code: "m"})
		s.must(err)
	}
	s.drain()
	fees := s.rootChargeFees(ch.ID)
	var amounts []int64
	ids := map[string]bool{}
	for _, f := range fees {
		amounts = append(amounts, f.Amount)
		ids[f.PayInAdvanceEventTransactionID] = true
		if f.Units.String() != "1" {
			t.Errorf("fee units = %s, want = 1", f.Units)
		}
	}
	slices.Sort(amounts)
	if !slices.Equal(amounts, []int64{0, 0, 200}) || len(ids) != 3 {
		t.Errorf("amounts = %v over %d transactions, want = [0 0 200] over 3", amounts, len(ids))
	}
}

// source: spec/scenarios/pay_in_advance_charges_spec.rb:1306
func TestRootPayInAdvanceDynamic(t *testing.T) {
	s := newScenario(t, billingtest.Date(2024, 9, 26))
	c := s.Customer()
	p := s.Plan(period.Monthly, 1000)
	sub := s.rootSubscribe(c, p)
	m := s.Metric("m", catalog.AggregationTypeSum, "amount")
	ch := s.Charge(p, m, catalog.ChargeModelDynamic, `{}`, func(ch *catalog.Charge) { ch.PayInAdvance, ch.Invoiceable = true, false })
	// send stores an event carrying a precise amount, which the harness
	// events cannot.
	n := 0
	send := func(at time.Time, amount, precise string) metering.Event {
		n++
		d := money.MustParse(precise)
		e := metering.Event{
			OrganizationID: s.OrgID().String(), SubscriptionID: sub.ExternalID, Code: "m",
			TransactionID: fmt.Sprintf("tr_dynamic_%d", n), Timestamp: at, IngestedAt: s.Now(),
			Properties: metering.Properties{"amount": amount}, PreciseTotalAmountCents: &d,
		}
		s.Events.Add(e)
		return e
	}
	send(billingtest.Date(2024, 9, 30, 11), "9876", "9875.6823759")
	steps := []struct {
		at              time.Time
		amount, precise string
		want            int64
	}{
		{billingtest.Date(2024, 9, 28), "5", "1234.56", 1235},
		{billingtest.Date(2024, 9, 30), "1", "2.1", 2},
	}
	for i, st := range steps {
		s.travelTo(st.at)
		e := send(st.at, st.amount, st.precise)
		_, err := s.E.Invoices.InAdvanceCharge(s.Ctx, s.OrgID(), kernel.InAdvanceChargeArgs{
			SubscriptionID: sub.ID, TransactionID: e.TransactionID, Timestamp: e.Timestamp, Code: "m",
			Properties: map[string]string(e.Properties), PreciseTotalAmountCents: e.PreciseTotalAmountCents,
		})
		s.must(err)
		s.drain()
		fees := s.rootChargeFees(ch.ID)
		if len(fees) != i+1 {
			t.Fatalf("step %d: fees = %d, want = %d", i, len(fees), i+1)
		}
		if f := fees[i]; !f.InvoiceID.IsZero() || f.Units.String() != st.amount || f.EventsCount != 1 || f.Amount != st.want {
			t.Errorf("step %d: fee invoice/units/events/amount = %s/%s/%d/%d, want = none/%s/1/%d", i, f.InvoiceID, f.Units, f.EventsCount, f.Amount, st.amount, st.want)
		}
	}
}
