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
	"github.com/tanvoxyz/tanvo/libs/period"
)

// fixedFixture is the setup of spec/services/fees/fixed_charge_service_spec.rb:
// a monthly subscription started on 2022-03-17, billed for the rest of
// March.
type fixedFixture struct {
	*fixture
	sub  *subscription.Subscription
	plan *catalog.Plan
	b    period.Boundaries
}

func newFixedFixture(t *testing.T) *fixedFixture {
	t.Helper()
	f := newFixture(t, date(2022, time.March, 17))
	plan := f.Plan(period.Monthly, 100)
	s := f.Subscription(f.Customer(), plan, billingtest.SubscribedAt(date(2022, time.March, 17)))
	p := span(date(2022, time.March, 17), date(2022, time.April, 1))
	b := period.Boundaries{
		Subscription: p, Charges: p, ChargesBilled: true, FixedCharges: p, FixedChargesBilled: true,
		ChargesDays: 31, FixedChargesDays: 31,
	}
	f.Clock.Set(date(2022, time.April, 1))
	return &fixedFixture{fixture: f, sub: s, plan: plan, b: b}
}

// unitsAt stores a fixed charge event applied immediately.
func (f *fixedFixture) unitsAt(fc *catalog.FixedCharge, ts time.Time, units string) {
	f.T.Helper()
	ev := &subscription.FixedChargeEvent{
		ID: core.NewID[core.FixedChargeEventID](f.IDs), OrganizationID: f.OrgID(), SubscriptionID: f.sub.ID, FixedChargeID: fc.ID,
		Units: dec(units), Timestamp: ts, ApplyUnitsImmediately: true, CreatedAt: ts, UpdatedAt: ts,
	}
	f.Do(func(ctx context.Context, r port.Repos) error { return r.FixedChargeEvents.Record(ctx, ev, time.UTC) })
}

// fee computes the fee of fc in sc.
func (f *fixedFixture) fee(sc fees.Scope, fc *catalog.FixedCharge, opts fees.Options) *invoice.Fee {
	f.T.Helper()
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*invoice.Fee, error) {
		return f.svc.FixedChargeFee(ctx, r, sc, fc, opts)
	})
}

func TestFixedChargeFee(t *testing.T) {
	t.Parallel()
	graduated := `{"graduated_ranges":[{"from_value":0,"to_value":5,"per_unit_amount":"0.1","flat_amount":"10"},{"from_value":6,"to_value":null,"per_unit_amount":"2","flat_amount":"20"}]}`
	volume := `{"volume_ranges":[{"from_value":0,"to_value":10,"per_unit_amount":"0.1","flat_amount":"10"},{"from_value":11,"to_value":null,"per_unit_amount":"2","flat_amount":"20"}]}`
	type change struct {
		days  int
		units string
	}
	cases := []struct {
		name     string
		model    catalog.FixedChargeModel
		props    string
		prorated bool
		// changes are unit changes, days after the start of the period.
		changes []change
		want    wantFee
	}{
		// source: spec/services/fees/fixed_charge_service_spec.rb:126
		{
			name: "standard prorated", model: catalog.FixedChargeModelStandard, props: `{"amount":"310"}`, prorated: true,
			changes: []change{{12, "10"}}, want: wantFee{30000, "30000.002", "10", 3000, "", 0},
		},
		// source: spec/services/fees/fixed_charge_service_spec.rb:177
		{
			name: "standard", model: catalog.FixedChargeModelStandard, props: `{"amount":"20"}`,
			changes: []change{{12, "10"}}, want: wantFee{20000, "20000", "10", 2000, "20", 0},
		},
		// source: spec/services/fees/fixed_charge_service_spec.rb:322
		{
			name: "graduated", model: catalog.FixedChargeModelGraduated, props: graduated,
			changes: []change{{5, "62"}, {10, "3.1"}}, want: wantFee{1031, "1031", "3.1", 332, "3.3258064516", 0},
		},
		// source: spec/services/fees/fixed_charge_service_spec.rb:366
		{
			name: "graduated prorated", model: catalog.FixedChargeModelGraduated, props: graduated, prorated: true,
			changes: []change{{5, "62"}, {10, "3.1"}}, want: wantFee{1105, "1105", "3.1", 356, "", 0},
		},
		// source: spec/services/fees/fixed_charge_service_spec.rb:414
		{
			name: "volume", model: catalog.FixedChargeModelVolume, props: volume,
			changes: []change{{5, "31"}, {10, "3.1"}}, want: wantFee{1031, "1031", "3.1", 332, "3.3258064516", 0},
		},
		// source: spec/services/fees/fixed_charge_service_spec.rb:456
		{
			name: "volume prorated", model: catalog.FixedChargeModelVolume, props: volume, prorated: true,
			changes: []change{{5, "31"}, {10, "3.1"}}, want: wantFee{1055, "1055", "3.1", 340, "3.4032258065", 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixedFixture(t)
			fc := f.FixedCharge(f.plan, f.AddOn(0), tc.model, tc.props, "0", func(fc *catalog.FixedCharge) { fc.Prorated = tc.prorated })
			for _, c := range tc.changes {
				f.unitsAt(fc, f.b.FixedCharges.From.AddDate(0, 0, c.days), c.units)
			}
			sc := f.scope(f.sub, f.b, date(2022, time.April, 1), draft)
			got := f.fee(sc, fc, fees.Options{})
			checkFee(t, got, tc.want)
			if got.FixedChargeID != fc.ID || got.Type != core.FeeTypeFixedCharge || got.InvoiceID != sc.Invoice.ID {
				t.Errorf("got fee = %+v, want a fee of the fixed charge on the invoice", got)
			}
		})
	}
}

// A fee without units nor amount is not stored: lago builds it unpersisted,
// tanvo returns nil.
// source: spec/services/fees/fixed_charge_service_spec.rb:62
func TestFixedChargeFeeWithoutUnits(t *testing.T) {
	t.Parallel()
	f := newFixedFixture(t)
	fc := f.FixedCharge(f.plan, f.AddOn(0), catalog.FixedChargeModelStandard, `{"amount":"310"}`, "0", func(fc *catalog.FixedCharge) { fc.Prorated = true })
	if got := f.fee(f.scope(f.sub, f.b, date(2022, time.April, 1)), fc, fees.Options{}); got != nil {
		t.Errorf("got fee = %+v, want nil", got)
	}
	// A recurring invoice keeps it.
	got := f.fee(f.scope(f.sub, monthly(date(2022, time.April, 1), date(2022, time.May, 1)), date(2022, time.May, 1)), fc, fees.Options{Recurring: true})
	checkFee(t, got, wantFee{0, "0", "0", 0, "", 0})
}

// source: spec/services/fees/fixed_charge_service_spec.rb:151
func TestFixedChargeFeeProperties(t *testing.T) {
	t.Parallel()
	f := newFixedFixture(t)
	fc := f.FixedCharge(f.plan, f.AddOn(0), catalog.FixedChargeModelStandard, `{"amount":"310"}`, "0", func(fc *catalog.FixedCharge) { fc.Prorated = true })
	f.unitsAt(fc, date(2022, time.March, 29), "10")
	got := f.fee(f.scope(f.sub, f.b, date(2022, time.April, 1)), fc, fees.Options{})
	p := got.Properties
	if !p.ChargesFrom.Equal(date(2022, time.March, 17)) || !p.ChargesTo.Equal(date(2022, time.April, 1)) || p.ChargesDurationDays != 31 {
		t.Errorf("got properties = %+v, want fixed charges [2022-03-17, 2022-04-01) over 31 days", p)
	}
}

// source: spec/services/fees/fixed_charge_service_spec.rb:482
func TestFixedChargeFeeAlreadyBilled(t *testing.T) {
	t.Parallel()
	f := newFixedFixture(t)
	fc := f.FixedCharge(f.plan, f.AddOn(0), catalog.FixedChargeModelStandard, `{"amount":"20"}`, "1")
	sc := f.scope(f.sub, f.b, date(2022, time.April, 1))
	f.store(&invoice.Fee{
		ID: core.NewID[core.FeeID](f.IDs), OrganizationID: f.OrgID(), InvoiceID: sc.Invoice.ID, SubscriptionID: f.sub.ID,
		FixedChargeID: fc.ID, Type: core.FeeTypeFixedCharge, Currency: billingtest.USD, PreciseAmount: dec("0"), Units: dec("1"),
		Invoiceable: true, PaymentStatus: invoice.FeePaymentStatusPending, CreatedAt: f.Now(),
	})
	if got := f.fee(sc, fc, fees.Options{}); got != nil {
		t.Errorf("got fee = %+v, want nil", got)
	}
}

func TestFixedChargeFeeAdjusted(t *testing.T) {
	t.Parallel()
	name := "Custom Fixed Charge Name"
	cases := []struct {
		name  string
		final bool
		adj   invoice.AdjustedFee
		want  wantFee
	}{
		// source: spec/services/fees/fixed_charge_service_spec.rb:771
		{name: "units", adj: invoice.AdjustedFee{Mode: invoice.AdjustmentModeUnits, Units: dec("5")}, want: wantFee{155000, "155000", "5", 31000, "310", 0}},
		// source: spec/services/fees/fixed_charge_service_spec.rb:815
		{
			name: "units and unit price", adj: invoice.AdjustedFee{Mode: invoice.AdjustmentModeUnitsAndUnitPrice, Units: dec("10"), UnitPreciseAmount: dec("5")},
			want: wantFee{5000, "5000", "10", 500, "5", 0},
		},
		// source: spec/services/fees/fixed_charge_service_spec.rb:851
		{
			name: "display name", adj: invoice.AdjustedFee{Mode: invoice.AdjustmentModeDisplayName, InvoiceDisplayName: &name, Units: dec("5")},
			want: wantFee{30000, "30000.002", "10", 3000, "30.000002", 0},
		},
		// source: spec/services/fees/fixed_charge_service_spec.rb:887
		{name: "zero units", adj: invoice.AdjustedFee{Mode: invoice.AdjustmentModeUnits, Units: dec("0")}, want: wantFee{0, "0", "0", 0, "", 0}},
		// source: spec/services/fees/fixed_charge_service_spec.rb:917
		{
			name: "invoice not a draft", final: true, adj: invoice.AdjustedFee{Mode: invoice.AdjustmentModeUnits, Units: dec("5")},
			want: wantFee{30000, "30000.002", "10", 3000, "30.000002", 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixedFixture(t)
			fc := f.FixedCharge(f.plan, f.AddOn(0), catalog.FixedChargeModelStandard, `{"amount":"310"}`, "0", func(fc *catalog.FixedCharge) { fc.Prorated = true })
			f.unitsAt(fc, date(2022, time.March, 29), "10")
			var opts []invoiceOpt
			if !tc.final {
				opts = append(opts, draft)
			}
			sc := f.scope(f.sub, f.b, date(2022, time.April, 1), opts...)
			a := tc.adj
			a.InvoiceID, a.SubscriptionID, a.FixedChargeID, a.FeeType = sc.Invoice.ID, f.sub.ID, fc.ID, core.FeeTypeFixedCharge
			f.adjust(&a)
			got := f.fee(sc, fc, fees.Options{})
			checkFee(t, got, tc.want)
			if a.Mode == invoice.AdjustmentModeDisplayName && (got.InvoiceDisplayName == nil || *got.InvoiceDisplayName != name) {
				t.Errorf("got display name = %v, want = %s", got.InvoiceDisplayName, name)
			}
		})
	}
}

// The units of a fixed charge come from its changes ordered by creation: a
// change created later but taking effect earlier supersedes another, and
// the last created one sets the units of the fee.
func TestFixedChargeFeeChangeOrder(t *testing.T) {
	t.Parallel()
	type change struct {
		at, created time.Time
		units       string
	}
	cases := []struct {
		name    string
		changes []change
		want    wantFee
	}{
		// source: spec/services/fees/fixed_charge_service_spec.rb:228
		{
			name:    "created after the period, effective before it",
			changes: []change{{date(2022, time.January, 17), date(2022, time.May, 17), "10"}},
			want:    wantFee{150000, "", "10", 15000, "", 0},
		},
		// source: spec/services/fees/fixed_charge_service_spec.rb:250
		{
			name:    "created after the period, effective in it",
			changes: []change{{date(2022, time.March, 22), date(2022, time.May, 17), "10"}},
			want:    wantFee{100000, "", "10", 9999, "", 0},
		},
		// The change to 20 on 03-27 is superseded by the change to 10 on
		// 03-22 created after it: 10 units for 10 days.
		// source: app/services/fixed_charge_events/aggregations/prorated_aggregation_service.rb:42
		{
			name: "superseded by a later created change taking effect earlier",
			changes: []change{
				{date(2022, time.March, 27), date(2022, time.March, 18), "20"},
				{date(2022, time.March, 22), date(2022, time.March, 20), "10"},
			},
			want: wantFee{100000, "", "10", 9999, "", 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixedFixture(t)
			fc := f.FixedCharge(f.plan, f.AddOn(0), catalog.FixedChargeModelStandard, `{"amount":"310"}`, "0", func(fc *catalog.FixedCharge) { fc.Prorated = true })
			for _, c := range tc.changes {
				ev := &subscription.FixedChargeEvent{
					ID: core.NewID[core.FixedChargeEventID](f.IDs), OrganizationID: f.OrgID(), SubscriptionID: f.sub.ID, FixedChargeID: fc.ID,
					Units: dec(c.units), Timestamp: c.at, ApplyUnitsImmediately: true, CreatedAt: c.created, UpdatedAt: c.created,
				}
				f.Do(func(ctx context.Context, r port.Repos) error { return r.FixedChargeEvents.Record(ctx, ev, time.UTC) })
			}
			sc := f.scope(f.sub, f.b, date(2022, time.April, 1), draft)
			checkFee(t, f.fee(sc, fc, fees.Options{}), tc.want)
		})
	}
}
