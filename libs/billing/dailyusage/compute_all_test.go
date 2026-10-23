package dailyusage_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

func TestComputeAll(t *testing.T) {
	// source: spec/services/daily_usages/compute_all_service_spec.rb
	base := billingtest.Date(2024, 10, 22, 0, 5)
	type plan int
	const (
		noCharge plan = iota
		prorated
		recurring
		weighted
		plain
		deletedRecurring
		proratedRecurring
	)
	tests := []struct {
		name     string
		source   string
		at       time.Time
		custTZ   string
		entityTZ string
		last     func(at time.Time) *core.Date
		skip     bool
		plan     plan
		stored   bool
		jitter   time.Duration
		want     bool
		window   time.Duration
	}{
		{name: "an event yesterday enqueues", source: ":22", want: true},
		{name: "the configured jitter", source: ":30", jitter: time.Minute, want: true, window: time.Minute},
		{name: "a negative jitter falls back to the default", source: ":42", jitter: -100 * time.Second, want: true},
		{name: "a zero jitter falls back to the default", source: ":54", jitter: 0, want: true},
		{name: "an already computed day is skipped", source: ":90", stored: true, want: true},
		{name: "billing entity time zone before its midnight", source: ":101", entityTZ: "America/Sao_Paulo"},
		{name: "billing entity time zone at its midnight", source: ":111", entityTZ: "America/Sao_Paulo", at: billingtest.Date(2024, 10, 22, 3, 5), want: true},
		{name: "customer time zone before its midnight", source: ":122", custTZ: "America/Sao_Paulo"},
		{name: "customer time zone at its midnight", source: ":131", custTZ: "America/Sao_Paulo", at: billingtest.Date(2024, 10, 22, 3, 5), want: true},
		{name: "no event ever", source: ":139", last: func(time.Time) *core.Date { return nil }},
		{name: "an event today", source: ":148", last: func(at time.Time) *core.Date { return datePtr(core.DateOf(at, time.UTC)) }, want: true},
		{name: "a stale event", source: ":157", last: stale},
		{name: "a prorated charge without recent events", source: ":176", last: stale, plan: prorated, want: true},
		{name: "a recurring metric after the rollover", source: ":191", at: billingtest.Date(2024, 10, 2, 0, 5), last: stale, plan: recurring, want: true},
		{name: "a recurring metric off the rollover", source: ":201", last: stale, plan: recurring},
		{name: "a weighted sum metric without recent events", source: ":213", last: stale, plan: weighted, want: true},
		{name: "a charge that is not time-dependent", source: ":224", last: stale, plan: plain},
		{name: "a deleted recurring charge", source: ":233", last: stale, plan: deletedRecurring},
		{name: "a recurring metric with a recent event off the rollover", source: ":253", last: func(at time.Time) *core.Date { return datePtr(core.DateOf(at, time.UTC)) }, plan: recurring, want: true},
		{name: "both time-dependent and recurring legs", source: ":269", at: billingtest.Date(2024, 10, 2, 0, 5), last: stale, plan: proratedRecurring, want: true},
		{name: "skip_daily_usage", source: ":288", last: func(at time.Time) *core.Date { return datePtr(core.DateOf(at, time.UTC)) }, skip: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "spec/services/daily_usages/compute_all_service_spec.rb" + tt.source
			at := tt.at
			if at.IsZero() {
				at = base
			}
			var custOpts []func(*customer.Customer)
			if tt.custTZ != "" {
				custOpts = append(custOpts, func(c *customer.Customer) { c.Timezone = paris(t, tt.custTZ) })
			}
			f := newFixture(t, at, custOpts...)
			f.Deps.DailyUsageJitter = tt.jitter
			if tt.entityTZ != "" {
				f.UpdateBillingEntity(func(be *organization.BillingEntity) { be.Timezone = paris(t, tt.entityTZ) })
			}
			last := datePtr(core.DateOf(at, time.UTC).AddDays(-1))
			if tt.last != nil {
				last = tt.last(at)
			}
			p := f.plan
			if tt.plan != noCharge {
				p = f.Plan(f.plan.Interval, 0)
				m := f.Metric("storage", catalog.AggregationTypeSum, "value", func(m *catalog.BillableMetric) {
					m.Recurring = tt.plan == recurring || tt.plan == deletedRecurring || tt.plan == proratedRecurring || tt.plan == prorated
					if tt.plan == weighted {
						m.AggregationType, m.Recurring, m.WeightedInterval = catalog.AggregationTypeWeightedSum, false, catalog.WeightedIntervalSeconds
					}
					if tt.plan == plain {
						m.Recurring = false
					}
				})
				c := f.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) {
					c.Prorated = tt.plan == prorated || tt.plan == proratedRecurring
				})
				if tt.plan == deletedRecurring {
					f.Do(func(ctx context.Context, r port.Repos) error { return r.Charges.Discard(ctx, f.OrgID(), c.ID, f.Now()) })
				}
			}
			subs := make([]*subscription.Subscription, 3)
			for i := range subs {
				subs[i] = f.sub(p, at.AddDate(-1, 0, 0), func(s *subscription.Subscription) { s.SkipDailyUsage = tt.skip })
				f.receivedOn(subs[i], last)
			}
			if tt.stored {
				f.storeUsage(subs[0], core.DateOf(at, time.UTC).AddDays(-1))
			}

			n, err := f.svc.ComputeAll(f.Ctx, f.OrgID(), at)
			if err != nil {
				t.Fatalf("%s: ComputeAll() error = %v", src, err)
			}
			got := f.computeArgs()
			wantN := 0
			window := tt.window
			if window == 0 {
				window = kernel.DefaultDailyUsageJitter
			}
			for i, s := range subs {
				j, ok := got[s.ID]
				want := tt.want && (!tt.stored || i != 0)
				if ok != want {
					t.Errorf("%s: subscription %d enqueued: got = %v, want = %v", src, i, ok, want)
				}
				if !ok {
					continue
				}
				wantN++
				var a kernel.DailyUsageArgs
				if err := json.Unmarshal(j.Args, &a); err != nil {
					t.Fatal(err)
				}
				if !a.Timestamp.Equal(at) {
					t.Errorf("%s: timestamp: got = %v, want = %v", src, a.Timestamp, at)
				}
				if j.RunAt.Before(f.Now()) || !j.RunAt.Before(f.Now().Add(window)) {
					t.Errorf("%s: run at: got = %v, want = in [%v, %v)", src, j.RunAt, f.Now(), f.Now().Add(window))
				}
				loc := f.cust.Timezone
				if loc == nil {
					loc = f.BillingEntity.Timezone
				}
				if loc == nil {
					loc = time.UTC
				}
				if want := kernel.DailyUsageKey(s.ID, core.DateOf(at, loc)); j.UniqueKey != want {
					t.Errorf("%s: unique key: got = %q, want = %q", src, j.UniqueKey, want)
				}
			}
			if n != wantN || len(got) != wantN {
				t.Errorf("%s: ComputeAll() = %d with %d jobs, want = %d", src, n, len(got), wantN)
			}
		})
	}
}

// stale returns a day five days before at.
func stale(at time.Time) *core.Date { return datePtr(core.DateOf(at, time.UTC).AddDays(-5)) }

func TestComputeAllOncePerDay(t *testing.T) {
	// source: spec/jobs/daily_usages/compute_job_spec.rb:58 (one job per
	// subscription and local day while one is pending or running)
	at := billingtest.Date(2024, 10, 22, 0, 5)
	f := newFixture(t, at)
	s := f.sub(f.plan, at.AddDate(-1, 0, 0))
	f.receivedOn(s, datePtr(core.DateOf(at, time.UTC)))
	for _, tick := range []time.Time{at, at.Add(time.Hour), at.Add(2 * time.Hour)} {
		if _, err := f.svc.ComputeAll(f.Ctx, f.OrgID(), tick); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(f.jobs(kernel.JobComputeDailyUsage)); got != 1 {
		t.Errorf("compute jobs of %s over one local day: got = %d, want = 1", s.ID, got)
	}
}

func TestDailyUsageKey(t *testing.T) {
	// source: spec/jobs/daily_usages/compute_job_spec.rb:80-151
	ny, paris := paris(t, "America/New_York"), paris(t, "Europe/Paris")
	sub := core.NewID[core.SubscriptionID](&billingtest.IDs{})
	tests := []struct {
		source string
		at     time.Time
		loc    *time.Location
		want   core.Date
	}{
		{":84", billingtest.Date(2024, 1, 15, 10), paris, core.NewDate(2024, 1, 15)},
		{":92", billingtest.Date(2024, 1, 15, 23), paris, core.NewDate(2024, 1, 16)},
		{":92", billingtest.Date(2024, 1, 16, 0), paris, core.NewDate(2024, 1, 16)},
		{":104", billingtest.Date(2024, 1, 15, 0), paris, core.NewDate(2024, 1, 15)},
		{":129", billingtest.Date(2024, 1, 15, 3), ny, core.NewDate(2024, 1, 14)},
		{":141", billingtest.Date(2024, 1, 15, 23), time.UTC, core.NewDate(2024, 1, 15)},
	}
	for _, tt := range tests {
		got := kernel.DailyUsageKey(sub, core.DateOf(tt.at, tt.loc))
		if want := "daily_usage:" + sub.String() + ":" + tt.want.String(); got != want {
			t.Errorf("spec/jobs/daily_usages/compute_job_spec.rb%s: DailyUsageKey() = %q, want = %q", tt.source, got, want)
		}
	}
}
