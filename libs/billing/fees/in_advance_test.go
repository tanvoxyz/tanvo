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
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// inAdvance computes the fees of the stored event e of c.
func (f *chargeFixture) inAdvance(c *catalog.Charge, e metering.Event, estimate bool) *fees.ChargeResult {
	f.T.Helper()
	env := f.env(f.sub)
	ev := fees.InAdvanceEvent{Event: e, Estimate: estimate}
	if !estimate {
		ev.ID = core.NewID[core.EventID](f.IDs)
	}
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*fees.ChargeResult, error) {
		return f.svc.InAdvanceChargeFees(ctx, r, env, c, ev, fees.Options{})
	})
}

func advance(c *catalog.Charge) { c.PayInAdvance = true }

// source: spec/services/fees/create_pay_in_advance_service_spec.rb:256
func TestInAdvanceChargeFees(t *testing.T) {
	t.Parallel()
	f := newChargeFixture(t)
	m := f.Metric("metric", catalog.AggregationTypeCount, "")
	c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"10"}`, advance)
	for i := range 2 {
		e := f.Event(f.sub, m.Code, date(2022, time.March, 20+i))
		res := f.inAdvance(c, e, false)
		feeCount(t, res, 1)
		got := res.Fees[0]
		checkFee(t, got, wantFee{1000, "1000", "1", 1000, "10", 1})
		if !got.InvoiceID.IsZero() || !got.PayInAdvance || got.PayInAdvanceEventTransactionID != e.TransactionID || got.PayInAdvanceEventID.IsZero() {
			t.Errorf("got fee = %+v, want a pay-in-advance fee of the event without invoice", got)
		}
		if !got.Properties.ChargesFrom.Equal(date(2022, time.March, 15)) || !got.Properties.ChargesTo.Equal(date(2022, time.April, 1)) {
			t.Errorf("got properties = %+v, want the current charges period", got.Properties)
		}
	}
}

// Each event pays the price of the usage with it less the price without it.
// source: spec/services/charges/apply_pay_in_advance_charge_model_service_spec.rb:169
func TestInAdvanceChargeFeesIncremental(t *testing.T) {
	t.Parallel()
	f := newChargeFixture(t)
	m := f.Metric("metric", catalog.AggregationTypeSum, "value")
	c := f.Charge(f.plan, m, catalog.ChargeModelGraduated,
		`{"graduated_ranges":[{"from_value":0,"to_value":10,"per_unit_amount":"1","flat_amount":"0"},{"from_value":11,"to_value":null,"per_unit_amount":"0.5","flat_amount":"0"}]}`, advance)
	for _, tc := range []struct {
		value  string
		amount int64
	}{{"8", 800}, {"5", 350}, {"4", 200}} {
		res := f.inAdvance(c, f.Event(f.sub, m.Code, date(2022, time.March, 20), "value", tc.value), false)
		feeCount(t, res, 1)
		checkFee(t, res.Fees[0], wantFee{tc.amount, "", tc.value, res.Fees[0].UnitAmount, "", 1})
	}
}

func TestInAdvanceChargeFeesFilters(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, filterProps string) (*chargeFixture, *catalog.Charge) {
		f := newChargeFixture(t)
		m := f.metric(catalog.AggregationTypeCount, "")
		c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"10"}`, advance, func(c *catalog.Charge) {
			c.Filters = []catalog.ChargeFilter{billingtest.ChargeFilter(map[string][]string{"region": {"europe"}}, filterProps)}
		})
		return f, c
	}
	// source: spec/services/fees/create_pay_in_advance_service_spec.rb:377
	t.Run("matching filter", func(t *testing.T) {
		t.Parallel()
		f, c := setup(t, `{"amount":"20"}`)
		res := f.inAdvance(c, f.Event(f.sub, "metric_count", date(2022, time.March, 20), "region", "europe"), false)
		feeCount(t, res, 1)
		checkFee(t, res.Fees[0], wantFee{2000, "2000", "1", 2000, "20", 1})
		if res.Fees[0].ChargeFilterID != c.Filters[0].ID {
			t.Errorf("got filter = %s, want = %s", res.Fees[0].ChargeFilterID, c.Filters[0].ID)
		}
	})
	// source: spec/services/fees/create_pay_in_advance_service_spec.rb:422
	t.Run("filter with pricing group keys", func(t *testing.T) {
		t.Parallel()
		f, c := setup(t, `{"amount":"20","pricing_group_keys":["cloud"]}`)
		res := f.inAdvance(c, f.Event(f.sub, "metric_count", date(2022, time.March, 20), "region", "europe", "cloud", "aws"), false)
		feeCount(t, res, 1)
		if got := res.Fees[0].GroupedBy; got["cloud"] != "aws" || len(got) != 1 {
			t.Errorf("got grouped by = %v, want = map[cloud:aws]", got)
		}
	})
	// source: spec/services/fees/create_pay_in_advance_service_spec.rb:514
	t.Run("default bucket", func(t *testing.T) {
		t.Parallel()
		f, c := setup(t, `{"amount":"20"}`)
		res := f.inAdvance(c, f.Event(f.sub, "metric_count", date(2022, time.March, 20), "region", "usa"), false)
		feeCount(t, res, 1)
		checkFee(t, res.Fees[0], wantFee{1000, "1000", "1", 1000, "10", 1})
		if !res.Fees[0].ChargeFilterID.IsZero() {
			t.Errorf("got filter = %s, want the default bucket", res.Fees[0].ChargeFilterID)
		}
	})
}

// source: spec/services/fees/create_pay_in_advance_service_spec.rb:575
func TestInAdvanceChargeFeesGrouped(t *testing.T) {
	t.Parallel()
	f := newChargeFixture(t)
	m := f.Metric("metric", catalog.AggregationTypeCount, "")
	c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"10","pricing_group_keys":["cloud"]}`, advance)
	res := f.inAdvance(c, f.Event(f.sub, m.Code, date(2022, time.March, 20), "cloud", "aws"), false)
	feeCount(t, res, 1)
	if got := res.Fees[0].GroupedBy; got["cloud"] != "aws" {
		t.Errorf("got grouped by = %v, want = map[cloud:aws]", got)
	}
}

// source: spec/services/fees/create_pay_in_advance_service_spec.rb:622
func TestInAdvanceChargeFeesEstimate(t *testing.T) {
	t.Parallel()
	f := newChargeFixture(t)
	m := f.Metric("metric", catalog.AggregationTypeCount, "")
	c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"10"}`, advance)
	e := metering.Event{
		OrganizationID: f.OrgID().String(), SubscriptionID: f.sub.ExternalID, Code: m.Code,
		TransactionID: "estimate", Timestamp: date(2022, time.March, 20), IngestedAt: f.Now(),
	}
	res := f.inAdvance(c, e, true)
	feeCount(t, res, 1)
	checkFee(t, res.Fees[0], wantFee{1000, "1000", "1", 1000, "10", 1})
	if !res.Fees[0].PayInAdvanceEventID.IsZero() {
		t.Errorf("got event ID = %s, want zero", res.Fees[0].PayInAdvanceEventID)
	}
}

// source: spec/services/fees/create_pay_in_advance_service_spec.rb:680
func TestInAdvanceChargeFeesPricingUnit(t *testing.T) {
	t.Parallel()
	f := newChargeFixture(t)
	m := f.Metric("metric", catalog.AggregationTypeCount, "")
	c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"10"}`, advance, f.pricingUnit("0.5"))
	res := f.inAdvance(c, f.Event(f.sub, m.Code, date(2022, time.March, 20)), false)
	feeCount(t, res, 1)
	got := res.Fees[0]
	checkFee(t, got, wantFee{500, "500", "1", 500, "5", 1})
	// source: spec/services/fees/create_pay_in_advance_service_spec.rb:713
	if pu := got.PricingUnitUsage; pu == nil || pu.Amount.Cmp(dec("1000")) != 0 {
		t.Errorf("got pricing unit usage = %+v, want an amount of 1000", pu)
	}
}

// source: spec/services/fees/create_pay_in_advance_service_spec.rb:735
func TestInAdvanceChargeFeesCachedAggregation(t *testing.T) {
	t.Parallel()
	f := newChargeFixture(t)
	m := f.Metric("metric", catalog.AggregationTypeSum, "value")
	c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"10"}`, advance)
	e := f.Event(f.sub, m.Code, date(2022, time.March, 20), "value", "3")
	res := f.inAdvance(c, e, false)
	if len(res.Aggregations) != 1 {
		t.Fatalf("got %d cached aggregations, want = 1", len(res.Aggregations))
	}
	a := res.Aggregations[0]
	if a.ChargeID != c.ID || a.Aggregation.TransactionID != e.TransactionID || !a.Aggregation.Timestamp.Equal(e.Timestamp) ||
		a.Aggregation.CurrentAggregation.Cmp(dec("3")) != 0 {
		t.Errorf("got cached aggregation = %+v, want 3 at the event", a)
	}
}

// fixedAdvanceFixture is the setup of
// spec/services/fees/build_pay_in_advance_fixed_charge_service_spec.rb: a
// monthly plan paid in advance subscribed on 2024-03-01, with 10 units of a
// fixed charge at 10.
type fixedAdvanceFixture struct {
	*fixture
	sub *subscription.Subscription
	fc  *catalog.FixedCharge
}

func newFixedAdvanceFixture(t *testing.T, prorated bool, paid ...string) *fixedAdvanceFixture {
	t.Helper()
	f := newFixture(t, date(2024, time.March, 1))
	plan := f.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.PayInAdvance = true })
	fc := f.FixedCharge(plan, f.AddOn(0), catalog.FixedChargeModelStandard, `{"amount":"10"}`, "10", func(fc *catalog.FixedCharge) {
		fc.PayInAdvance, fc.Prorated = true, prorated
	})
	s := f.Subscription(f.Customer(), plan, billingtest.SubscribedAt(date(2024, time.March, 1)))
	p := span(date(2024, time.March, 1), date(2024, time.April, 1))
	for _, u := range paid {
		f.store(&invoice.Fee{
			ID: core.NewID[core.FeeID](f.IDs), OrganizationID: f.OrgID(), SubscriptionID: s.ID, FixedChargeID: fc.ID,
			Type: core.FeeTypeFixedCharge, Currency: billingtest.USD, Units: dec(u), PreciseAmount: dec("0"), Invoiceable: true,
			PaymentStatus: invoice.FeePaymentStatusPending, CreatedAt: f.Now(),
			Properties: invoice.FeeProperties{From: p.From, To: p.To, ChargesFrom: p.From, ChargesTo: p.To},
		})
	}
	return &fixedAdvanceFixture{fixture: f, sub: s, fc: fc}
}

func (f *fixedAdvanceFixture) fee(ts time.Time, units string) *invoice.Fee {
	f.T.Helper()
	env := f.env(f.sub)
	ev := &subscription.FixedChargeEvent{
		ID: core.NewID[core.FixedChargeEventID](f.IDs), OrganizationID: f.OrgID(), SubscriptionID: f.sub.ID, FixedChargeID: f.fc.ID,
		Units: dec(units), Timestamp: ts, ApplyUnitsImmediately: true, CreatedAt: ts, UpdatedAt: ts,
	}
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*invoice.Fee, error) {
		return f.svc.InAdvanceFixedChargeFee(ctx, r, env, f.fc, ev)
	})
}

func TestInAdvanceFixedChargeFee(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		prorated bool
		paid     []string
		ts       time.Time
		units    string
		want     int64
		// wantUnits are the units billed.
		wantUnits string
	}{
		// source: spec/services/fees/build_pay_in_advance_fixed_charge_service_spec.rb:50
		{name: "added", ts: date(2024, time.March, 15), units: "10", want: 10000, wantUnits: "10"},
		// source: spec/services/fees/build_pay_in_advance_fixed_charge_service_spec.rb:93
		{name: "increase", paid: []string{"10"}, ts: date(2024, time.March, 15), units: "15", want: 5000, wantUnits: "5"},
		// source: spec/services/fees/build_pay_in_advance_fixed_charge_service_spec.rb:136
		{name: "decrease", paid: []string{"10"}, ts: date(2024, time.March, 15), units: "5", want: 0, wantUnits: "0"},
		// source: spec/services/fees/build_pay_in_advance_fixed_charge_service_spec.rb:179
		{name: "unchanged", paid: []string{"10"}, ts: date(2024, time.March, 15), units: "10", want: 0, wantUnits: "0"},
		// source: spec/services/fees/build_pay_in_advance_fixed_charge_service_spec.rb:240
		{name: "several paid fees", paid: []string{"10", "5"}, ts: date(2024, time.March, 15), units: "20", want: 5000, wantUnits: "5"},
		// source: spec/services/fees/build_pay_in_advance_fixed_charge_service_spec.rb:337
		{name: "prorated, added mid-period", prorated: true, ts: date(2024, time.March, 15), units: "10", want: 5484, wantUnits: "10"},
		// source: spec/services/fees/build_pay_in_advance_fixed_charge_service_spec.rb:361
		{name: "prorated, added on the first day", prorated: true, ts: date(2024, time.March, 1), units: "10", want: 10000, wantUnits: "10"},
		// source: spec/services/fees/build_pay_in_advance_fixed_charge_service_spec.rb:384
		{name: "prorated, added on the last day", prorated: true, ts: date(2024, time.March, 31), units: "10", want: 323, wantUnits: "10"},
		// source: spec/services/fees/build_pay_in_advance_fixed_charge_service_spec.rb:475
		{name: "prorated decrease", prorated: true, paid: []string{"10"}, ts: date(2024, time.March, 15), units: "5", want: 0, wantUnits: "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixedAdvanceFixture(t, tc.prorated, tc.paid...)
			got := f.fee(tc.ts, tc.units)
			checkAmount(t, got, tc.want, "")
			if !eqDec(got.Units, dec(tc.wantUnits)) {
				t.Errorf("got units = %s, want = %s", got.Units, tc.wantUnits)
			}
			if !got.PayInAdvance || !got.InvoiceID.IsZero() || got.FixedChargeID != f.fc.ID {
				t.Errorf("got fee = %+v, want a pay-in-advance fee of the fixed charge without invoice", got)
			}
		})
	}
}
