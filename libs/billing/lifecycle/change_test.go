package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/period"
)

func (f *fixture) changePlan(cur *subscription.Subscription, p *catalog.Plan) *subscription.Subscription {
	f.T.Helper()
	next, err := f.svc.ChangePlan(f.Ctx, lifecycle.ChangePlanInput{OrganizationID: f.OrgID(), SubscriptionID: cur.ID, PlanID: p.ID})
	if err != nil {
		f.T.Fatalf("change plan: %v", err)
	}
	return next
}

// downgrade schedules a downgrade of cur to a cheaper plan.
func (f *fixture) downgrade(cur *subscription.Subscription, opts ...func(*catalog.Plan)) *subscription.Subscription {
	f.T.Helper()
	return f.changePlan(cur, f.Plan(period.Monthly, 10, opts...))
}

func TestUpgrade(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		oldPlan []func(*catalog.Plan)
		newPlan []func(*catalog.Plan)
		want    []string
		credit  bool
	}{
		{
			name: "in-arrears new plan bills the old subscription only", source: "spec/services/subscriptions/activate_service_spec.rb:658",
			want: []string{"2026-03-10 12:00:01 old:subscription_terminating"},
		},
		{
			name: "in-advance new plan bills both on one invoice", source: "spec/services/subscriptions/activate_service_spec.rb:671",
			newPlan: []func(*catalog.Plan){inAdvance},
			want:    []string{"2026-03-10 12:00:01 old:subscription_terminating,new:subscription_starting"},
		},
		{
			name: "in-advance new plan in the chain's trial bills the old one only", source: "spec/services/subscriptions/plan_upgrade_service_spec.rb",
			oldPlan: []func(*catalog.Plan){trial(30)}, newPlan: []func(*catalog.Plan){inAdvance, trial(30)},
			want: []string{"2026-03-10 12:00:01 old:subscription_terminating"},
		},
		{
			name: "in-advance old plan credits the unused days", source: "spec/services/subscriptions/create_service_spec.rb:1329",
			oldPlan: []func(*catalog.Plan){inAdvance},
			want:    []string{"2026-03-10 12:00:01 old:subscription_terminating"}, credit: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, now)
			c := f.Customer()
			cur := f.name(f.Subscription(c, f.Plan(period.Monthly, 1000, tc.oldPlan...), billingtest.SubscribedAt(now.AddDate(0, 0, -5))), "old")
			if tc.credit {
				inv := f.StoredInvoice(c)
				f.StoredFee(inv, 1000, billingtest.SubscriptionFee(cur))
			}
			next := f.changePlan(cur, f.Plan(period.Monthly, 2000, tc.newPlan...))
			old := f.reload(cur.ID)
			check(t, "old status", old.Status, subscription.StatusTerminated)
			check(t, "old terminated_at", stamp(*old.TerminatedAt), stamp(now))
			got := f.reload(next.ID)
			check(t, "new status", got.Status, subscription.StatusActive)
			check(t, "new started_at", stamp(*got.StartedAt), stamp(now))
			check(t, "previous", got.PreviousSubscriptionID, cur.ID)
			check(t, "external_id", got.ExternalID, cur.ExternalID)
			checkSlice(t, "bills", f.bills(), tc.want)
			checkSlice(t, "webhooks", f.Webhooks(), hooks(webhook.EventTypeSubscriptionTerminated, webhook.EventTypeSubscriptionStarted))
			if tc.credit {
				if len(f.credits.ins) != 1 || !f.credits.ins[0].Upgrade || f.credits.ins[0].SubscriptionID != cur.ID {
					t.Fatalf("credit notes: got = %+v, want = one upgrade credit note of the old subscription", f.credits.ins)
				}
			} else {
				check(t, "credit notes", len(f.credits.ins), 0)
			}
		})
	}
}

func TestUpgradePurchaseOrderNumbers(t *testing.T) {
	tests := []struct {
		name, source string
		newPO        *string
		want         []string
	}{
		{
			"same number bills both on one invoice", "spec/services/subscriptions/activate_service_spec.rb:673", nil,
			[]string{"2026-03-10 12:00:01 old:subscription_terminating,new:subscription_starting"},
		},
		{
			"different numbers bill one invoice each", "spec/services/subscriptions/activate_service_spec.rb:694", new("PO-2"),
			[]string{"2026-03-10 12:00:01 old:subscription_terminating", "2026-03-10 12:00:01 new:subscription_starting"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("source: %s", tc.source)
			f := newFixture(t, now)
			cur := f.name(f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000), billingtest.SubscribedAt(now.AddDate(0, 0, -5))), "old")
			f.UpdateSubscription(cur, func(s *subscription.Subscription) { s.PurchaseOrderNumber = new("PO-1") })
			p := f.Plan(period.Monthly, 2000, inAdvance)
			_, err := f.svc.ChangePlan(f.Ctx, lifecycle.ChangePlanInput{OrganizationID: f.OrgID(), SubscriptionID: cur.ID, PlanID: p.ID, PurchaseOrderNumber: tc.newPO})
			if err != nil {
				t.Fatalf("change plan: %v", err)
			}
			checkSlice(t, "bills", f.bills(), tc.want)
		})
	}
}

// source: spec/services/subscriptions/activate_service_spec.rb:712
func TestUpgradeGated(t *testing.T) {
	f := newFixture(t, now)
	cur := f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000))
	newPlan := f.Plan(period.Monthly, 2000, inAdvance)
	// The rule is stored on the new subscription before activation: the
	// upgrade path of Create (rules attached by the API) is modeled by a
	// subscription created pending, then activated.
	next := f.Subscription(f.Customer(), newPlan, billingtest.SubscribedAt(now.Add(time.Hour)), func(s *subscription.Subscription) {
		s.CustomerID, s.ExternalID, s.PreviousSubscriptionID = cur.CustomerID, cur.ExternalID, cur.ID
	})
	f.name(next, "next")
	f.rule(next, subscription.ActivationRuleStatusInactive)
	_, err := f.svc.Activate(f.Ctx, f.OrgID(), next.ID, now)
	must(t, err)
	check(t, "new status", f.reload(next.ID).Status, subscription.StatusIncomplete)
	check(t, "old status", f.reload(cur.ID).Status, subscription.StatusActive)
	checkSlice(t, "bills", f.bills(), []string{"2026-03-10 12:00:00 skip next:subscription_starting"})
	checkSlice(t, "webhooks", f.Webhooks(), hooks(webhook.EventTypeSubscriptionIncomplete))
}

// source: spec/services/subscriptions/plan_downgrade_service_spec.rb
func TestDowngradeSchedulesNext(t *testing.T) {
	f := newFixture(t, now)
	cur := f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000), billingtest.Anniversary)
	next := f.downgrade(cur)
	check(t, "current status", f.reload(cur.ID).Status, subscription.StatusActive)
	got := f.reload(next.ID)
	check(t, "next status", got.Status, subscription.StatusPending)
	check(t, "previous", got.PreviousSubscriptionID, cur.ID)
	check(t, "billing_time", got.BillingTime, period.Anniversary)
	checkSlice(t, "bills", f.bills(), nil)
	checkSlice(t, "webhooks", f.Webhooks(), hooks(webhook.EventTypeSubscriptionUpdated))

	// source: spec/services/subscriptions/create_service_spec.rb:1666
	again := f.downgrade(cur)
	check(t, "replaced status", f.reload(next.ID).Status, subscription.StatusCanceled)
	check(t, "new next status", f.reload(again.ID).Status, subscription.StatusPending)
	checkSlice(t, "webhooks", f.Webhooks(), hooks(webhook.EventTypeSubscriptionUpdated,
		webhook.EventTypeSubscriptionCanceled, webhook.EventTypeSubscriptionUpdated))
}

// source: spec/services/subscriptions/plan_upgrade_service_spec.rb (current subscription is pending)
func TestChangePlanOfFutureSubscriptionInPlace(t *testing.T) {
	f := newFixture(t, now)
	sub := f.pending(f.Plan(period.Monthly, 1000))
	for _, amount := range []int64{2000, 10} {
		p := f.Plan(period.Monthly, amount)
		got := f.changePlan(sub, p)
		check(t, "id", got.ID, sub.ID)
		check(t, "plan", f.reload(sub.ID).PlanID, p.ID)
		check(t, "status", f.reload(sub.ID).Status, subscription.StatusPending)
	}
	checkSlice(t, "bills", f.bills(), nil)
}

func TestChangePlanSamePlan(t *testing.T) {
	f := newFixture(t, now)
	p := f.Plan(period.Monthly, 1000)
	sub := f.Subscription(f.Customer(), p)
	check(t, "id", f.changePlan(sub, p).ID, sub.ID)
	checkSlice(t, "webhooks", f.Webhooks(), nil)
}

func TestSwapDowngrade(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		newPlan []func(*catalog.Plan)
		fixed   bool
		want    []string
	}{
		{
			name: "in-arrears next plan", source: "spec/services/subscriptions/activate_service_spec.rb:875",
			want: []string{"2026-04-01 00:00:00 old:subscription_terminating"},
		},
		{
			name: "in-advance next plan", source: "spec/services/subscriptions/activate_service_spec.rb:889",
			newPlan: []func(*catalog.Plan){inAdvance},
			want:    []string{"2026-04-01 00:00:00 old:subscription_terminating,new:subscription_starting"},
		},
		{
			name: "next plan with in-advance fixed charges", source: "spec/services/subscriptions/activate_service_spec.rb:900",
			fixed: true, want: []string{"2026-04-01 00:00:00 old:subscription_terminating,new:subscription_starting"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, now)
			cur := f.name(f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000)), "old")
			p := f.Plan(period.Monthly, 10, tc.newPlan...)
			if tc.fixed {
				f.inAdvanceFixed(p, true)
			}
			next := f.changePlan(cur, p)
			at := billingtest.Date(2026, time.April, 1)
			f.Clock.Set(at)
			var swapped bool
			f.Do(func(ctx context.Context, r port.Repos) error {
				var err error
				swapped, err = f.svc.SwapDowngrade(ctx, r, f.OrgID(), cur.ID, at)
				return err
			})
			check(t, "swapped", swapped, true)
			old := f.reload(cur.ID)
			check(t, "old status", old.Status, subscription.StatusTerminated)
			check(t, "old terminated_at", stamp(*old.TerminatedAt), stamp(at))
			got := f.reload(next.ID)
			check(t, "new status", got.Status, subscription.StatusActive)
			check(t, "new started_at", stamp(*got.StartedAt), stamp(at))
			checkSlice(t, "bills", f.bills(), tc.want)
			checkSlice(t, "webhooks", f.Webhooks(), hooks(webhook.EventTypeSubscriptionUpdated,
				webhook.EventTypeSubscriptionTerminated, webhook.EventTypeSubscriptionStarted))
		})
	}
}

func TestSwapDowngradeWithoutPendingNext(t *testing.T) {
	tests := []struct {
		name   string
		source string
		next   func(f *fixture, cur *subscription.Subscription)
	}{
		{name: "no next subscription", source: "spec/services/subscriptions/terminate_service_spec.rb:624"},
		{
			name: "next subscription not pending", source: "spec/services/subscriptions/terminate_service_spec.rb:634",
			next: func(f *fixture, cur *subscription.Subscription) {
				next := f.downgrade(cur)
				f.UpdateSubscription(next, func(s *subscription.Subscription) { s.Status, s.CanceledAt = subscription.StatusCanceled, &now })
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, now)
			cur := f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000))
			if tc.next != nil {
				tc.next(f, cur)
			}
			swapped := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (bool, error) {
				return f.svc.SwapDowngrade(ctx, r, f.OrgID(), cur.ID, now)
			})
			check(t, "swapped", swapped, false)
			check(t, "status", f.reload(cur.ID).Status, subscription.StatusActive)
			checkSlice(t, "bills", f.bills(), nil)
		})
	}
}
