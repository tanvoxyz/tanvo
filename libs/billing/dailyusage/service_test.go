package dailyusage_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func job(t *testing.T, f *fixture, kind string, args any) port.Job {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return port.Job{OrganizationID: f.OrgID(), Kind: kind, Args: raw}
}

func TestHandleJob(t *testing.T) {
	// source: spec/jobs/clock/compute_all_daily_usages_job_spec.rb:26,
	// spec/jobs/daily_usages/compute_job_spec.rb:14,
	// spec/jobs/daily_usages/fill_from_invoice_job_spec.rb:14 and
	// spec/jobs/daily_usages/fill_history_job_spec.rb:17 (each job runs its
	// service)
	at := billingtest.Date(2024, 10, 22, 0, 5)

	t.Run("compute all", func(t *testing.T) {
		f := newComputeFixture(t, at, at, "100", nil)
		f.receivedOn(f.sub, datePtr(core.DateOf(at, time.UTC)))
		ok, err := f.svc.HandleJob(f.Ctx, job(t, f.fixture, kernel.JobComputeDailyUsages, kernel.InstantArgs{At: at}))
		if !ok || err != nil {
			t.Fatalf("HandleJob() = %v, %v, want = true, nil", ok, err)
		}
		if got := len(f.jobs(kernel.JobComputeDailyUsage)); got != 1 {
			t.Errorf("compute jobs: got = %d, want = 1", got)
		}
		_, err = f.svc.HandleJob(f.Ctx, job(t, f.fixture, kernel.JobComputeDailyUsages, kernel.InstantArgs{}))
		if !errors.Is(err, core.ErrInvalidValue) {
			t.Errorf("HandleJob() without an instant: got = %v, want = %v", err, core.ErrInvalidValue)
		}
	})

	t.Run("compute", func(t *testing.T) {
		f := newComputeFixture(t, at, at, "100", nil)
		f.event(at)
		ok, err := f.svc.HandleJob(f.Ctx, job(t, f.fixture, kernel.JobComputeDailyUsage, kernel.DailyUsageArgs{SubscriptionID: f.sub.ID, Timestamp: at}))
		if !ok || err != nil || len(f.usages(f.sub)) != 1 {
			t.Errorf("HandleJob() = %v, %v with %d rows, want = true, nil with 1", ok, err, len(f.usages(f.sub)))
		}
	})

	t.Run("fill from invoice", func(t *testing.T) {
		f := newInvoiceFixture(t)
		f.link(f.sub, f.cp)
		f.chargeFee(f.sub, 12, 1200, 0)
		args := kernel.FillDailyUsageFromInvoiceArgs{InvoiceID: f.inv.ID, SubscriptionIDs: []core.SubscriptionID{f.sub.ID}}
		ok, err := f.svc.HandleJob(f.Ctx, job(t, f.fixture, kernel.JobFillDailyUsageFromInvoice, args))
		if !ok || err != nil || len(f.usages(f.sub)) != 1 {
			t.Errorf("HandleJob() = %v, %v with %d rows, want = true, nil with 1", ok, err, len(f.usages(f.sub)))
		}
	})

	t.Run("fill history", func(t *testing.T) {
		f := newHistoryFixture(t, billingtest.Date(2024, 10, 16, 12), countMetric, "1")
		f.event(billingtest.Date(2024, 10, 15, 10))
		args := kernel.FillDailyUsageHistoryArgs{SubscriptionID: f.sub.ID, From: core.NewDate(2024, 10, 1)}
		ok, err := f.svc.HandleJob(f.Ctx, job(t, f.fixture, kernel.JobFillDailyUsageHistory, args))
		if !ok || err != nil || len(f.usages(f.sub)) != 1 {
			t.Errorf("HandleJob() = %v, %v with %d rows, want = true, nil with 1", ok, err, len(f.usages(f.sub)))
		}
	})

	t.Run("other kinds", func(t *testing.T) {
		f := newFixture(t, at)
		if ok, err := f.svc.HandleJob(f.Ctx, port.Job{Kind: "other"}); ok || err != nil {
			t.Errorf("HandleJob(other) = %v, %v, want = false, nil", ok, err)
		}
	})
}

func TestEnqueueFillHistory(t *testing.T) {
	// source: lib/tasks/daily_usages.rake:20-25 (the subscriptions to fill)
	now := billingtest.Date(2024, 10, 20, 12)
	f := newFixture(t, now)
	active := f.sub(f.plan, billingtest.Date(2024, 9, 1))
	early := billingtest.Date(2024, 9, 15)
	late := billingtest.Date(2024, 10, 5)
	terminatedBefore := f.sub(f.plan, billingtest.Date(2024, 9, 1))
	f.UpdateSubscription(terminatedBefore, terminate(early))
	terminatedAfter := f.sub(f.plan, billingtest.Date(2024, 9, 1))
	f.UpdateSubscription(terminatedAfter, terminate(late))
	f.sub(f.plan, billingtest.Date(2024, 11, 1)) // pending

	from := core.NewDate(2024, 10, 1)
	n, err := f.svc.EnqueueFillHistory(f.Ctx, f.OrgID(), from, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[core.SubscriptionID]bool{}
	for _, j := range f.jobs(kernel.JobFillDailyUsageHistory) {
		var a kernel.FillDailyUsageHistoryArgs
		if err := json.Unmarshal(j.Args, &a); err != nil {
			t.Fatal(err)
		}
		if a.From != from || a.To != nil {
			t.Errorf("args: got = %+v, want = from %s", a, from)
		}
		got[a.SubscriptionID] = true
	}
	if n != 2 || len(got) != 2 || !got[active.ID] || !got[terminatedAfter.ID] {
		t.Errorf("EnqueueFillHistory() = %d for %v, want = 2 for %s and %s", n, got, active.ID, terminatedAfter.ID)
	}

	f.Store.TakeJobs()
	if n, err := f.svc.EnqueueFillHistory(f.Ctx, f.OrgID(), from, []core.SubscriptionID{active.ID}); err != nil || n != 1 {
		t.Errorf("EnqueueFillHistory(ids) = %d, %v, want = 1, nil", n, err)
	}
}

func TestRebuildHistory(t *testing.T) {
	// source: lib/tasks/daily_usages.rake:52-71 (the deletion before the
	// fills, scoped to the subscriptions and the days from from_date on)
	now := billingtest.Date(2024, 10, 20, 12)
	from := core.NewDate(2024, 10, 1)
	tests := []struct {
		name         string
		args         func(a, b core.SubscriptionID) kernel.RebuildDailyUsageHistoryArgs
		deleted      int
		enqueued     int
		keptA, keptB int
	}{
		{
			name: "delete existing of one subscription",
			args: func(a, _ core.SubscriptionID) kernel.RebuildDailyUsageHistoryArgs {
				return kernel.RebuildDailyUsageHistoryArgs{From: from, SubscriptionIDs: []core.SubscriptionID{a}, DeleteExisting: true}
			},
			deleted: 2, enqueued: 1, keptA: 1, keptB: 1,
		},
		{
			name: "delete existing of every subscription",
			args: func(_, _ core.SubscriptionID) kernel.RebuildDailyUsageHistoryArgs {
				return kernel.RebuildDailyUsageHistoryArgs{From: from, DeleteExisting: true}
			},
			deleted: 3, enqueued: 2, keptA: 1, keptB: 0,
		},
		{
			name: "keep existing",
			args: func(_, _ core.SubscriptionID) kernel.RebuildDailyUsageHistoryArgs {
				return kernel.RebuildDailyUsageHistoryArgs{From: from}
			},
			deleted: 0, enqueued: 2, keptA: 3, keptB: 1,
		},
		{
			// The default window, 120 days back, covers every row.
			name: "default from",
			args: func(_, _ core.SubscriptionID) kernel.RebuildDailyUsageHistoryArgs {
				return kernel.RebuildDailyUsageHistoryArgs{DeleteExisting: true}
			},
			deleted: 4, enqueued: 2, keptA: 0, keptB: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, now)
			a := f.sub(f.plan, billingtest.Date(2024, 9, 1))
			b := f.sub(f.plan, billingtest.Date(2024, 9, 1))
			for _, d := range []core.Date{core.NewDate(2024, 9, 25), core.NewDate(2024, 10, 2), core.NewDate(2024, 10, 10)} {
				f.storeUsage(a, d)
			}
			f.storeUsage(b, core.NewDate(2024, 10, 5))

			deleted, enqueued, err := f.svc.RebuildHistory(f.Ctx, f.OrgID(), tc.args(a.ID, b.ID))
			if err != nil {
				t.Fatalf("RebuildHistory() error got = %v, want = nil", err)
			}
			if deleted != tc.deleted || enqueued != tc.enqueued {
				t.Errorf("RebuildHistory() got = (%d, %d), want = (%d, %d)", deleted, enqueued, tc.deleted, tc.enqueued)
			}
			if got := len(f.usages(a)); got != tc.keptA {
				t.Errorf("daily usages of a got = %d, want = %d", got, tc.keptA)
			}
			if got := len(f.usages(b)); got != tc.keptB {
				t.Errorf("daily usages of b got = %d, want = %d", got, tc.keptB)
			}
			if got := len(f.jobs(kernel.JobFillDailyUsageHistory)); got != tc.enqueued {
				t.Errorf("fill jobs got = %d, want = %d", got, tc.enqueued)
			}
		})
	}

	t.Run("job", func(t *testing.T) {
		f := newFixture(t, now)
		a := f.sub(f.plan, billingtest.Date(2024, 9, 1))
		f.storeUsage(a, core.NewDate(2024, 10, 2))
		args := kernel.RebuildDailyUsageHistoryArgs{From: from, DeleteExisting: true}
		ok, err := f.svc.HandleJob(f.Ctx, job(t, f, kernel.JobRebuildDailyUsageHistory, args))
		if !ok || err != nil || len(f.usages(a)) != 0 || len(f.jobs(kernel.JobFillDailyUsageHistory)) != 1 {
			t.Errorf("HandleJob() got = (%v, %v) with %d rows, want = (true, nil) with 0 and one fill", ok, err, len(f.usages(a)))
		}
	})
}
