package rpc_test

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/period"
	catalogv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	subscriptionv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/subscription/v1"
)

func (f *subscriptionFixture) subPlan(t *testing.T, id string) *catalog.Plan {
	t.Helper()
	pid, err := rpc.ParseID[core.PlanID]("plan_id", id)
	if err != nil {
		t.Fatalf("ParseID(%q): %v", id, err)
	}
	return billingtest.Read(f.s.Fixtures, func(ctx context.Context, r port.Repos) (*catalog.Plan, error) {
		return r.Plans.Get(ctx, f.s.Fixtures.OrgID(), pid)
	})
}

func subThreshold(amount string) *catalogv1.UsageThreshold {
	return catalogv1.UsageThreshold_builder{Amount: new(amount), ThresholdDisplayName: new("T " + amount)}.Build()
}

// source: spec/requests/api/v1/subscriptions_controller_spec.rb:66
// source: spec/requests/api/v1/subscriptions_controller_spec.rb:188
func TestCreateSubscriptionWithPlanOverrides(t *testing.T) {
	t.Parallel()
	f := newSubscriptionFixture(t)
	f.s.Fixtures.UsageThreshold(f.plan, 5000, false)
	cust := rpc.ExternalIDPrefix + f.s.Fixtures.Customer().ExternalID
	sub, err := f.create(t, subscriptionv1.Subscription_builder{
		ExternalId: new("sub_o"), CustomerId: new(cust),
		PlanOverrides: subscriptionv1.PlanOverrides_builder{
			Amount: commonv1.Money_builder{Currency: new("USD"), Amount: new("7.77")}.Build(),
			Name:   new("overridden name"),
			MinimumCommitment: catalogv1.Commitment_builder{
				Amount: new("10.00"), InvoiceDisplayName: new("Commitment"),
			}.Build(),
		}.Build(),
		UsageThresholds: []*catalogv1.UsageThreshold{subThreshold("9.99")},
	})
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if sub.GetPlanId() == f.plan.ID.String() || sub.GetParentPlanId() != f.plan.ID.String() {
		t.Fatalf("got plan = %s parent %s, want = an override of %s", sub.GetPlanId(), sub.GetParentPlanId(), f.plan.ID)
	}
	if p := f.subPlan(t, sub.GetPlanId()); p.Amount != 777 || p.Name != "overridden name" {
		t.Errorf("got override plan = %d %q, want = 777 overridden name", p.Amount, p.Name)
	}
	ts := sub.GetUsageThresholds()
	if len(ts) != 1 || ts[0].GetAmount() != "9.99" || ts[0].GetThresholdDisplayName() != "T 9.99" {
		t.Errorf("got usage_thresholds = %v, want = the subscription's 9.99", ts)
	}
}

func TestSubscriptionPlanOverridesRefused(t *testing.T) {
	t.Parallel()
	f := newSubscriptionFixture(t)
	cust := rpc.ExternalIDPrefix + f.s.Fixtures.Customer().ExternalID

	// source: spec/services/subscriptions/create_service_spec.rb:344
	_, err := f.create(t, subscriptionv1.Subscription_builder{
		ExternalId: new("s"), CustomerId: new(cust),
		UsageThresholds: []*catalogv1.UsageThreshold{subThreshold("1")},
		PlanOverrides:   subscriptionv1.PlanOverrides_builder{UsageThresholds: []*catalogv1.UsageThreshold{subThreshold("2")}}.Build(),
	})
	subWantViolation(t, err, "subscription.usage_thresholds", "incompatible_params")
	subWantViolation(t, err, "subscription.plan_overrides.usage_thresholds", "incompatible_params")

	// source: spec/services/usage_thresholds/update_service_spec.rb:44
	_, err = f.create(t, subscriptionv1.Subscription_builder{
		ExternalId: new("s"), CustomerId: new(cust),
		UsageThresholds: []*catalogv1.UsageThreshold{subThreshold("1"), subThreshold("1")},
	})
	subWantViolation(t, err, "subscription.usage_thresholds", "duplicated_values")

	_, err = f.create(t, subscriptionv1.Subscription_builder{
		ExternalId: new("s"), CustomerId: new(cust),
		PlanOverrides: subscriptionv1.PlanOverrides_builder{
			FixedCharges: []*catalogv1.FixedCharge{catalogv1.FixedCharge_builder{Units: new("3")}.Build()},
		}.Build(),
	})
	subWantViolation(t, err, "subscription.plan_overrides.fixed_charges[0].id", "value_is_mandatory")
	subWantCode(t, err, connect.CodeInvalidArgument, "")
}

// source: spec/requests/api/v1/subscriptions_controller_spec.rb:1428
func TestUpdateSubscriptionWithPlanOverrides(t *testing.T) {
	t.Parallel()
	f := newSubscriptionFixture(t)
	f.s.Fixtures.UsageThreshold(f.plan, 5000, false)
	sub := f.s.Fixtures.Subscription(f.s.Fixtures.Customer(), f.plan)
	resp, err := f.c.UpdateSubscription(t.Context(), subscriptionv1.UpdateSubscriptionRequest_builder{
		Subscription: subscriptionv1.Subscription_builder{
			Id:              new(rpc.ExternalIDPrefix + sub.ExternalID),
			PlanOverrides:   subscriptionv1.PlanOverrides_builder{Name: new("rename plan to create override")}.Build(),
			UsageThresholds: []*catalogv1.UsageThreshold{subThreshold("9.99")},
		}.Build(),
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"plan_overrides", "usage_thresholds"}},
	}.Build())
	if err != nil {
		t.Fatalf("UpdateSubscription: %v", err)
	}
	got := resp.GetSubscription()
	if got.GetParentPlanId() != f.plan.ID.String() || f.subPlan(t, got.GetPlanId()).Name != "rename plan to create override" {
		t.Errorf("got plan = %s parent %s, want = a renamed override of %s", got.GetPlanId(), got.GetParentPlanId(), f.plan.ID)
	}
	if ts := got.GetUsageThresholds(); len(ts) != 1 || ts[0].GetAmount() != "9.99" {
		t.Errorf("got usage_thresholds = %v, want = the subscription's 9.99", ts)
	}

	// An empty list in the mask clears the subscription's thresholds: the
	// original plan's apply again.
	resp, err = f.c.UpdateSubscription(t.Context(), subscriptionv1.UpdateSubscriptionRequest_builder{
		Subscription: subscriptionv1.Subscription_builder{Id: new(sub.ID.String())}.Build(),
		UpdateMask:   &fieldmaskpb.FieldMask{Paths: []string{"usage_thresholds"}},
	}.Build())
	if err != nil {
		t.Fatalf("UpdateSubscription(clear): %v", err)
	}
	if ts := resp.GetSubscription().GetUsageThresholds(); len(ts) != 1 || ts[0].GetAmount() != "50.00" {
		t.Errorf("got usage_thresholds = %v, want = the original plan's 50.00", ts)
	}
}

// source: app/services/subscriptions/plan_upgrade_service.rb:57
func TestChangeSubscriptionPlanWithOverrides(t *testing.T) {
	t.Parallel()
	f := newSubscriptionFixture(t)
	sub := f.s.Fixtures.Subscription(f.s.Fixtures.Customer(), f.plan)
	higher := f.s.Fixtures.Plan(period.Monthly, 5000)
	resp, err := f.c.ChangeSubscriptionPlan(t.Context(), subscriptionv1.ChangeSubscriptionPlanRequest_builder{
		SubscriptionId: new(sub.ID.String()), PlanId: new(higher.ID.String()),
		PlanOverrides: subscriptionv1.PlanOverrides_builder{
			Amount: commonv1.Money_builder{Currency: new("USD"), Amount: new("60.00")}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		t.Fatalf("ChangeSubscriptionPlan: %v", err)
	}
	got := resp.GetSubscription()
	if got.GetParentPlanId() != higher.ID.String() || f.subPlan(t, got.GetPlanId()).Amount != 6000 ||
		got.GetStatus() != subscriptionv1.SubscriptionStatus_SUBSCRIPTION_STATUS_ACTIVE {
		t.Errorf("got = %v, want = active on an override of %s at 60.00", got, higher.ID)
	}
}
