package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// newLoggedFixture returns a fixture recording activity logs.
func newLoggedFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, now)
	f.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceAPI})
	return f
}

// logged is the expected log t of sub, with no changes: the lifecycle
// produces its logs without a block.
func (f *fixture) logged(t auditlog.ActivityType, sub *subscription.Subscription) billingtest.ActivityWant {
	f.T.Helper()
	c := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*customer.Customer, error) {
		return r.Customers.Get(ctx, f.OrgID(), sub.CustomerID)
	})
	return billingtest.ActivityWant{
		Type: t, ResourceType: auditlog.ResourceSubscription, ResourceID: sub.ID.UUID(),
		ExternalCustomerID: c.ExternalID, ExternalSubscriptionID: sub.ExternalID,
	}
}

func TestActivityLogs(t *testing.T) {
	const (
		started    = auditlog.ActivitySubscriptionStarted
		updated    = auditlog.ActivitySubscriptionUpdated
		incomplete = auditlog.ActivitySubscriptionIncomplete
		terminated = auditlog.ActivitySubscriptionTerminated
		canceled   = auditlog.ActivitySubscriptionCanceled
	)
	// source: spec/services/subscriptions/create_service_spec.rb:161
	t.Run("a back-dated creation is started", func(t *testing.T) {
		f := newLoggedFixture(t)
		sub, err := f.svc.Create(f.Ctx, f.build(f.Customer(), f.Plan(period.Monthly, 1000), "ext", now.AddDate(0, 0, -5)))
		must(t, err)
		f.WantActivities(f.logged(started, sub))
	})
	// source: spec/services/subscriptions/activate_service_spec.rb:32
	t.Run("an activation is started", func(t *testing.T) {
		f := newLoggedFixture(t)
		sub := f.pending(f.Plan(period.Monthly, 1000))
		_, err := f.svc.Activate(f.Ctx, f.OrgID(), sub.ID, now)
		must(t, err)
		f.WantActivities(f.logged(started, sub))
	})
	// source: spec/services/subscriptions/activate_service_spec.rb:198
	t.Run("a gated activation is incomplete", func(t *testing.T) {
		f := newLoggedFixture(t)
		sub := f.pending(f.Plan(period.Monthly, 1000, inAdvance))
		f.rule(sub, subscription.ActivationRuleStatusInactive)
		_, err := f.svc.Activate(f.Ctx, f.OrgID(), sub.ID, now)
		must(t, err)
		f.WantActivities(f.logged(incomplete, sub))
		if got := f.Activities(started); len(got) != 0 {
			t.Errorf("started logs: got = %d, want = 0", len(got))
		}
	})
	// source: spec/services/subscriptions/activate_service_spec.rb:459,471,
	// plan_upgrade_service_spec.rb:54
	t.Run("an upgrade terminates the previous subscription and starts the next", func(t *testing.T) {
		f := newLoggedFixture(t)
		cur := f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000), billingtest.SubscribedAt(now.AddDate(0, 0, -5)))
		next := f.changePlan(cur, f.Plan(period.Monthly, 2000))
		f.WantActivities(f.logged(terminated, cur), f.logged(started, next))
	})
	// source: spec/services/subscriptions/plan_downgrade_service_spec.rb:52,
	// create_service_spec.rb:1487
	t.Run("a downgrade updates the current subscription", func(t *testing.T) {
		f := newLoggedFixture(t)
		cur := f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000))
		f.downgrade(cur)
		f.WantActivities(f.logged(updated, cur))
	})
	// source: spec/services/subscriptions/activate_service_spec.rb:826,838
	t.Run("a downgrade swap terminates the previous subscription and starts the next", func(t *testing.T) {
		f := newLoggedFixture(t)
		cur := f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000))
		next := f.downgrade(cur)
		at := billingtest.Date(2026, time.April, 1)
		f.Clock.Set(at)
		f.Do(func(ctx context.Context, r port.Repos) error {
			_, err := f.svc.SwapDowngrade(ctx, r, f.OrgID(), cur.ID, at)
			return err
		})
		f.WantActivities(f.logged(terminated, cur), f.logged(started, next))
	})
	// source: spec/services/subscriptions/terminate_service_spec.rb:95
	t.Run("a termination is terminated", func(t *testing.T) {
		f := newLoggedFixture(t)
		sub := f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000), billingtest.SubscribedAt(now.AddDate(0, 0, -5)))
		must(t, f.terminate(sub))
		f.WantActivities(f.logged(terminated, sub))
	})
	// source: app/services/subscriptions/terminate_service.rb:36-37,82-83
	t.Run("terminating a pending downgrade updates the current subscription", func(t *testing.T) {
		f := newLoggedFixture(t)
		cur := f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000))
		next := f.downgrade(cur)
		must(t, f.terminate(next))
		f.WantActivities(f.logged(updated, cur), f.logged(updated, cur), f.logged(terminated, next))
	})
	// source: spec/services/subscriptions/activation_rules/resolve_subscription_status_service_spec.rb:66
	t.Run("an expired incomplete subscription is canceled", func(t *testing.T) {
		f := newLoggedFixture(t)
		sub, _ := f.incomplete(invoice.StatusOpen)
		_, err := f.expire(sub)
		must(t, err)
		f.WantActivities(f.logged(canceled, sub))
	})
	// A refused termination logs nothing.
	t.Run("a refused termination logs nothing", func(t *testing.T) {
		f := newLoggedFixture(t)
		sub := f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000), billingtest.SubscribedAt(now.AddDate(0, 0, -5)))
		must(t, f.terminate(sub))
		if err := f.terminate(sub); err == nil {
			t.Fatal("terminate() error = nil, want an error")
		}
		f.WantActivities(f.logged(terminated, sub))
	})
}
