package fees_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// chargeFixture is the setup of spec/services/fees/charge_service_spec.rb:
// a monthly subscription started on 2022-03-15, billed for the rest of
// March.
type chargeFixture struct {
	*fixture
	sub  *subscription.Subscription
	plan *catalog.Plan
	b    period.Boundaries
}

func newChargeFixture(t *testing.T) *chargeFixture {
	t.Helper()
	f := newFixture(t, date(2022, time.March, 15))
	plan := f.Plan(period.Monthly, 100)
	s := f.Subscription(f.Customer(), plan, billingtest.SubscribedAt(date(2022, time.March, 15)))
	p := span(date(2022, time.March, 15), date(2022, time.April, 1))
	b := period.Boundaries{
		Subscription: p, Charges: p, ChargesBilled: true, FixedCharges: p, FixedChargesBilled: true,
		ChargesDays: 31, FixedChargesDays: 31,
	}
	f.Clock.Set(date(2022, time.April, 1))
	return &chargeFixture{fixture: f, sub: s, plan: plan, b: b}
}

// metric creates a metric with the region and country filters of the
// spec.
func (f *chargeFixture) metric(agg catalog.AggregationType, field string) *catalog.BillableMetric {
	return f.Metric("metric_"+string(agg), agg, field, func(m *catalog.BillableMetric) {
		m.Filters = []catalog.MetricFilter{
			{ID: core.NewID[core.BillableMetricFilterID](f.IDs), Key: "region", Values: []string{"europe", "usa"}},
			{ID: core.NewID[core.BillableMetricFilterID](f.IDs), Key: "country", Values: []string{"france"}},
		}
	})
}

// fees computes the fees of c on a new invoice of the subscription.
func (f *chargeFixture) fees(c *catalog.Charge, opts fees.Options, invOpts ...invoiceOpt) (fees.Scope, *fees.ChargeResult) {
	f.T.Helper()
	sc := f.scope(f.sub, f.b, date(2022, time.April, 1), invOpts...)
	return sc, f.run(sc, c, opts)
}

// run computes the fees of c in sc.
func (f *chargeFixture) run(sc fees.Scope, c *catalog.Charge, opts fees.Options) *fees.ChargeResult {
	f.T.Helper()
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*fees.ChargeResult, error) {
		return f.svc.ChargeFees(ctx, r, sc, c, opts)
	})
}

// pricingUnit prices c in a new pricing unit at the conversion rate.
func (f *chargeFixture) pricingUnit(rate string) func(*catalog.Charge) {
	pu := &catalog.PricingUnit{
		ID: core.NewID[core.PricingUnitID](f.IDs), OrganizationID: f.OrgID(), Code: "credits", Name: "Credits", ShortName: "CR", CreatedAt: f.Now(),
	}
	f.Do(func(ctx context.Context, r port.Repos) error { return r.PricingUnits.Create(ctx, pu) })
	return func(c *catalog.Charge) {
		c.AppliedPricingUnit = &catalog.AppliedPricingUnit{PricingUnitID: pu.ID, ConversionRate: dec(rate)}
	}
}

// wantFee is the expected part of a fee.
type wantFee struct {
	amount      int64
	precise     string
	units       string
	unit        int64
	preciseUnit string
	// events is the events count; -1 skips the check.
	events int64
}

func checkFee(t *testing.T, got *invoice.Fee, want wantFee) {
	t.Helper()
	checkAmount(t, got, want.amount, want.precise)
	if !eqDec(got.Units, dec(want.units)) {
		t.Errorf("got units = %s, want = %s", got.Units, want.units)
	}
	if got.UnitAmount != want.unit {
		t.Errorf("got unit amount = %d, want = %d", got.UnitAmount, want.unit)
	}
	if want.preciseUnit != "" && !eqDec(got.PreciseUnitAmount.Round(10, money.HalfUp), dec(want.preciseUnit).Round(10, money.HalfUp)) {
		t.Errorf("got precise unit amount = %s, want = %s", got.PreciseUnitAmount, want.preciseUnit)
	}
	if want.events >= 0 && got.EventsCount != want.events {
		t.Errorf("got events count = %d, want = %d", got.EventsCount, want.events)
	}
}

// feeCount checks the number of fees.
func feeCount(t *testing.T, res *fees.ChargeResult, want int) {
	t.Helper()
	got := 0
	if res != nil {
		got = len(res.Fees)
	}
	if got != want {
		t.Fatalf("got %d fees, want = %d", got, want)
	}
}

// byGroup returns the fee grouped by key = value.
func byGroup(t *testing.T, res *fees.ChargeResult, key, value string) *invoice.Fee {
	t.Helper()
	for _, f := range res.Fees {
		if f.GroupedBy[key] == value {
			return f
		}
	}
	t.Fatalf("got no fee grouped by %s = %s, want one", key, value)
	return nil
}

// byFilter returns the fee of a charge filter (zero for the default
// bucket), true-up fees left out.
func byFilter(t *testing.T, res *fees.ChargeResult, id core.ChargeFilterID) *invoice.Fee {
	t.Helper()
	for _, f := range res.Fees {
		if f.ChargeFilterID == id && f.TrueUpParentFeeID.IsZero() {
			return f
		}
	}
	t.Fatalf("got no fee of filter %s, want one", id)
	return nil
}

func TestChargeFeesStandard(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		agg    catalog.AggregationType
		field  string
		events int
		// rate prices the charge in a pricing unit.
		rate string
		want wantFee
	}{
		// source: spec/services/fees/charge_service_spec.rb:193
		{name: "count", agg: catalog.AggregationTypeCount, events: 1, want: wantFee{2000, "2000", "1", 2000, "20", 1}},
		// source: spec/services/fees/charge_service_spec.rb:1152
		{name: "sum", agg: catalog.AggregationTypeSum, field: "foo_bar", events: 1, want: wantFee{2000, "2000", "1", 2000, "20", -1}},
		// source: spec/services/fees/charge_service_spec.rb:1152
		{name: "max", agg: catalog.AggregationTypeMax, field: "foo_bar", events: 1, want: wantFee{2000, "2000", "1", 2000, "20", -1}},
		// source: spec/services/fees/charge_service_spec.rb:1152
		{name: "unique count", agg: catalog.AggregationTypeUniqueCount, field: "foo_bar", events: 1, want: wantFee{2000, "2000", "1", 2000, "20", -1}},
		// source: spec/services/fees/charge_service_spec.rb:1152
		{name: "latest", agg: catalog.AggregationTypeLatest, field: "foo_bar", events: 1, want: wantFee{2000, "2000", "1", 2000, "20", -1}},
		// source: spec/services/fees/charge_service_spec.rb:1184
		{name: "count in a pricing unit", agg: catalog.AggregationTypeCount, events: 1, rate: "0.25", want: wantFee{500, "500", "1", 500, "5", 1}},
		// source: spec/services/fees/charge_service_spec.rb:1184
		{name: "sum in a pricing unit", agg: catalog.AggregationTypeSum, field: "foo_bar", events: 1, rate: "0.25", want: wantFee{500, "500", "1", 500, "5", -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newChargeFixture(t)
			m := f.Metric("metric", tc.agg, tc.field)
			var opts []func(*catalog.Charge)
			if tc.rate != "" {
				opts = append(opts, f.pricingUnit(tc.rate))
			}
			c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"20"}`, opts...)
			for range tc.events {
				f.Event(f.sub, m.Code, date(2022, time.March, 29, 23, 59), "foo_bar", "1")
			}
			_, res := f.fees(c, fees.Options{})
			feeCount(t, res, 1)
			got := res.Fees[0]
			checkFee(t, got, tc.want)
			if got.ChargeID != c.ID || got.Type != core.FeeTypeCharge || got.PaymentStatus != invoice.FeePaymentStatusPending {
				t.Errorf("got fee = %+v, want a pending fee of the charge", got)
			}
			if tc.rate == "" {
				if got.PricingUnitUsage != nil {
					t.Errorf("got pricing unit usage = %+v, want nil", got.PricingUnitUsage)
				}
				return
			}
			// source: spec/services/fees/charge_service_spec.rb:1201
			pu := got.PricingUnitUsage
			if pu == nil || pu.Amount.Cmp(dec("2000")) != 0 || pu.PreciseAmount.Cmp(dec("2000")) != 0 || pu.UnitAmount.Cmp(dec("2000")) != 0 {
				t.Errorf("got pricing unit usage = %+v, want amounts 2000", pu)
			}
		})
	}
}

// source: spec/services/fees/charge_service_spec.rb:174
func TestChargeFeesWithoutEvents(t *testing.T) {
	t.Parallel()
	f := newChargeFixture(t)
	m := f.Metric("metric", catalog.AggregationTypeCount, "")
	c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"20"}`)
	_, res := f.fees(c, fees.Options{})
	feeCount(t, res, 0)
}

// source: spec/services/fees/charge_service_spec.rb:214
func TestChargeFeesProperties(t *testing.T) {
	t.Parallel()
	f := newChargeFixture(t)
	m := f.Metric("metric", catalog.AggregationTypeCount, "")
	c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"20"}`)
	f.Event(f.sub, m.Code, date(2022, time.March, 29))
	_, res := f.fees(c, fees.Options{})
	feeCount(t, res, 1)
	p := res.Fees[0].Properties
	if !p.ChargesFrom.Equal(date(2022, time.March, 15)) || !p.ChargesTo.Equal(date(2022, time.April, 1)) || p.ChargesDurationDays != 31 {
		t.Errorf("got properties = %+v, want charges [2022-03-15, 2022-04-01) over 31 days", p)
	}
}

// source: spec/services/fees/charge_service_spec.rb:1052
func TestChargeFeesGraduated(t *testing.T) {
	t.Parallel()
	f := newChargeFixture(t)
	m := f.Metric("metric", catalog.AggregationTypeCount, "")
	c := f.Charge(f.plan, m, catalog.ChargeModelGraduated, `{"graduated_ranges":[{"from_value":0,"to_value":null,"per_unit_amount":"0.01","flat_amount":"0.01"}]}`)
	for range 4 {
		f.Event(f.sub, m.Code, date(2022, time.March, 16))
	}
	_, res := f.fees(c, fees.Options{})
	feeCount(t, res, 1)
	checkFee(t, res.Fees[0], wantFee{5, "5", "4", 1, "0.0125", 4})
}

func TestChargeFeesGrouped(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, events bool) (*chargeFixture, *catalog.Charge) {
		f := newChargeFixture(t)
		m := f.Metric("metric", catalog.AggregationTypeSum, "value")
		c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"20","pricing_group_keys":["cloud"]}`)
		if events {
			f.Event(f.sub, m.Code, date(2022, time.March, 16), "cloud", "aws", "value", "10")
			f.Event(f.sub, m.Code, date(2022, time.March, 16), "cloud", "aws", "value", "5")
			f.Event(f.sub, m.Code, date(2022, time.March, 16), "cloud", "gcp", "value", "10")
		}
		return f, c
	}

	// source: spec/services/fees/charge_service_spec.rb:363
	t.Run("without events", func(t *testing.T) {
		t.Parallel()
		f, c := setup(t, false)
		_, res := f.fees(c, fees.Options{})
		feeCount(t, res, 0)
	})
	// source: spec/services/fees/charge_service_spec.rb:400
	t.Run("with events", func(t *testing.T) {
		t.Parallel()
		f, c := setup(t, true)
		_, res := f.fees(c, fees.Options{})
		feeCount(t, res, 2)
		checkFee(t, byGroup(t, res, "cloud", "aws"), wantFee{30000, "30000", "15", 2000, "20", -1})
		checkFee(t, byGroup(t, res, "cloud", "gcp"), wantFee{20000, "20000", "10", 2000, "20", -1})
	})
	// source: spec/services/fees/charge_service_spec.rb:464
	t.Run("with an adjusted group", func(t *testing.T) {
		t.Parallel()
		f, c := setup(t, true)
		sc := f.scope(f.sub, f.b, date(2022, time.April, 1), draft)
		f.adjust(&invoice.AdjustedFee{
			InvoiceID: sc.Invoice.ID, SubscriptionID: f.sub.ID, ChargeID: c.ID, FeeType: core.FeeTypeCharge,
			Mode: invoice.AdjustmentModeUnits, Units: dec("3"), GroupedBy: map[string]string{"cloud": "aws"},
			ChargesFrom: sc.Boundaries.Charges.From, ChargesTo: sc.Boundaries.Charges.To,
		})
		res := f.run(sc, c, fees.Options{})
		feeCount(t, res, 2)
		checkFee(t, byGroup(t, res, "cloud", "aws"), wantFee{6000, "6000", "3", 2000, "20", -1})
		checkFee(t, byGroup(t, res, "cloud", "gcp"), wantFee{20000, "20000", "10", 2000, "20", -1})
	})
}

// TestChargeFeesTargetWallet bills one fee per target wallet code of a
// charge accepting a target wallet.
//
// source: spec/services/fees/charge_service_spec.rb:990
func TestChargeFeesTargetWallet(t *testing.T) {
	t.Parallel()
	f := newChargeFixture(t)
	m := f.Metric("metric", catalog.AggregationTypeSum, "value")
	c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"20"}`, func(c *catalog.Charge) { c.AcceptsTargetWallet = true })
	f.Event(f.sub, m.Code, date(2022, time.March, 16), catalog.EventTargetWalletCode, "wallet_1", "value", "10")
	f.Event(f.sub, m.Code, date(2022, time.March, 16), catalog.EventTargetWalletCode, "wallet_2", "value", "5")
	_, res := f.fees(c, fees.Options{})
	feeCount(t, res, 2)
	w1 := byGroup(t, res, catalog.EventTargetWalletCode, "wallet_1")
	checkFee(t, w1, wantFee{20000, "20000", "10", 2000, "20", -1})
	w2 := byGroup(t, res, catalog.EventTargetWalletCode, "wallet_2")
	checkFee(t, w2, wantFee{10000, "10000", "5", 2000, "20", -1})
	for _, fee := range []*invoice.Fee{w1, w2} {
		if len(fee.GroupedBy) != 1 {
			t.Errorf("grouped_by: got = %v, want = only %s", fee.GroupedBy, catalog.EventTargetWalletCode)
		}
	}
}

// source: spec/services/fees/charge_service_spec.rb:1076
func TestChargeFeesAlreadyBilled(t *testing.T) {
	t.Parallel()
	f := newChargeFixture(t)
	m := f.Metric("metric", catalog.AggregationTypeCount, "")
	c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"20"}`)
	f.Event(f.sub, m.Code, date(2022, time.March, 16))
	sc := f.scope(f.sub, f.b, date(2022, time.April, 1))
	f.store(&invoice.Fee{
		ID: core.NewID[core.FeeID](f.IDs), OrganizationID: f.OrgID(), InvoiceID: sc.Invoice.ID, SubscriptionID: f.sub.ID,
		ChargeID: c.ID, Type: core.FeeTypeCharge, Currency: billingtest.USD, PreciseAmount: money.Zero, Units: money.Zero,
		Invoiceable: true, PaymentStatus: invoice.FeePaymentStatusPending, CreatedAt: f.Now(),
	})
	res := f.run(sc, c, fees.Options{})
	feeCount(t, res, 0)
}

// source: spec/services/fees/charge_service_spec.rb:1113
func TestChargeFeesUpgraded(t *testing.T) {
	t.Parallel()
	f := newFixture(t, date(2022, time.March, 1))
	cust := f.Customer()
	prev := f.Subscription(cust, f.Plan(period.Monthly, 80), billingtest.SubscribedAt(date(2022, time.March, 1)))
	f.UpdateSubscription(prev, func(s *subscription.Subscription) {
		s.Status, s.TerminatedAt = subscription.StatusTerminated, ptr(date(2022, time.April, 15))
	})
	f.Clock.Set(date(2022, time.April, 15))
	plan := f.Plan(period.Monthly, 100)
	s := f.Subscription(cust, plan, billingtest.SubscribedAt(date(2022, time.April, 15)), func(s *subscription.Subscription) {
		s.PreviousSubscriptionID = prev.ID
	})
	m := f.Metric("metric", catalog.AggregationTypeCount, "")
	c := f.Charge(plan, m, catalog.ChargeModelStandard, `{"amount":"20"}`)
	f.Event(s, m.Code, date(2022, time.April, 20))
	b := period.Boundaries{
		Subscription: span(date(2022, time.April, 15), date(2022, time.May, 1)), Charges: span(date(2022, time.April, 15), date(2022, time.May, 1)),
		ChargesBilled: true, ChargesDays: 30,
	}
	sc := f.scope(s, b, date(2022, time.May, 1))
	res := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*fees.ChargeResult, error) {
		return f.svc.ChargeFees(ctx, r, sc, c, fees.Options{})
	})
	feeCount(t, res, 1)
	checkFee(t, res.Fees[0], wantFee{2000, "2000", "1", 2000, "20", 1})
}

func TestChargeFeesAdjusted(t *testing.T) {
	t.Parallel()
	name := "test123"
	cases := []struct {
		name  string
		final bool
		skip  bool
		adj   invoice.AdjustedFee
		min   int64
		want  []wantFee
		// display is the wanted display name of the first fee.
		display *string
	}{
		// source: spec/services/fees/charge_service_spec.rb:1253
		{name: "skip adjusted fees", skip: true, adj: invoice.AdjustedFee{Mode: invoice.AdjustmentModeUnits, Units: dec("3")}},
		// source: spec/services/fees/charge_service_spec.rb:1262
		{name: "units", adj: invoice.AdjustedFee{Mode: invoice.AdjustmentModeUnits, Units: dec("3")}, want: []wantFee{{6000, "6000", "3", 2000, "20", 0}}},
		// source: spec/services/fees/charge_service_spec.rb:1285
		{
			name: "units with a true-up", min: 20000, adj: invoice.AdjustedFee{Mode: invoice.AdjustmentModeUnits, Units: dec("3")},
			want: []wantFee{{6000, "6000", "3", 2000, "20", 0}, {4968, "4967.741935483871", "1", 4968, "49.67741935483871", 0}},
		},
		// source: spec/services/fees/charge_service_spec.rb:1465
		{
			name: "units and unit price", adj: invoice.AdjustedFee{Mode: invoice.AdjustmentModeUnitsAndUnitPrice, Units: dec("1000"), UnitPreciseAmount: dec("0.001")},
			want: []wantFee{{100, "100", "1000", 0, "0.001", 0}},
		},
		// source: spec/services/fees/charge_service_spec.rb:1502
		{
			name: "display name", adj: invoice.AdjustedFee{Mode: invoice.AdjustmentModeDisplayName, InvoiceDisplayName: &name, Units: dec("3")},
			want: []wantFee{{0, "0", "0", 0, "0", 0}}, display: &name,
		},
		// source: spec/services/fees/charge_service_spec.rb:1527
		{name: "invoice not a draft", final: true, adj: invoice.AdjustedFee{Mode: invoice.AdjustmentModeUnits, Units: dec("3")}, want: []wantFee{{0, "0", "0", 0, "0", 0}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newChargeFixture(t)
			m := f.Metric("metric", catalog.AggregationTypeCount, "")
			c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"20"}`, func(c *catalog.Charge) { c.MinAmount = tc.min })
			var opts []invoiceOpt
			if !tc.final {
				opts = append(opts, draft)
			}
			sc := f.scope(f.sub, f.b, date(2022, time.April, 1), opts...)
			a := tc.adj
			a.InvoiceID, a.SubscriptionID, a.ChargeID, a.FeeType = sc.Invoice.ID, f.sub.ID, c.ID, core.FeeTypeCharge
			a.ChargesFrom, a.ChargesTo = sc.Boundaries.Charges.From, sc.Boundaries.Charges.To
			f.adjust(&a)
			res := f.run(sc, c, fees.Options{SkipAdjustedFees: tc.skip})
			feeCount(t, res, len(tc.want))
			for i, w := range tc.want {
				checkFee(t, res.Fees[i], w)
			}
			if tc.display != nil && (res.Fees[0].InvoiceDisplayName == nil || *res.Fees[0].InvoiceDisplayName != *tc.display) {
				t.Errorf("got display name = %v, want = %s", res.Fees[0].InvoiceDisplayName, *tc.display)
			}
		})
	}
}

func TestChargeFeesTrueUp(t *testing.T) {
	t.Parallel()
	// source: spec/services/fees/charge_service_spec.rb:1548
	t.Run("without usage", func(t *testing.T) {
		t.Parallel()
		f := newChargeFixture(t)
		m := f.Metric("metric", catalog.AggregationTypeCount, "")
		c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"20"}`, func(c *catalog.Charge) { c.MinAmount = 1000 })
		_, res := f.fees(c, fees.Options{})
		feeCount(t, res, 2)
		checkFee(t, res.Fees[0], wantFee{0, "0", "0", 0, "0", 0})
		checkFee(t, res.Fees[1], wantFee{548, "548.3870967741935", "1", 548, "5.483870967741935", 0})
		if res.Fees[1].TrueUpParentFeeID != res.Fees[0].ID {
			t.Errorf("got true-up parent = %s, want = %s", res.Fees[1].TrueUpParentFeeID, res.Fees[0].ID)
		}
	})
	// source: spec/services/fees/charge_service_spec.rb:1573
	t.Run("in a pricing unit", func(t *testing.T) {
		t.Parallel()
		f := newChargeFixture(t)
		m := f.Metric("metric", catalog.AggregationTypeCount, "")
		c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"20"}`, f.pricingUnit("1"), func(c *catalog.Charge) { c.MinAmount = 1000 })
		_, res := f.fees(c, fees.Options{})
		feeCount(t, res, 2)
		for _, fee := range res.Fees {
			if fee.PricingUnitUsage == nil {
				t.Errorf("got pricing unit usage = nil, want one")
			}
		}
	})
	// source: spec/services/fees/charge_service_spec.rb:2763
	t.Run("with filters", func(t *testing.T) {
		t.Parallel()
		f := newChargeFixture(t)
		m := f.metric(catalog.AggregationTypeCount, "")
		c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"0"}`, func(c *catalog.Charge) {
			c.MinAmount = 1000
			c.Filters = []catalog.ChargeFilter{
				billingtest.ChargeFilter(map[string][]string{"region": {"europe"}}, `{"amount":"20"}`),
				billingtest.ChargeFilter(map[string][]string{"region": {"usa"}}, `{"amount":"50"}`),
			}
		})
		_, res := f.fees(c, fees.Options{})
		feeCount(t, res, 2)
		checkAmount(t, res.Fees[0], 0, "0")
		checkAmount(t, res.Fees[1], 548, "548.3870967741935")
	})
}

// source: spec/services/fees/charge_service_spec.rb:1618
func TestChargeFeesNegativeUnits(t *testing.T) {
	t.Parallel()
	f := newChargeFixture(t)
	m := f.Metric("metric", catalog.AggregationTypeWeightedSum, "value", func(m *catalog.BillableMetric) {
		m.WeightedInterval = catalog.WeightedIntervalSeconds
	})
	c := f.Charge(f.plan, m, catalog.ChargeModelGraduated, `{"graduated_ranges":[{"from_value":0,"to_value":null,"per_unit_amount":"0.01","flat_amount":"0.01"}]}`)
	f.Event(f.sub, m.Code, date(2022, time.March, 16), "value", "-10")
	_, res := f.fees(c, fees.Options{})
	feeCount(t, res, 1)
	got := res.Fees[0]
	checkFee(t, got, wantFee{0, "0", "0", 0, "0", 1})
	if got.TotalAggregatedUnits.Cmp(dec("-10")) != 0 {
		t.Errorf("got total aggregated units = %s, want = -10", got.TotalAggregatedUnits)
	}
}

func TestChargeFeesFilters(t *testing.T) {
	t.Parallel()
	// events are the events of the filter specs: one without filter
	// values, one in the usa, two in europe and one in france.
	events := func(f *chargeFixture, code string, france bool) {
		f.Event(f.sub, code, date(2022, time.March, 16), "foo_bar", "12")
		f.Event(f.sub, code, date(2022, time.March, 16), "region", "usa", "foo_bar", "12")
		f.Event(f.sub, code, date(2022, time.March, 16), "region", "europe", "foo_bar", "10")
		f.Event(f.sub, code, date(2022, time.March, 16), "region", "europe", "foo_bar", "5")
		if france {
			f.Event(f.sub, code, date(2022, time.March, 16), "country", "france", "foo_bar", "5")
		}
	}
	europe, usa, france := map[string][]string{"region": {"europe"}}, map[string][]string{"region": {"usa"}}, map[string][]string{"country": {"france"}}
	cases := []struct {
		name    string
		agg     catalog.AggregationType
		field   string
		model   catalog.ChargeModel
		props   string
		filters []catalog.ChargeFilter
		france  bool
		rate    string
		// want are the fees of the filters, in order; the default bucket's
		// fee follows.
		want []wantFee
	}{
		// source: spec/services/fees/charge_service_spec.rb:1733
		{
			name: "standard count", agg: catalog.AggregationTypeCount, model: catalog.ChargeModelStandard, props: `{"amount":"10.12345"}`, france: true,
			filters: []catalog.ChargeFilter{
				billingtest.ChargeFilter(europe, `{"amount":"20"}`), billingtest.ChargeFilter(usa, `{"amount":"50"}`), billingtest.ChargeFilter(france, `{"amount":"10.12345"}`),
			},
			want: []wantFee{{4000, "4000", "2", 2000, "20", -1}, {5000, "5000", "1", 5000, "50", -1}, {1012, "1012.345", "1", 1012, "10.12345", -1}},
		},
		// source: spec/services/fees/charge_service_spec.rb:1778
		{
			name: "standard sum", agg: catalog.AggregationTypeSum, field: "foo_bar", model: catalog.ChargeModelStandard, props: `{"amount":"10.12345"}`, france: true,
			filters: []catalog.ChargeFilter{
				billingtest.ChargeFilter(europe, `{"amount":"20"}`), billingtest.ChargeFilter(usa, `{"amount":"50"}`), billingtest.ChargeFilter(france, `{"amount":"10.12345"}`),
			},
			want: []wantFee{{30000, "30000", "15", 2000, "20", -1}, {60000, "60000", "12", 5000, "50", -1}, {5062, "5061.725", "5", 1012, "10.12345", -1}},
		},
		// source: spec/services/fees/charge_service_spec.rb:1823
		{
			name: "standard max", agg: catalog.AggregationTypeMax, field: "foo_bar", model: catalog.ChargeModelStandard, props: `{"amount":"10.12345"}`, france: true,
			filters: []catalog.ChargeFilter{
				billingtest.ChargeFilter(europe, `{"amount":"20"}`), billingtest.ChargeFilter(usa, `{"amount":"50"}`), billingtest.ChargeFilter(france, `{"amount":"10.12345"}`),
			},
			want: []wantFee{{20000, "20000", "10", 2000, "20", -1}, {60000, "60000", "12", 5000, "50", -1}, {5062, "5061.725", "5", 1012, "10.12345", -1}},
		},
		// source: spec/services/fees/charge_service_spec.rb:1869
		{
			name: "standard unique count", agg: catalog.AggregationTypeUniqueCount, field: "foo_bar", model: catalog.ChargeModelStandard, props: `{"amount":"10.12345"}`, france: true,
			filters: []catalog.ChargeFilter{
				billingtest.ChargeFilter(europe, `{"amount":"20"}`), billingtest.ChargeFilter(usa, `{"amount":"50"}`), billingtest.ChargeFilter(france, `{"amount":"10.12345"}`),
			},
			want: []wantFee{{4000, "4000", "2", 2000, "20", -1}, {5000, "5000", "1", 5000, "50", -1}, {1012, "1012.345", "1", 1012, "10.12345", -1}},
		},
		// source: spec/services/fees/charge_service_spec.rb:2036
		{
			name: "package count", agg: catalog.AggregationTypeCount, model: catalog.ChargeModelPackage, props: `{"amount":"0","free_units":0,"package_size":1}`, france: true,
			filters: []catalog.ChargeFilter{
				billingtest.ChargeFilter(europe, `{"amount":"100","free_units":1,"package_size":8}`),
				billingtest.ChargeFilter(usa, `{"amount":"50","free_units":0,"package_size":10}`),
				billingtest.ChargeFilter(france, `{"amount":"40","free_units":1,"package_size":5}`),
			},
			want: []wantFee{{10000, "10000", "2", 10000, "100", -1}, {5000, "5000", "1", 5000, "50", -1}, {0, "0", "1", 0, "0", -1}},
		},
		// source: spec/services/fees/charge_service_spec.rb:2190
		{
			name: "percentage count", agg: catalog.AggregationTypeCount, model: catalog.ChargeModelPercentage, props: `{"rate":"0","fixed_amount":"0"}`, france: true,
			filters: []catalog.ChargeFilter{
				billingtest.ChargeFilter(europe, `{"rate":"2","fixed_amount":"1"}`),
				billingtest.ChargeFilter(usa, `{"rate":"1","fixed_amount":"0"}`),
				billingtest.ChargeFilter(france, `{"rate":"5","fixed_amount":"1"}`),
			},
			want: []wantFee{{204, "204", "2", 102, "1.02", -1}, {1, "1", "1", 1, "0.01", -1}, {105, "105", "1", 105, "1.05", -1}},
		},
		// source: spec/services/fees/charge_service_spec.rb:2343
		{
			name: "graduated count", agg: catalog.AggregationTypeCount, model: catalog.ChargeModelGraduated,
			props: `{"graduated_ranges":[{"from_value":0,"to_value":null,"per_unit_amount":"0","flat_amount":"0"}]}`,
			filters: []catalog.ChargeFilter{
				billingtest.ChargeFilter(europe, `{"graduated_ranges":[{"from_value":0,"to_value":null,"per_unit_amount":"0.01","flat_amount":"0.01"}]}`),
				billingtest.ChargeFilter(usa, `{"graduated_ranges":[{"from_value":0,"to_value":null,"per_unit_amount":"0.03","flat_amount":"0.01"}]}`),
			},
			want: []wantFee{{3, "3", "2", 1, "0.015", -1}, {4, "4", "1", 4, "0.04", -1}},
		},
		// source: spec/services/fees/charge_service_spec.rb:2393
		{
			name: "graduated count in a pricing unit", agg: catalog.AggregationTypeCount, model: catalog.ChargeModelGraduated, rate: "2",
			props: `{"graduated_ranges":[{"from_value":0,"to_value":null,"per_unit_amount":"0","flat_amount":"0"}]}`,
			filters: []catalog.ChargeFilter{
				billingtest.ChargeFilter(europe, `{"graduated_ranges":[{"from_value":0,"to_value":null,"per_unit_amount":"0.01","flat_amount":"0.01"}]}`),
				billingtest.ChargeFilter(usa, `{"graduated_ranges":[{"from_value":0,"to_value":null,"per_unit_amount":"0.03","flat_amount":"0.01"}]}`),
			},
			want: []wantFee{{6, "6", "2", 2, "0.02", -1}, {8, "8", "1", 8, "0.08", -1}},
		},
		// source: spec/services/fees/charge_service_spec.rb:2538
		{
			name: "volume count", agg: catalog.AggregationTypeCount, model: catalog.ChargeModelVolume,
			props: `{"volume_ranges":[{"from_value":0,"to_value":null,"per_unit_amount":"0","flat_amount":"0"}]}`,
			filters: []catalog.ChargeFilter{
				billingtest.ChargeFilter(europe, `{"volume_ranges":[{"from_value":0,"to_value":null,"per_unit_amount":"2","flat_amount":"10"}]}`),
				billingtest.ChargeFilter(usa, `{"volume_ranges":[{"from_value":0,"to_value":null,"per_unit_amount":"1","flat_amount":"10"}]}`),
			},
			want: []wantFee{{1400, "1400", "2", 700, "7", -1}, {1100, "1100", "1", 1100, "11", -1}},
		},
		// source: spec/services/fees/charge_service_spec.rb:2680
		{
			name: "graduated percentage count", agg: catalog.AggregationTypeCount, model: catalog.ChargeModelGraduatedPercentage,
			props: `{"graduated_percentage_ranges":[{"from_value":0,"to_value":null,"flat_amount":"1","rate":"0"}]}`,
			filters: []catalog.ChargeFilter{
				billingtest.ChargeFilter(europe, `{"graduated_percentage_ranges":[{"from_value":0,"to_value":null,"flat_amount":"0.01","rate":"2"}]}`),
				billingtest.ChargeFilter(usa, `{"graduated_percentage_ranges":[{"from_value":0,"to_value":null,"flat_amount":"0.01","rate":"3"}]}`),
			},
			want: []wantFee{{5, "5", "2", 2, "0.025", -1}, {4, "4", "1", 4, "0.04", -1}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newChargeFixture(t)
			m := f.metric(tc.agg, tc.field)
			opts := []func(*catalog.Charge){func(c *catalog.Charge) { c.Filters = tc.filters }}
			if tc.rate != "" {
				opts = append(opts, f.pricingUnit(tc.rate))
			}
			c := f.Charge(f.plan, m, tc.model, tc.props, opts...)
			events(f, m.Code, tc.france)
			_, res := f.fees(c, fees.Options{})
			feeCount(t, res, len(tc.want)+1)
			for i, w := range tc.want {
				checkFee(t, byFilter(t, res, c.Filters[i].ID), w)
			}
			// The default bucket comes last.
			if !res.Fees[len(res.Fees)-1].ChargeFilterID.IsZero() {
				t.Errorf("got last fee of filter %s, want the default bucket", res.Fees[len(res.Fees)-1].ChargeFilterID)
			}
		})
	}
}

// usage computes the current usage fees of c on 2022-03-20.
func (f *chargeFixture) usage(c *catalog.Charge, opts fees.Options) *fees.ChargeResult {
	f.T.Helper()
	opts.CurrentUsage = true
	return f.run(f.usageScope(f.sub, date(2022, time.March, 20)), c, opts)
}

func TestChargeFeesCurrentUsage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		agg   catalog.AggregationType
		field string
		model catalog.ChargeModel
		props string
		// events are the values of the events, "" for an event without one.
		events []string
		want   wantFee
	}{
		// source: spec/services/fees/charge_service_spec.rb:3043
		{
			name: "graduated", agg: catalog.AggregationTypeCount, model: catalog.ChargeModelGraduated, events: []string{"", "", "", ""},
			props: `{"graduated_ranges":[{"from_value":0,"to_value":null,"per_unit_amount":"0.01","flat_amount":"0.01"}]}`,
			want:  wantFee{5, "5", "4", 1, "", 4},
		},
		// source: spec/services/fees/charge_service_spec.rb:3095
		{name: "all fees zero", agg: catalog.AggregationTypeCount, model: catalog.ChargeModelStandard, props: `{"amount":"20"}`, want: wantFee{0, "0", "0", 0, "0", 0}},
		// source: spec/services/fees/charge_service_spec.rb:3129
		{
			name: "all fees zero, graduated", agg: catalog.AggregationTypeCount, model: catalog.ChargeModelGraduated,
			props: `{"graduated_ranges":[{"from_value":0,"to_value":10,"per_unit_amount":"2","flat_amount":"0"},{"from_value":11,"to_value":null,"per_unit_amount":"1","flat_amount":"50"}]}`,
			want:  wantFee{0, "0", "0", 0, "0", 0},
		},
		// source: spec/services/fees/charge_service_spec.rb:3172
		{
			name: "all fees zero, package", agg: catalog.AggregationTypeCount, model: catalog.ChargeModelPackage,
			props: `{"amount":"100","free_units":10,"package_size":10}`, want: wantFee{0, "0", "0", 0, "0", 0},
		},
		// source: spec/services/fees/charge_service_spec.rb:3209
		{
			name: "all fees zero, percentage", agg: catalog.AggregationTypeSum, field: "value", model: catalog.ChargeModelPercentage,
			props: `{"rate":"1","fixed_amount":"1"}`, want: wantFee{0, "0", "0", 0, "0", 0},
		},
		// source: spec/services/fees/charge_service_spec.rb:3346
		{
			name: "flat fee without units, graduated", agg: catalog.AggregationTypeCount, model: catalog.ChargeModelGraduated,
			props: `{"graduated_ranges":[{"from_value":0,"to_value":10,"per_unit_amount":"2","flat_amount":"100"},{"from_value":11,"to_value":null,"per_unit_amount":"1","flat_amount":"50"}]}`,
			want:  wantFee{10000, "10000", "0", 0, "", 0},
		},
		// source: spec/services/fees/charge_service_spec.rb:3383
		{
			name: "flat fee without units, volume", agg: catalog.AggregationTypeCount, model: catalog.ChargeModelVolume,
			props: `{"volume_ranges":[{"from_value":0,"to_value":100,"per_unit_amount":"2","flat_amount":"1"},{"from_value":101,"to_value":null,"per_unit_amount":"1","flat_amount":"0"}]}`,
			want:  wantFee{100, "100", "0", 0, "", 0},
		},
		// source: spec/services/fees/charge_service_spec.rb:3412
		{
			name: "flat fee without units, graduated percentage", agg: catalog.AggregationTypeCount, model: catalog.ChargeModelGraduatedPercentage,
			props: `{"graduated_percentage_ranges":[{"from_value":0,"to_value":10,"rate":"1","flat_amount":"100"},{"from_value":11,"to_value":null,"rate":"0.5","flat_amount":"50"}]}`,
			want:  wantFee{10000, "10000", "0", 0, "", 0},
		},
		// source: spec/services/fees/charge_service_spec.rb:3446
		{name: "non-zero fee", agg: catalog.AggregationTypeCount, model: catalog.ChargeModelStandard, props: `{"amount":"20"}`, events: []string{""}, want: wantFee{2000, "2000", "1", 2000, "20", 1}},
		// source: spec/services/fees/charge_service_spec.rb:3469
		{
			name: "zero units with events", agg: catalog.AggregationTypeSum, field: "value", model: catalog.ChargeModelStandard,
			props: `{"amount":"20"}`, events: []string{"0"}, want: wantFee{0, "0", "0", 0, "", 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newChargeFixture(t)
			m := f.Metric("metric", tc.agg, tc.field)
			c := f.Charge(f.plan, m, tc.model, tc.props)
			for _, v := range tc.events {
				if v == "" {
					f.Event(f.sub, m.Code, date(2022, time.March, 16))
				} else {
					f.Event(f.sub, m.Code, date(2022, time.March, 16), "value", v)
				}
			}
			res := f.usage(c, fees.Options{})
			feeCount(t, res, 1)
			got := res.Fees[0]
			checkFee(t, got, tc.want)
			if !got.InvoiceID.IsZero() || !got.ChargeFilterID.IsZero() || len(got.GroupedBy) != 0 || got.PayInAdvance {
				t.Errorf("got fee = %+v, want a current usage fee of the default bucket", got)
			}
		})
	}
}

func TestChargeFeesCurrentUsageFilters(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, europeEvent bool) (*chargeFixture, *catalog.Charge) {
		f := newChargeFixture(t)
		m := f.metric(catalog.AggregationTypeCount, "")
		c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"20"}`, func(c *catalog.Charge) {
			c.Filters = []catalog.ChargeFilter{
				billingtest.ChargeFilter(map[string][]string{"region": {"europe"}}, `{"amount":"20"}`),
				billingtest.ChargeFilter(map[string][]string{"region": {"usa"}}, `{"amount":"50"}`),
			}
		})
		if europeEvent {
			f.Event(f.sub, m.Code, date(2022, time.March, 16), "region", "europe")
		}
		return f, c
	}
	// source: spec/services/fees/charge_service_spec.rb:3524
	t.Run("all filters without usage", func(t *testing.T) {
		t.Parallel()
		f, c := setup(t, false)
		res := f.usage(c, fees.Options{})
		feeCount(t, res, 3)
		for _, fee := range res.Fees {
			checkFee(t, fee, wantFee{0, "0", "0", 0, "", 0})
		}
	})
	// source: spec/services/fees/charge_service_spec.rb:3546
	t.Run("one filter with usage", func(t *testing.T) {
		t.Parallel()
		f, c := setup(t, true)
		res := f.usage(c, fees.Options{})
		feeCount(t, res, 3)
		checkFee(t, byFilter(t, res, c.Filters[0].ID), wantFee{2000, "2000", "1", 2000, "20", 1})
		checkFee(t, byFilter(t, res, c.Filters[1].ID), wantFee{0, "0", "0", 0, "", 0})
		checkFee(t, byFilter(t, res, core.ChargeFilterID{}), wantFee{0, "0", "0", 0, "", 0})
	})
	// source: spec/services/fees/charge_service_spec.rb:4299
	t.Run("without zero units filters", func(t *testing.T) {
		t.Parallel()
		f, c := setup(t, true)
		res := f.usage(c, fees.Options{SkipZeroUnitsFilters: true})
		feeCount(t, res, 2)
		byFilter(t, res, c.Filters[0].ID)
		byFilter(t, res, core.ChargeFilterID{})
	})
}

// source: spec/services/fees/charge_service_spec.rb:4230
func TestChargeFeesFilterWithoutValues(t *testing.T) {
	t.Parallel()
	f := newChargeFixture(t)
	m := f.metric(catalog.AggregationTypeSum, "value")
	c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"20"}`, func(c *catalog.Charge) {
		c.Filters = []catalog.ChargeFilter{
			billingtest.ChargeFilter(map[string][]string{"region": {"europe"}}, `{"amount":"20"}`),
			billingtest.ChargeFilter(nil, `{"amount":"50"}`),
		}
	})
	f.Event(f.sub, m.Code, date(2022, time.March, 16), "region", "europe", "value", "10")
	f.Event(f.sub, m.Code, date(2022, time.March, 16), "value", "5")
	_, res := f.fees(c, fees.Options{})
	feeCount(t, res, 2)
	checkFee(t, byFilter(t, res, c.Filters[0].ID), wantFee{20000, "20000", "10", 2000, "20", -1})
	checkFee(t, byFilter(t, res, core.ChargeFilterID{}), wantFee{10000, "10000", "5", 2000, "20", -1})
}

// source: spec/services/fees/charge_service_spec.rb:2782
func TestChargeFeesRecurringWeightedSum(t *testing.T) {
	t.Parallel()
	f := newChargeFixture(t)
	m := f.Metric("metric", catalog.AggregationTypeWeightedSum, "value", func(m *catalog.BillableMetric) {
		m.WeightedInterval, m.Recurring = catalog.WeightedIntervalSeconds, true
	})
	c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"20"}`)
	_, res := f.fees(c, fees.Options{Recurring: true})
	feeCount(t, res, 1)
	checkFee(t, res.Fees[0], wantFee{0, "0", "0", 0, "", 0})
	if !res.Fees[0].TotalAggregatedUnits.IsZero() {
		t.Errorf("got total aggregated units = %s, want = 0", res.Fees[0].TotalAggregatedUnits)
	}
	if len(res.Aggregations) != 1 {
		t.Fatalf("got %d cached aggregations, want = 1", len(res.Aggregations))
	}
	a := res.Aggregations[0]
	if a.ChargeID != c.ID || !a.ChargeFilterID.IsZero() || a.ExternalSubscriptionID != f.sub.ExternalID {
		t.Errorf("got cached aggregation = %+v, want one of the charge's default bucket", a)
	}
	if !a.Aggregation.Timestamp.Equal(f.b.Subscription.From) || !a.Aggregation.CurrentAggregation.IsZero() {
		t.Errorf("got cached aggregation = %+v, want 0 at %s", a.Aggregation, f.b.Subscription.From)
	}
}

// A grouped recurring aggregation keeps one state per group, each at the
// instant of the group's last event.
// source: spec/services/fees/charge_service_spec.rb:505
func TestChargeFeesRecurringWeightedSumGrouped(t *testing.T) {
	t.Parallel()
	f := newChargeFixture(t)
	m := f.Metric("metric", catalog.AggregationTypeWeightedSum, "value", func(m *catalog.BillableMetric) {
		m.WeightedInterval, m.Recurring = catalog.WeightedIntervalSeconds, true
	})
	c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"20","pricing_group_keys":["cloud"]}`)
	f.Event(f.sub, m.Code, date(2022, time.March, 16), "cloud", "aws", "value", "10")
	f.Event(f.sub, m.Code, date(2022, time.March, 20), "cloud", "gcp", "value", "3")
	_, res := f.fees(c, fees.Options{})
	feeCount(t, res, 2)
	if len(res.Aggregations) != 2 {
		t.Fatalf("got %d cached aggregations, want = 2", len(res.Aggregations))
	}
	want := map[string]time.Time{"aws": date(2022, time.March, 16), "gcp": date(2022, time.March, 20)}
	for _, a := range res.Aggregations {
		cloud, _ := a.Aggregation.Groups.Value("cloud")
		if at, ok := want[cloud]; !ok || !a.Aggregation.Timestamp.Equal(at) {
			t.Errorf("got cached aggregation of %q at %s, want = %s", cloud, a.Aggregation.Timestamp, at)
		}
	}
}

// A charge terminated at local midnight bills its true-up for the day the
// period ends on too, as lago counts it.
// source: app/services/fees/create_true_up_service.rb:74
func TestChargeFeesTrueUpTerminatedAtMidnight(t *testing.T) {
	t.Parallel()
	f := newChargeFixture(t)
	end := date(2022, time.March, 25)
	f.UpdateSubscription(f.sub, func(s *subscription.Subscription) {
		s.Status, s.TerminatedAt = subscription.StatusTerminated, ptr(end)
	})
	f.b.Subscription, f.b.Charges, f.b.FixedCharges = span(f.b.Charges.From, end), span(f.b.Charges.From, end), span(f.b.Charges.From, end)
	m := f.Metric("metric", catalog.AggregationTypeCount, "")
	c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"20"}`, func(c *catalog.Charge) { c.MinAmount = 1000 })
	_, res := f.fees(c, fees.Options{})
	feeCount(t, res, 2)
	// 11 days (2022-03-15 to 2022-03-25 inclusive) of 31.
	checkAmount(t, res.Fees[1], 355, "354.8387096774193548")
}
