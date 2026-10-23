package dailyusage_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

const fillHistorySpec = "spec/services/daily_usages/fill_history_service_spec.rb"

type metricKind int

const (
	countMetric metricKind = iota
	recurringSumMetric
	weightedSumMetric
)

// historyFixture is the setup of fill_history_service_spec.rb: a calendar
// subscription started 2024-10-01 to a plan with one standard charge.
type historyFixture struct {
	*fixture
	metric *catalog.BillableMetric
	sub    *subscription.Subscription
}

func newHistoryFixture(t *testing.T, now time.Time, kind metricKind, amount string, chargeOpts ...func(*catalog.Charge)) *historyFixture {
	t.Helper()
	f := &historyFixture{fixture: newFixture(t, now)}
	switch kind {
	case countMetric:
		f.metric = f.Metric("api_calls", catalog.AggregationTypeCount, "")
	case recurringSumMetric:
		f.metric = f.Metric("seats", catalog.AggregationTypeSum, "item_id", func(m *catalog.BillableMetric) { m.Recurring = true })
	case weightedSumMetric:
		f.metric = f.Metric("storage", catalog.AggregationTypeWeightedSum, "value", func(m *catalog.BillableMetric) {
			m.WeightedInterval = catalog.WeightedIntervalSeconds
		})
	}
	f.Charge(f.plan, f.metric, catalog.ChargeModelStandard, `{"amount":"`+amount+`"}`, chargeOpts...)
	f.sub = f.fixture.sub(f.plan, billingtest.Date(2024, 10, 1))
	return f
}

// event ingests an event of the metric at ts.
func (f *historyFixture) event(ts time.Time) {
	f.Event(f.sub, f.metric.Code, ts, "item_id", "1", "value", "1")
}

func (f *historyFixture) fill(from core.Date, to *core.Date) {
	f.T.Helper()
	if err := f.svc.FillHistory(f.Ctx, f.OrgID(), kernel.FillDailyUsageHistoryArgs{SubscriptionID: f.sub.ID, From: from, To: to}); err != nil {
		f.T.Fatalf("FillHistory() error = %v", err)
	}
}

// days returns the stored usage days of the subscription, in order.
func (f *historyFixture) days() []core.Date {
	f.T.Helper()
	var out []core.Date
	for _, du := range f.usages(f.sub) {
		out = append(out, du.UsageDate)
	}
	slices.SortFunc(out, func(a, b core.Date) int { return a.Compare(b) })
	return out
}

func TestFillHistory(t *testing.T) {
	d := core.NewDate
	tests := []struct {
		source   string
		now      time.Time
		kind     metricKind
		amount   string
		prorated bool
		events   []time.Time
		from, to core.Date
		want     []core.Date
	}{
		{
			source: ":30", now: billingtest.Date(2024, 10, 16, 12), amount: "0", events: []time.Time{billingtest.Date(2024, 10, 15, 10)},
			from: d(2024, 10, 15), to: d(2024, 10, 15), want: []core.Date{d(2024, 10, 15)},
		},
		{source: ":60", now: billingtest.Date(2024, 10, 16, 12), amount: "10", from: d(2024, 10, 15), to: d(2024, 10, 15)},
		{
			source: ":70", now: billingtest.Date(2024, 10, 17, 12), amount: "1",
			events: []time.Time{billingtest.Date(2024, 10, 14, 10), billingtest.Date(2024, 10, 16, 10)},
			from:   d(2024, 10, 14), to: d(2024, 10, 16), want: []core.Date{d(2024, 10, 14), d(2024, 10, 16)},
		},
		{source: ":104", now: billingtest.Date(2024, 10, 16, 12), amount: "1", from: d(2024, 10, 15), to: d(2024, 10, 15)},
		{
			source: ":114", now: billingtest.Date(2024, 10, 16, 12), kind: recurringSumMetric, amount: "1", prorated: true,
			events: []time.Time{billingtest.Date(2024, 10, 14, 10)},
			from:   d(2024, 10, 14), to: d(2024, 10, 15), want: []core.Date{d(2024, 10, 14), d(2024, 10, 15)},
		},
		{
			source: ":147", now: billingtest.Date(2024, 10, 16, 12), kind: recurringSumMetric, amount: "1",
			events: []time.Time{billingtest.Date(2024, 10, 14, 10)},
			from:   d(2024, 10, 14), to: d(2024, 10, 15), want: []core.Date{d(2024, 10, 14)},
		},
		{
			source: ":172", now: billingtest.Date(2024, 11, 3, 12), kind: recurringSumMetric, amount: "1",
			events: []time.Time{billingtest.Date(2024, 10, 30, 10)},
			from:   d(2024, 10, 30), to: d(2024, 11, 2), want: []core.Date{d(2024, 10, 30), d(2024, 11, 1)},
		},
		{
			source: ":199", now: billingtest.Date(2024, 10, 16, 12), kind: weightedSumMetric, amount: "1",
			events: []time.Time{billingtest.Date(2024, 10, 14, 10)},
			from:   d(2024, 10, 14), to: d(2024, 10, 15), want: []core.Date{d(2024, 10, 14), d(2024, 10, 15)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			src := fillHistorySpec + tt.source
			f := newHistoryFixture(t, tt.now, tt.kind, tt.amount, func(c *catalog.Charge) { c.Prorated = tt.prorated })
			for _, e := range tt.events {
				f.event(e)
			}
			f.fill(tt.from, &tt.to)
			if got := f.days(); !slices.Equal(got, tt.want) {
				t.Errorf("%s: usage days: got = %v, want = %v", src, got, tt.want)
			}
		})
	}
}

func TestFillHistoryFreeCharge(t *testing.T) {
	// source: spec/services/daily_usages/fill_history_service_spec.rb:30
	f := newHistoryFixture(t, billingtest.Date(2024, 10, 16, 12), countMetric, "0")
	f.event(billingtest.Date(2024, 10, 15, 10))
	day := core.NewDate(2024, 10, 15)
	f.fill(day, &day)
	rows := f.usages(f.sub)
	if len(rows) != 1 {
		t.Fatalf("%s:30: rows: got = %d, want = 1", fillHistorySpec, len(rows))
	}
	du := rows[0]
	if du.CustomerID != f.cust.ID || du.SubscriptionID != f.sub.ID || du.UsageDate != day || du.Usage.Amount != 0 || len(du.Usage.Charges) != 1 {
		t.Errorf("%s:30: row: got = %v %v %s %d over %d charges, want = %v %v %s 0 over 1", fillHistorySpec,
			du.CustomerID, du.SubscriptionID, du.UsageDate, du.Usage.Amount, len(du.Usage.Charges), f.cust.ID, f.sub.ID, day)
	}
	// The first day of the range has an empty diff
	// (fill_history_service.rb:73-79); it is refreshed at its midnight.
	if du.UsageDiff != nil {
		t.Errorf("%s:30: usage_diff of the first day: got = %v, want = nil", fillHistorySpec, du.UsageDiff)
	}
	if want := billingtest.Date(2024, 10, 15); !du.RefreshedAt.Equal(want) {
		t.Errorf("%s:30: refreshed_at: got = %v, want = %v", fillHistorySpec, du.RefreshedAt, want)
	}
}

func TestFillHistoryExistingBaseline(t *testing.T) {
	// source: spec/services/daily_usages/fill_history_service_spec.rb:227-279
	f := newHistoryFixture(t, billingtest.Date(2024, 10, 17, 12), countMetric, "1")
	f.event(billingtest.Date(2024, 10, 14, 10))
	f.event(billingtest.Date(2024, 10, 16, 10))
	existing := f.storeUsage(f.sub, core.NewDate(2024, 10, 15), func(du *subscription.DailyUsage) {
		du.From, du.To = billingtest.Date(2024, 10, 1), billingtest.Date(2024, 11, 1)
	})
	from, to := core.NewDate(2024, 10, 14), core.NewDate(2024, 10, 16)
	f.fill(from, &to)

	rows := f.usages(f.sub)
	if len(rows) != 3 {
		t.Fatalf("%s:270: rows: got = %d, want = 3", fillHistorySpec, len(rows))
	}
	for _, du := range rows {
		switch du.UsageDate {
		case existing.UsageDate:
			if du.ID != existing.ID || len(du.Usage.Charges) != 0 {
				t.Errorf("%s:270: 2024-10-15: got = %s, want = the existing %s unchanged", fillHistorySpec, du.ID, existing.ID)
			}
		case to:
			// Against the existing empty usage, the diff is the whole
			// usage of the two events, not the one event since 10-14.
			if du.UsageDiff == nil || len(du.UsageDiff.Charges) != 1 || !du.UsageDiff.Charges[0].Units.Equal(money.NewFromInt(2)) {
				t.Errorf("%s:262: diff of 2024-10-16: got = %+v, want = 2 units against the existing usage", fillHistorySpec, du.UsageDiff)
			}
		}
	}
}

func TestFillHistoryRange(t *testing.T) {
	// source: spec/services/daily_usages/fill_history_service_spec.rb:283-313
	// (the range end), with a weighted sum metric so every day is stored
	d := core.NewDate
	terminated := billingtest.Date(2024, 10, 10, 12)
	to := d(2024, 10, 20)
	toFive := d(2024, 10, 5)
	tests := []struct {
		source    string
		to        *core.Date
		terminate bool
		wantLast  core.Date
	}{
		{source: ":291 terminated", to: &to, terminate: true, wantLast: d(2024, 10, 10)},
		{source: ":302 to_date", to: &toFive, wantLast: d(2024, 10, 5)},
		{source: ":308 yesterday", wantLast: d(2024, 10, 14)},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			src := fillHistorySpec + tt.source
			f := newHistoryFixture(t, billingtest.Date(2024, 10, 15, 12), weightedSumMetric, "1")
			f.event(billingtest.Date(2024, 10, 1, 1))
			var inv *invoice.Invoice
			if tt.terminate {
				f.UpdateSubscription(f.sub, func(s *subscription.Subscription) {
					s.Status, s.TerminatedAt = subscription.StatusTerminated, &terminated
				})
				inv = f.StoredInvoice(f.cust)
				f.LinkSubscription(inv, f.sub, invoice.InvoicingReasonSubscriptionTerminating, billingtest.Date(2024, 10, 1), terminated)
			}
			f.fill(d(2024, 10, 3), tt.to)
			days := f.days()
			if len(days) == 0 || days[0] != d(2024, 10, 3) || days[len(days)-1] != tt.wantLast {
				t.Fatalf("%s: usage days: got = %v, want = 2024-10-03 to %s", src, days, tt.wantLast)
			}
			jobs := f.jobs(kernel.JobFillDailyUsageFromInvoice)
			if !tt.terminate {
				if len(jobs) != 0 {
					t.Errorf("%s: fill from invoice jobs: got = %d, want = 0", src, len(jobs))
				}
				return
			}
			// The termination invoice then fills the last day
			// (fill_history_service.rb:88-96).
			if len(jobs) != 1 {
				t.Fatalf("%s: fill from invoice jobs: got = %d, want = 1", src, len(jobs))
			}
			var a kernel.FillDailyUsageFromInvoiceArgs
			if err := json.Unmarshal(jobs[0].Args, &a); err != nil {
				t.Fatal(err)
			}
			if a.InvoiceID != inv.ID || !slices.Equal(a.SubscriptionIDs, []core.SubscriptionID{f.sub.ID}) {
				t.Errorf("%s: fill from invoice args: got = %+v, want = %s for %s", src, a, inv.ID, f.sub.ID)
			}
		})
	}
}

func TestFillHistorySandbox(t *testing.T) {
	// source: tanvo (lago's sandbox mode, fill_history_service.rb:58-64,
	// computes without saving)
	f := newHistoryFixture(t, billingtest.Date(2024, 10, 17, 12), countMetric, "1")
	f.event(billingtest.Date(2024, 10, 14, 10))
	to := core.NewDate(2024, 10, 16)
	if err := f.svc.FillHistory(f.Ctx, f.OrgID(), kernel.FillDailyUsageHistoryArgs{SubscriptionID: f.sub.ID, From: core.NewDate(2024, 10, 14), To: &to, Sandbox: true}); err != nil {
		t.Fatal(err)
	}
	if got := len(f.usages(f.sub)); got != 0 {
		t.Errorf("sandbox rows: got = %d, want = 0", got)
	}
}
