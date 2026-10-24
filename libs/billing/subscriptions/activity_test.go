package subscriptions_test

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/subscriptions"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

func TestUpdateActivityLogs(t *testing.T) {
	const (
		started = auditlog.ActivitySubscriptionStarted
		updated = auditlog.ActivitySubscriptionUpdated
	)
	tests := []struct {
		name, source string
		// at is the subscription's start; in the future, it starts in the
		// future.
		at   time.Time
		in   subscriptions.UpdateInput
		want []billingtest.ActivityWant
	}{
		{
			name: "active subscription", source: "spec/services/subscriptions/update_service_spec.rb:206",
			at: now.AddDate(0, 0, -5), in: subscriptions.UpdateInput{Name: core.Some("Renamed")},
			want: []billingtest.ActivityWant{{Type: updated, Changes: []string{"name"}}},
		},
		{
			// No log, though the webhook goes out.
			name: "subscription starting in the future", source: "spec/services/subscriptions/update_service_spec.rb:390",
			at: now.AddDate(0, 0, 2), in: subscriptions.UpdateInput{Name: core.Some("Renamed")},
		},
		{
			name: "start moved to today", source: "spec/services/subscriptions/update_service_spec.rb:415",
			at: now.AddDate(0, 0, 2), in: subscriptions.UpdateInput{SubscriptionAt: core.Some(now.Add(-time.Hour))},
			want: []billingtest.ActivityWant{{Type: started}},
		},
		{
			name: "start moved to the past", source: "app/services/subscriptions/update_service.rb:194-201",
			at: now.AddDate(0, 0, 2), in: subscriptions.UpdateInput{SubscriptionAt: core.Some(now.AddDate(0, 0, -5))},
			want: []billingtest.ActivityWant{{Type: started}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newFixture(t)
			f.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceAPI})
			c := f.Customer()
			sub := f.Subscription(c, f.Plan(period.Monthly, 1000), billingtest.SubscribedAt(tt.at))
			in := tt.in
			in.OrganizationID, in.Subscription = f.OrgID(), subscriptions.SubscriptionRef{ID: sub.ID}
			if _, err := f.svc.Update(f.Ctx, in); err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			for i := range tt.want {
				tt.want[i].ResourceType, tt.want[i].ResourceID = auditlog.ResourceSubscription, sub.ID.UUID()
				tt.want[i].ExternalCustomerID, tt.want[i].ExternalSubscriptionID = c.ExternalID, sub.ExternalID
			}
			if len(tt.want) == 0 {
				if got := f.Activities(updated, started); len(got) != 0 {
					t.Errorf("logs: got = %d, want = 0", len(got))
				}
				return
			}
			f.WantActivities(tt.want...)
			if got := f.Activities(updated); tt.want[0].Type == started && len(got) != 0 {
				t.Errorf("updated logs: got = %d, want = 0", len(got))
			}
		})
	}
}

// A refused update logs nothing.
//
// source: spec/services/subscriptions/update_service_spec.rb:41
func TestUpdateActivityLogsRefused(t *testing.T) {
	f := newFixture(t)
	f.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceAPI})
	sub := f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000), func(s *subscription.Subscription) {
		if err := s.Activate(now, now); err != nil {
			t.Fatal(err)
		}
		s.Status = subscription.StatusIncomplete
	})
	if _, err := f.svc.Update(f.Ctx, subscriptions.UpdateInput{
		OrganizationID: f.OrgID(), Subscription: subscriptions.SubscriptionRef{ID: sub.ID}, Name: core.Some("x"),
	}); err == nil {
		t.Fatal("Update() error = nil, want an error")
	}
	f.WantActivities()
}
