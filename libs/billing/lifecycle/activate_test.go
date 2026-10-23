package lifecycle_test

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var now = billingtest.Date(2026, time.March, 10, 12, 0)

func TestActivateStandalone(t *testing.T) {
	tests := []struct {
		name   string
		source string
		plan   []func(*catalog.Plan)
		fixed  *bool // a fixed charge, paid in advance or not
		want   []string
	}{
		{name: "pay in arrears", source: "spec/services/subscriptions/activate_service_spec.rb:35"},
		{
			name: "pay in advance, not in trial", source: "spec/services/subscriptions/activate_service_spec.rb:75",
			plan: []func(*catalog.Plan){inAdvance}, want: []string{"2026-03-10 12:00:00 skip sub:subscription_starting"},
		},
		{
			name: "pay in advance with in-advance fixed charges", source: "spec/services/subscriptions/activate_service_spec.rb:92",
			plan: []func(*catalog.Plan){inAdvance}, fixed: new(true), want: []string{"2026-03-10 12:00:00 skip sub:subscription_starting"},
		},
		{
			name: "pay in arrears with in-advance fixed charges", source: "spec/services/subscriptions/activate_service_spec.rb:106",
			fixed: new(true), want: []string{"2026-03-10 12:00:00 skip sub:subscription_starting"},
		},
		{
			name: "pay in arrears with in-arrears fixed charges", source: "spec/services/subscriptions/activate_service_spec.rb:125",
			fixed: new(false),
		},
		{
			name: "pay in advance in trial", source: "spec/services/subscriptions/activate_service_spec.rb:139",
			plan: []func(*catalog.Plan){inAdvance, trial(10)},
		},
		{
			name: "pay in advance in trial with in-advance fixed charges", source: "spec/services/subscriptions/activate_service_spec.rb:148",
			plan: []func(*catalog.Plan){inAdvance, trial(10)}, fixed: new(true),
			want: []string{"2026-03-10 12:00:00 skip sub:subscription_starting"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, now)
			p := f.Plan(period.Monthly, 1000, tc.plan...)
			if tc.fixed != nil {
				f.inAdvanceFixed(p, *tc.fixed)
			}
			sub := f.name(f.pending(p), "sub")
			_, err := f.svc.Activate(f.Ctx, f.OrgID(), sub.ID, now)
			must(t, err)
			got := f.reload(sub.ID)
			check(t, "status", got.Status, subscription.StatusActive)
			check(t, "started_at", stamp(*got.StartedAt), stamp(now))
			checkSlice(t, "bills", f.bills(), tc.want)
			checkSlice(t, "webhooks", f.Webhooks(), hooks(webhook.EventTypeSubscriptionStarted))
		})
	}
}

func TestActivateGated(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		plan      []func(*catalog.Plan)
		fixed     bool
		wantRule  subscription.ActivationRuleStatus
		wantState subscription.Status
		want      []string
		wantHooks []webhook.EventType
	}{
		{
			name: "payment rule on an in-advance plan", source: "spec/services/subscriptions/activate_service_spec.rb:168",
			plan: []func(*catalog.Plan){inAdvance}, wantRule: subscription.ActivationRuleStatusPending, wantState: subscription.StatusIncomplete,
			want: []string{"2026-03-10 12:00:00 skip sub:subscription_starting"}, wantHooks: hooks(webhook.EventTypeSubscriptionIncomplete),
		},
		{
			name: "payment rule on an in-arrears plan with in-advance fixed charges", source: "spec/services/subscriptions/activate_service_spec.rb:219",
			fixed: true, wantRule: subscription.ActivationRuleStatusPending, wantState: subscription.StatusIncomplete,
			want: []string{"2026-03-10 12:00:00 skip sub:subscription_starting"}, wantHooks: hooks(webhook.EventTypeSubscriptionIncomplete),
		},
		{
			name: "payment rule in trial with in-advance fixed charges", source: "spec/services/subscriptions/activate_service_spec.rb:247",
			plan: []func(*catalog.Plan){inAdvance, trial(10)}, fixed: true,
			wantRule: subscription.ActivationRuleStatusPending, wantState: subscription.StatusIncomplete,
			want: []string{"2026-03-10 12:00:00 skip sub:subscription_starting"}, wantHooks: hooks(webhook.EventTypeSubscriptionIncomplete),
		},
		{
			name: "payment rule in trial is not applicable", source: "spec/services/subscriptions/activate_service_spec.rb:238",
			plan: []func(*catalog.Plan){inAdvance, trial(10)}, wantRule: subscription.ActivationRuleStatusNotApplicable,
			wantState: subscription.StatusActive, wantHooks: hooks(webhook.EventTypeSubscriptionStarted),
		},
		{
			name: "payment rule on an in-arrears plan is not applicable", source: "spec/services/subscriptions/activate_service_spec.rb:261",
			wantRule: subscription.ActivationRuleStatusNotApplicable, wantState: subscription.StatusActive,
			wantHooks: hooks(webhook.EventTypeSubscriptionStarted),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, now)
			p := f.Plan(period.Monthly, 1000, tc.plan...)
			if tc.fixed {
				f.inAdvanceFixed(p, true)
			}
			sub := f.name(f.pending(p), "sub")
			f.rule(sub, subscription.ActivationRuleStatusInactive)
			_, err := f.svc.Activate(f.Ctx, f.OrgID(), sub.ID, now)
			must(t, err)
			got := f.reload(sub.ID)
			check(t, "status", got.Status, tc.wantState)
			checkSlice(t, "rules", f.rules(sub), []subscription.ActivationRuleStatus{tc.wantRule})
			checkSlice(t, "bills", f.bills(), tc.want)
			checkSlice(t, "webhooks", f.Webhooks(), tc.wantHooks)
		})
	}
}

func TestActivateIncomplete(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		rule      subscription.ActivationRuleStatus // "" for no rule
		wantState subscription.Status
		want      []string
		wantHooks []webhook.EventType
	}{
		{
			name: "satisfied payment rule was billed while gated", source: "spec/services/subscriptions/activate_service_spec.rb:281",
			rule: subscription.ActivationRuleStatusSatisfied, wantState: subscription.StatusActive,
			wantHooks: hooks(webhook.EventTypeSubscriptionStarted),
		},
		{
			name: "no payment rule bills the start", source: "spec/services/subscriptions/activate_service_spec.rb:526",
			wantState: subscription.StatusActive, want: []string{"2026-03-10 12:00:00 sub:subscription_starting"},
			wantHooks: hooks(webhook.EventTypeSubscriptionStarted),
		},
		{
			name: "failed payment rule does not activate", source: "spec/services/subscriptions/activate_service_spec.rb:544",
			rule: subscription.ActivationRuleStatusFailed, wantState: subscription.StatusIncomplete,
		},
		{
			name: "pending rule keeps it gated", source: "spec/services/subscriptions/activate_service_spec.rb:574",
			rule: subscription.ActivationRuleStatusPending, wantState: subscription.StatusIncomplete,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, now)
			p := f.Plan(period.Monthly, 1000, inAdvance)
			sub := f.name(f.pending(p), "sub")
			f.UpdateSubscription(sub, func(s *subscription.Subscription) { must(t, s.MarkIncomplete(now.Add(-time.Hour))) })
			if tc.rule != "" {
				f.rule(sub, tc.rule)
			}
			_, err := f.svc.Activate(f.Ctx, f.OrgID(), sub.ID, now)
			must(t, err)
			check(t, "status", f.reload(sub.ID).Status, tc.wantState)
			checkSlice(t, "bills", f.bills(), tc.want)
			checkSlice(t, "webhooks", f.Webhooks(), tc.wantHooks)
		})
	}
}

// source: spec/services/subscriptions/activate_service_spec.rb:561
func TestActivateActiveIsNoOp(t *testing.T) {
	f := newFixture(t, now)
	sub := f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000, inAdvance))
	_, err := f.svc.Activate(f.Ctx, f.OrgID(), sub.ID, now.Add(time.Hour))
	must(t, err)
	check(t, "started_at", stamp(*f.reload(sub.ID).StartedAt), stamp(now))
	checkSlice(t, "bills", f.bills(), nil)
	checkSlice(t, "webhooks", f.Webhooks(), nil)
}

func TestActivateWithoutBiller(t *testing.T) {
	f := newFixture(t, now)
	f.svc.WithBiller(nil)
	sub := f.pending(f.Plan(period.Monthly, 1000, inAdvance))
	if _, err := f.svc.Activate(f.Ctx, f.OrgID(), sub.ID, now); err == nil {
		t.Fatalf("error: got = nil, want = ErrNoBiller")
	}
	check(t, "status", f.reload(sub.ID).Status, subscription.StatusPending)
}
