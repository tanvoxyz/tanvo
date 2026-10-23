package lifecycle_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/period"
)

func TestActivatePending(t *testing.T) {
	tests := []struct {
		name   string
		source string
		subAt  time.Time
		tz     string
		plan   []func(*catalog.Plan)
		fixed  bool
		want   bool
		bills  int
	}{
		{name: "later today", source: "spec/services/subscriptions/activate_all_pending_service_spec.rb:11", subAt: now.Add(3 * time.Hour), want: true},
		{name: "tomorrow", source: "spec/services/subscriptions/activate_all_pending_service_spec.rb:11", subAt: now.Add(24 * time.Hour)},
		{
			name: "in-advance plan bills the start", source: "spec/services/subscriptions/activate_all_pending_service_spec.rb:45",
			subAt: now.Add(time.Hour), plan: []func(*catalog.Plan){inAdvance}, want: true, bills: 1,
		},
		{
			name: "in-arrears plan with in-advance fixed charges", source: "spec/services/subscriptions/activate_all_pending_service_spec.rb:92",
			subAt: now.Add(time.Hour), fixed: true, want: true, bills: 1,
		},
		{
			name: "next UTC day is today in the customer's time zone", source: "spec/services/subscriptions/activate_all_pending_service_spec.rb:124",
			subAt: billingtest.Date(2026, time.March, 11, 2, 0), tz: "America/New_York", want: true,
		},
		{
			name: "next UTC day without time zone", source: "spec/services/subscriptions/activate_all_pending_service_spec.rb:124",
			subAt: billingtest.Date(2026, time.March, 11, 2, 0),
		},
		{
			name: "in trial with in-advance fixed charges", source: "spec/services/subscriptions/activate_all_pending_service_spec.rb:152",
			subAt: now.Add(time.Hour), plan: []func(*catalog.Plan){inAdvance, trial(5)}, fixed: true, want: true, bills: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, now)
			p := f.Plan(period.Monthly, 1000, tc.plan...)
			if tc.fixed {
				f.inAdvanceFixed(p, true)
			}
			c := f.Customer(func(c *customer.Customer) {
				if tc.tz != "" {
					loc, err := time.LoadLocation(tc.tz)
					must(t, err)
					c.Timezone = loc
				}
			})
			sub := f.Subscription(c, p, billingtest.SubscribedAt(tc.subAt))
			n, err := f.svc.ActivatePending(f.Ctx, f.OrgID(), now)
			must(t, err)
			check(t, "activated", n == 1, tc.want)
			check(t, "active", f.reload(sub.ID).Status == subscription.StatusActive, tc.want)
			check(t, "bills", len(f.biller.reqs), tc.bills)
		})
	}
}

// source: app/jobs/clock/terminate_ended_subscriptions_job.rb
func TestTerminateEnded(t *testing.T) {
	f := newFixture(t, now)
	p := f.Plan(period.Monthly, 1000)
	endingToday := now.Add(6 * time.Hour)
	endedEarlier := now.Add(-time.Hour)
	tomorrow := now.Add(24 * time.Hour)
	later := f.Subscription(f.Customer(), p, billingtest.SubscribedAt(now.AddDate(0, -1, 0)), func(s *subscription.Subscription) { s.EndingAt = &endingToday })
	earlier := f.Subscription(f.Customer(), p, billingtest.SubscribedAt(now.AddDate(0, -1, 0)), func(s *subscription.Subscription) { s.EndingAt = &endedEarlier })
	kept := f.Subscription(f.Customer(), p, billingtest.SubscribedAt(now.AddDate(0, -1, 0)), func(s *subscription.Subscription) { s.EndingAt = &tomorrow })
	n, err := f.svc.TerminateEnded(f.Ctx, f.OrgID(), now)
	must(t, err)
	check(t, "terminated", n, 2)
	check(t, "later terminated_at", stamp(*f.reload(later.ID).TerminatedAt), stamp(now))
	check(t, "earlier terminated_at", stamp(*f.reload(earlier.ID).TerminatedAt), stamp(endedEarlier))
	check(t, "kept status", f.reload(kept.ID).Status, subscription.StatusActive)
	check(t, "bills", len(f.biller.reqs), 2)
}

// A subscription started after its ending_at, on the day of a late sweep
// tick, is terminated at its start, not before it: the termination
// instant is clamped to started_at (lago terminates at Time.current, which
// is never before the start).
//
// source: app/jobs/clock/terminate_ended_subscriptions_job.rb:10
func TestTerminateEndedNotBeforeStart(t *testing.T) {
	f := newFixture(t, now)
	p := f.Plan(period.Monthly, 1000)
	ending := now.Add(-2 * time.Hour)
	started := now.Add(-time.Hour)
	sub := f.Subscription(f.Customer(), p, billingtest.SubscribedAt(started), func(s *subscription.Subscription) { s.EndingAt = &ending })
	n, err := f.svc.TerminateEnded(f.Ctx, f.OrgID(), now)
	must(t, err)
	check(t, "terminated", n, 1)
	check(t, "terminated_at", stamp(*f.reload(sub.ID).TerminatedAt), stamp(started))
}

func TestEndTrials(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		plan      []func(*catalog.Plan)
		started   time.Time
		billed    bool // a periodic invoice was issued today
		wantEnded bool
		want      []string
	}{
		{
			name: "plan without trial", source: "spec/services/subscriptions/free_trial_billing_service_spec.rb:13",
			started: now.AddDate(0, 0, -20),
		},
		{
			name: "trial not ended", source: "spec/services/subscriptions/free_trial_billing_service_spec.rb:23",
			plan: []func(*catalog.Plan){trial(30)}, started: now.AddDate(0, 0, -20),
		},
		{
			name: "in-arrears plan", source: "spec/services/subscriptions/free_trial_billing_service_spec.rb:76",
			plan: []func(*catalog.Plan){trial(10)}, started: now.AddDate(0, 0, -10), wantEnded: true,
		},
		{
			name: "in-advance plan bills the rest of the period", source: "spec/services/subscriptions/free_trial_billing_service_spec.rb:110",
			plan: []func(*catalog.Plan){inAdvance, trial(10)}, started: now.AddDate(0, 0, -10), wantEnded: true,
			want: []string{"2026-03-10 12:00:00 skip sub:subscription_starting"},
		},
		{
			name: "in-advance plan already billed today", source: "app/services/subscriptions/free_trial_billing_service.rb:16",
			plan: []func(*catalog.Plan){inAdvance, trial(10)}, started: now.AddDate(0, 0, -10), billed: true, wantEnded: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, now)
			c := f.Customer()
			sub := f.name(f.Subscription(c, f.Plan(period.Monthly, 1000, tc.plan...), billingtest.SubscribedAt(tc.started)), "sub")
			if tc.billed {
				f.LinkSubscription(f.StoredInvoice(c), sub, invoice.InvoicingReasonSubscriptionPeriodic, now.Add(-time.Hour), now)
			}
			n, err := f.svc.EndTrials(f.Ctx, f.OrgID(), now)
			must(t, err)
			got := f.reload(sub.ID)
			check(t, "ended", n == 1, tc.wantEnded)
			check(t, "trial_ended_at set", got.TrialEndedAt != nil, tc.wantEnded)
			if tc.wantEnded {
				check(t, "trial_ended_at", stamp(*got.TrialEndedAt), stamp(tc.started.AddDate(0, 0, 10)))
				checkSlice(t, "webhooks", f.Webhooks(), hooks(webhook.EventTypeSubscriptionTrialEnded))
			}
			checkSlice(t, "bills", f.bills(), tc.want)
		})
	}
}

// source: spec/services/subscriptions/free_trial_billing_service_spec.rb:41
func TestEndTrialsAcrossChain(t *testing.T) {
	f := newFixture(t, now)
	c := f.Customer()
	old := f.Subscription(c, f.Plan(period.Monthly, 1000, trial(10)), billingtest.SubscribedAt(now.AddDate(0, 0, -12)))
	f.UpdateSubscription(old, func(s *subscription.Subscription) { must(t, s.Terminate(now.AddDate(0, 0, -5))) })
	sub := f.Subscription(c, f.Plan(period.Monthly, 2000, trial(10)), billingtest.SubscribedAt(now.AddDate(0, 0, -5)), func(s *subscription.Subscription) {
		s.ExternalID, s.PreviousSubscriptionID = old.ExternalID, old.ID
	})
	n, err := f.svc.EndTrials(f.Ctx, f.OrgID(), now)
	must(t, err)
	check(t, "ended", n, 1)
	check(t, "trial_ended_at", stamp(*f.reload(sub.ID).TrialEndedAt), stamp(now.AddDate(0, 0, -2)))
}

// A periodic invoice issued earlier on the local day counts even when a
// later invoice (here progressive billing) is the subscription's last.
//
// source: app/services/subscriptions/free_trial_billing_service.rb:101 (already_billed_today)
func TestEndTrialsBilledTodayBeforeAnotherInvoice(t *testing.T) {
	f := newFixture(t, now)
	c := f.Customer()
	started := now.AddDate(0, 0, -10)
	sub := f.Subscription(c, f.Plan(period.Monthly, 1000, inAdvance, trial(10)), billingtest.SubscribedAt(started))
	f.LinkSubscription(f.StoredInvoice(c), sub, invoice.InvoicingReasonSubscriptionPeriodic, now.Add(-2*time.Hour), now.Add(-time.Hour))
	f.LinkSubscription(f.StoredInvoice(c), sub, invoice.InvoicingReasonProgressiveBilling, now.Add(-time.Hour), now)
	n, err := f.svc.EndTrials(f.Ctx, f.OrgID(), now)
	must(t, err)
	check(t, "ended", n, 1)
	checkSlice(t, "bills", f.bills(), nil)
}

// A dispatched sweep job without the clock tick is refused, never run at
// the worker's clock.
func TestHandleJobRequiresInstant(t *testing.T) {
	f := newFixture(t, now)
	for _, kind := range []string{kernel.JobActivatePendingSubscriptions, kernel.JobTerminateEndedSubscriptions, kernel.JobEndTrials} {
		ok, err := f.svc.HandleJob(f.Ctx, port.Job{Kind: kind, OrganizationID: f.OrgID(), Args: []byte(`{}`)})
		check(t, kind+" handled", ok, true)
		if !errors.Is(err, core.ErrInvalidValue) {
			t.Errorf("%s error: got = %v, want = %v", kind, err, core.ErrInvalidValue)
		}
	}
}

func TestHandleJob(t *testing.T) {
	f := newFixture(t, now)
	sub := f.pending(f.Plan(period.Monthly, 1000))
	args, err := json.Marshal(kernel.InstantArgs{At: now.Add(2 * time.Hour)})
	must(t, err)
	ok, err := f.svc.HandleJob(f.Ctx, port.Job{Kind: kernel.JobActivatePendingSubscriptions, OrganizationID: f.OrgID(), Args: args})
	must(t, err)
	check(t, "handled", ok, true)
	check(t, "status", f.reload(sub.ID).Status, subscription.StatusActive)
	for _, kind := range []string{kernel.JobTerminateEndedSubscriptions, kernel.JobEndTrials} {
		ok, err := f.svc.HandleJob(f.Ctx, port.Job{Kind: kind, OrganizationID: f.OrgID(), Args: args})
		must(t, err)
		check(t, kind, ok, true)
	}
	ok, err = f.svc.HandleJob(f.Ctx, port.Job{Kind: kernel.JobBillSubscriptions})
	must(t, err)
	check(t, "other kind", ok, false)
}
