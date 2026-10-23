package dailyusage_test

import (
	"encoding/json"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

const diffSpec = "spec/services/daily_usages/compute_diff_service_spec.rb"

func TestComputeDiffPreviousLookup(t *testing.T) {
	// source: spec/services/daily_usages/compute_diff_service_spec.rb:753
	// and :787 (the latest prior snapshot of the period, across days
	// without one)
	first := billingtest.Date(2024, 10, 17, 0, 5)
	f := newComputeFixture(t, first, first, "100", nil)
	f.event(first)
	if du, err := f.svc.Compute(f.Ctx, f.OrgID(), f.sub.ID, first); err != nil || du == nil {
		t.Fatalf("Compute(2024-10-17) = %v, %v, want = a row", du, err)
	}

	later := billingtest.Date(2024, 10, 22, 0, 5)
	f.Clock.Set(later)
	f.event(later)
	f.event(later)
	du, err := f.svc.Compute(f.Ctx, f.OrgID(), f.sub.ID, later)
	if err != nil || du == nil {
		t.Fatalf("Compute(2024-10-22) = %v, %v, want = a row", du, err)
	}
	if du.UsageDate != core.NewDate(2024, 10, 21) {
		t.Fatalf("usage_date: got = %s, want = 2024-10-21", du.UsageDate)
	}
	d := du.UsageDiff
	if d == nil || d.Amount != 20000 || len(d.Charges) != 1 || !d.Charges[0].Units.Equal(money.NewFromInt(2)) || d.Charges[0].EventsCount != 2 {
		t.Errorf("%s:787: diff against the 2024-10-16 snapshot: got = %+v, want = 2 units, 2 events, 20000", diffSpec, d)
	}
}

func TestComputeDiffOtherPeriod(t *testing.T) {
	// source: spec/services/daily_usages/compute_diff_service_spec.rb:818
	ts := billingtest.Date(2024, 10, 2, 0, 5)
	f := newComputeFixture(t, ts, ts, "100", nil)
	f.event(ts)
	f.storeUsage(f.sub, core.NewDate(2024, 9, 30), func(du *subscription.DailyUsage) {
		du.From, du.To = billingtest.Date(2024, 9, 1), billingtest.Date(2024, 10, 1)
		du.Usage = subscription.UsageSnapshot{From: du.From, To: du.To, Amount: 5000, TotalAmount: 5000, Charges: []subscription.ChargeUsage{{
			UsageValues: subscription.UsageValues{Units: money.NewFromInt(1), EventsCount: 1, Amount: 5000},
			Charge:      subscription.ChargeRef{ID: f.charge.ID},
		}}}
	})
	du, err := f.svc.Compute(f.Ctx, f.OrgID(), f.sub.ID, ts)
	if err != nil || du == nil {
		t.Fatalf("Compute() = %v, %v, want = a row", du, err)
	}
	got, _ := json.Marshal(du.UsageDiff)
	want, _ := json.Marshal(du.Usage)
	if string(got) != string(want) {
		t.Errorf("%s:818: diff across periods: got = %s, want = the whole usage %s", diffSpec, got, want)
	}
}

func TestFillFromInvoiceDiffBreakdowns(t *testing.T) {
	// source: spec/services/daily_usages/compute_diff_service_spec.rb:1369
	// (the looked-up previous snapshot's presentation breakdowns are
	// subtracted)
	f := newInvoiceFixture(t)
	f.link(f.sub, f.cp)
	us := "us"
	fee := f.chargeFee(f.sub, 3, 300, 0, func(fee *invoice.Fee) {
		fee.Units = money.MustParse("1.5")
		fee.PresentationBreakdowns = []invoice.PresentationBreakdown{{PresentationBy: map[string]*string{"region": &us}, Units: money.MustParse("1.5")}}
	})
	f.storeUsage(f.sub, core.NewDate(2025, 6, 25), func(du *subscription.DailyUsage) {
		du.From, du.To = f.cp.From, f.cp.To
		du.Usage = subscription.UsageSnapshot{From: du.From, To: du.To, Amount: 100, TotalAmount: 100, Charges: []subscription.ChargeUsage{{
			UsageValues:            subscription.UsageValues{Units: money.One, EventsCount: 1, Amount: 100},
			Charge:                 subscription.ChargeRef{ID: fee.ChargeID},
			PresentationBreakdowns: []subscription.PresentationBreakdownUsage{{PresentationBy: map[string]*string{"region": &us}, Units: money.One}},
		}}}
	})
	out := f.fill(f.sub)
	if len(out) != 1 || out[0].UsageDiff == nil {
		t.Fatalf("stored: got = %v, want = one row with a diff", out)
	}
	got, err := json.Marshal(out[0].UsageDiff.Charges[0].PresentationBreakdowns)
	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"presentation_by":{"region":"us"},"units":"0.5"}]`; string(got) != want {
		t.Errorf("%s:1369: presentation_breakdowns diff: got = %s, want = %s", diffSpec, got, want)
	}
}
