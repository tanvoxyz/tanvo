package subscription

import (
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestUsageEntities(t *testing.T) {
	type tc struct {
		Name       string            `yaml:"name"`
		Source     string            `yaml:"source"`
		Entity     string            `yaml:"entity"`
		Historical int64             `yaml:"historical"`
		Invoiced   int64             `yaml:"invoiced"`
		Current    int64             `yaml:"current"`
		Total      int64             `yaml:"total"`
		Units      *spectest.Decimal `yaml:"units"`
		Errors     []string          `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "usage.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			var err error
			switch c.Entity {
			case "lifetime_usage":
				l := &LifetimeUsage{
					SubscriptionID: core.NewID[core.SubscriptionID](testIDs), ExternalSubscriptionID: "sub_1",
					Currency: money.MustCurrency("EUR"), HistoricalUsageAmount: c.Historical,
					InvoicedUsageAmount: c.Invoiced, CurrentUsageAmount: c.Current,
				}
				err = l.Validate()
				if c.Total != 0 && l.Total() != c.Total {
					t.Errorf("Total: got = %d, want = %d", l.Total(), c.Total)
				}
			case "fixed_charge_event":
				e := &FixedChargeEvent{
					SubscriptionID: core.NewID[core.SubscriptionID](testIDs), FixedChargeID: core.NewID[core.FixedChargeID](testIDs),
					Units: c.Units.Decimal, Timestamp: testNow,
				}
				err = e.Validate()
			case "applied_usage_threshold":
				a := &AppliedUsageThreshold{
					UsageThresholdID: core.NewID[core.UsageThresholdID](testIDs), SubscriptionID: core.NewID[core.SubscriptionID](testIDs),
					InvoiceID: core.NewID[core.InvoiceID](testIDs), LifetimeUsageAmount: c.Current,
				}
				err = a.Validate()
			default:
				t.Fatalf("entity %q", c.Entity)
			}
			if got := fieldErrors(err); !slices.Equal(got, c.Errors) {
				t.Errorf("errors: got = %v, want = %v", got, c.Errors)
			}
		})
	}
}

func TestLifetimeUsageRefresh(t *testing.T) {
	l := &LifetimeUsage{}
	l.FlagForRecalculation(testNow)
	if !l.RecalculateCurrentUsage || !l.RecalculateInvoicedUsage {
		t.Fatalf("flags: got = %v %v, want = true true", l.RecalculateInvoicedUsage, l.RecalculateCurrentUsage)
	}
	l.SetInvoicedUsage(10, testNow)
	l.SetCurrentUsage(5, testNow)
	if l.RecalculateCurrentUsage || l.RecalculateInvoicedUsage || l.Total() != 15 {
		t.Errorf("after refresh: got = %+v, want = flags cleared and total 15", l)
	}
}

func TestKeepLatestPerDay(t *testing.T) {
	// domain §7.5: a later event the same day replaces the earlier one.
	paris, _ := time.LoadLocation("Europe/Paris")
	sub, fc, other := core.NewID[core.SubscriptionID](testIDs), core.NewID[core.FixedChargeID](testIDs), core.NewID[core.FixedChargeID](testIDs)
	ev := func(fc core.FixedChargeID, h float64, units int64) *FixedChargeEvent {
		return &FixedChargeEvent{
			ID: core.NewID[core.FixedChargeEventID](testIDs), SubscriptionID: sub, FixedChargeID: fc,
			Units: money.NewFromInt(units), Timestamp: at(h),
		}
	}
	morning, evening, nextDay, otherCharge := ev(fc, 2, 1), ev(fc, 20, 2), ev(fc, 26, 3), ev(other, 3, 9)
	// 23:30 UTC on the previous day is 00:30 in Paris, the same local day
	// as morning.
	lateUTC := ev(fc, -0.5, 7)
	got := KeepLatestPerDay([]*FixedChargeEvent{evening, morning, nextDay, otherCharge}, time.UTC)
	if want := []*FixedChargeEvent{otherCharge, evening, nextDay}; !slices.Equal(got, want) {
		t.Errorf("UTC: got = %v, want = %v", got, want)
	}
	got = KeepLatestPerDay([]*FixedChargeEvent{lateUTC, morning}, paris)
	if want := []*FixedChargeEvent{morning}; !slices.Equal(got, want) {
		t.Errorf("Paris: got = %v, want = %v", got, want)
	}
	got = KeepLatestPerDay([]*FixedChargeEvent{lateUTC, morning}, time.UTC)
	if want := []*FixedChargeEvent{lateUTC, morning}; !slices.Equal(got, want) {
		t.Errorf("UTC across midnight: got = %v, want = %v", got, want)
	}
}

func TestUnitsAt(t *testing.T) {
	nextPeriod := func(time.Time) time.Time { return at(240) }
	immediate := &FixedChargeEvent{Units: money.NewFromInt(5), Timestamp: at(10), ApplyUnitsImmediately: true}
	deferred := &FixedChargeEvent{Units: money.NewFromInt(8), Timestamp: at(20)}
	events := []*FixedChargeEvent{immediate, deferred}
	cases := []struct {
		at    float64
		units int64
		ok    bool
	}{{0, 0, false}, {10, 5, true}, {100, 5, true}, {240, 8, true}}
	for _, c := range cases {
		u, ok := UnitsAt(events, at(c.at), nextPeriod)
		if ok != c.ok || (ok && !u.Equal(money.NewFromInt(c.units))) {
			t.Errorf("UnitsAt(%v): got = %v %v, want = %d %v", c.at, u, ok, c.units, c.ok)
		}
	}
}
