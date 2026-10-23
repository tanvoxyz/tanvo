package rpc_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/genproto/googleapis/type/date"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
	subscriptionv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/subscription/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/subscription/v1/subscriptionv1connect"
)

// subscriptionFixture is a server with a monthly plan.
type subscriptionFixture struct {
	s    *rpctest.Server
	c    subscriptionv1connect.SubscriptionServiceClient
	plan *catalog.Plan
}

func newSubscriptionFixture(t *testing.T) *subscriptionFixture {
	t.Helper()
	s := rpctest.New(t)
	return &subscriptionFixture{
		s: s, c: subscriptionv1connect.NewSubscriptionServiceClient(s.Connect(s.APIKey)),
		plan: s.Fixtures.Plan(period.Monthly, 1000),
	}
}

func (f *subscriptionFixture) create(t *testing.T, b subscriptionv1.Subscription_builder) (*subscriptionv1.Subscription, error) {
	t.Helper()
	if b.PlanId == nil {
		b.PlanId = new(rpc.CodePrefix + f.plan.Code)
	}
	resp, err := f.c.CreateSubscription(t.Context(), subscriptionv1.CreateSubscriptionRequest_builder{Subscription: b.Build()}.Build())
	return resp.GetSubscription(), err
}

func subWantViolation(t *testing.T, err error, field, reason string) {
	t.Helper()
	for _, v := range rpc.ErrorViolations(err) {
		if v.Field == field && v.Reason == reason {
			return
		}
	}
	t.Fatalf("got err = %v (violations %+v), want = %s %s", err, rpc.ErrorViolations(err), field, reason)
}

func subWantCode(t *testing.T, err error, code connect.Code, reason string) {
	t.Helper()
	if connect.CodeOf(err) != code || (reason != "" && rpc.ErrorReason(err) != reason) {
		t.Fatalf("got err = %v (reason %q), want = %s %s", err, rpc.ErrorReason(err), code, reason)
	}
}

// source: spec/requests/api/v1/subscriptions_controller_spec.rb:237
// source: spec/requests/api/v1/subscriptions_controller_spec.rb:61
func TestCreateSubscription(t *testing.T) {
	t.Parallel()
	f := newSubscriptionFixture(t)
	sub, err := f.create(t, subscriptionv1.Subscription_builder{
		ExternalId: new("sub_1"), CustomerId: new(rpc.ExternalIDPrefix + "cus_new"),
		Name: new("Main"), PurchaseOrderNumber: new("PO-1"),
		BillingTime: new(subscriptionv1.BillingTime_BILLING_TIME_ANNIVERSARY),
	})
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if sub.GetStatus() != subscriptionv1.SubscriptionStatus_SUBSCRIPTION_STATUS_ACTIVE || sub.GetName() != "Main" ||
		sub.GetPlanId() != f.plan.ID.String() || sub.GetPurchaseOrderNumber() != "PO-1" ||
		sub.GetBillingTime() != subscriptionv1.BillingTime_BILLING_TIME_ANNIVERSARY || !sub.GetConsolidateInvoice() {
		t.Errorf("got = %v, want = active Main anniversary subscription on the plan", sub)
	}
	if !sub.GetStartedAt().AsTime().Equal(rpctest.Now) || !sub.HasCurrentPeriodStartedAt() || !sub.HasCurrentPeriodEndedAt() {
		t.Errorf("got started_at = %v, period = %v..%v, want = now and a current period", sub.GetStartedAt(), sub.GetCurrentPeriodStartedAt(), sub.GetCurrentPeriodEndedAt())
	}

	got, err := f.c.GetSubscription(t.Context(), subscriptionv1.GetSubscriptionRequest_builder{SubscriptionId: new(rpc.ExternalIDPrefix + "sub_1")}.Build())
	if err != nil || got.GetSubscription().GetId() != sub.GetId() {
		t.Fatalf("GetSubscription = %v, %v, want = %s", got, err, sub.GetId())
	}
}

func TestCreateSubscriptionRefused(t *testing.T) {
	t.Parallel()
	f := newSubscriptionFixture(t)
	cust := rpc.ExternalIDPrefix + f.s.Fixtures.Customer().ExternalID

	// source: spec/requests/api/v1/subscriptions_controller_spec.rb:386
	_, err := f.create(t, subscriptionv1.Subscription_builder{ExternalId: new("s"), CustomerId: new(cust), PlanId: new("code:nope")})
	subWantCode(t, err, connect.CodeNotFound, "plan_not_found")

	// source: spec/requests/api/v1/subscriptions_controller_spec.rb:858
	_, err = f.create(t, subscriptionv1.Subscription_builder{
		ExternalId: new("s"), CustomerId: new(cust),
		ActivationRules: []*subscriptionv1.ActivationRule{subscriptionv1.ActivationRule_builder{
			RuleType: new(subscriptionv1.ActivationRuleType_ACTIVATION_RULE_TYPE_PAYMENT), TimeoutHours: new(int32(24)),
		}.Build()},
	})
	subWantViolation(t, err, "subscription.customer_id", "no_linked_payment_provider")

	// source: app/services/subscriptions/validate_service.rb:53
	_, err = f.create(t, subscriptionv1.Subscription_builder{
		ExternalId: new("s"), CustomerId: new(cust), EndingAt: timestamppb.New(rpctest.Now.Add(time.Hour)),
	})
	subWantViolation(t, err, "subscription.ending_at", "invalid_date")

	_, err = f.create(t, subscriptionv1.Subscription_builder{
		ExternalId: new("s"), CustomerId: new(cust), BillingAnchorDate: &date.Date{Year: 2026, Month: 1, Day: 1},
	})
	subWantCode(t, err, connect.CodeUnimplemented, "")
}

// source: spec/requests/api/v1/subscriptions_controller_spec.rb:1010
// source: spec/services/subscriptions/update_service_spec.rb:419
func TestUpdateSubscription(t *testing.T) {
	t.Parallel()
	f := newSubscriptionFixture(t)
	c := f.s.Fixtures.Customer()
	pending := f.s.Fixtures.Subscription(c, f.plan, billingtest.SubscribedAt(rpctest.Now.AddDate(0, 0, 5)))
	at := rpctest.Now.AddDate(0, 0, 10)
	resp, err := f.c.UpdateSubscription(t.Context(), subscriptionv1.UpdateSubscriptionRequest_builder{
		Subscription: subscriptionv1.Subscription_builder{
			Id: new(pending.ID.String()), Name: new("Renamed"), SubscriptionAt: timestamppb.New(at),
		}.Build(),
	}.Build())
	if err != nil {
		t.Fatalf("UpdateSubscription: %v", err)
	}
	got := resp.GetSubscription()
	if got.GetName() != "Renamed" || !got.GetSubscriptionAt().AsTime().Equal(at) ||
		got.GetStatus() != subscriptionv1.SubscriptionStatus_SUBSCRIPTION_STATUS_PENDING {
		t.Errorf("got = %v, want = pending Renamed at %s", got, at)
	}

	// The mask clears the purchase order number.
	resp, err = f.c.UpdateSubscription(t.Context(), subscriptionv1.UpdateSubscriptionRequest_builder{
		Subscription: subscriptionv1.Subscription_builder{Id: new(pending.ID.String())}.Build(),
		UpdateMask:   &fieldmaskpb.FieldMask{Paths: []string{"purchase_order_number"}},
	}.Build())
	if err != nil || resp.GetSubscription().HasPurchaseOrderNumber() || resp.GetSubscription().GetName() != "Renamed" {
		t.Fatalf("UpdateSubscription(mask) = %v, %v, want = no purchase order number, name kept", resp, err)
	}
}

// source: app/controllers/concerns/subscription_index.rb:11
func TestListSubscriptions(t *testing.T) {
	t.Parallel()
	f := newSubscriptionFixture(t)
	c := f.s.Fixtures.Customer()
	active := f.s.Fixtures.Subscription(c, f.plan)
	pending := f.s.Fixtures.Subscription(c, f.plan, billingtest.SubscribedAt(rpctest.Now.AddDate(0, 0, 5)))
	f.s.Fixtures.Subscription(f.s.Fixtures.Customer(), f.s.Fixtures.Plan(period.Monthly, 1000))

	code, body := f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/customers/external_id%3A"+c.ExternalID+"/subscriptions", nil)
	if code != http.StatusOK {
		t.Fatalf("GET subscriptions = %d %s, want = 200", code, body)
	}
	out := decode[struct {
		Subscriptions []struct {
			ID string `json:"id"`
		} `json:"subscriptions"`
	}](t, body)
	if len(out.Subscriptions) != 1 || out.Subscriptions[0].ID != active.ID.String() {
		t.Errorf("got = %+v, want = only the active %s", out.Subscriptions, active.ID)
	}

	resp, err := f.c.ListSubscriptions(t.Context(), subscriptionv1.ListSubscriptionsRequest_builder{
		Filter: new(`status = "pending" AND plan_id = "code:` + f.plan.Code + `"`),
	}.Build())
	if err != nil || len(resp.GetSubscriptions()) != 1 || resp.GetSubscriptions()[0].GetId() != pending.ID.String() {
		t.Fatalf("ListSubscriptions(pending) = %v, %v, want = %s", resp, err, pending.ID)
	}
}

// source: spec/requests/api/v1/subscriptions_controller_spec.rb:918
func TestChangeSubscriptionPlan(t *testing.T) {
	t.Parallel()
	f := newSubscriptionFixture(t)
	sub := f.s.Fixtures.Subscription(f.s.Fixtures.Customer(), f.plan)
	higher := f.s.Fixtures.Plan(period.Monthly, 5000)
	resp, err := f.c.ChangeSubscriptionPlan(t.Context(), subscriptionv1.ChangeSubscriptionPlanRequest_builder{
		SubscriptionId: new(rpc.ExternalIDPrefix + sub.ExternalID), PlanId: new(higher.ID.String()),
	}.Build())
	if err != nil {
		t.Fatalf("ChangeSubscriptionPlan: %v", err)
	}
	if got := resp.GetSubscription(); got.GetPlanId() != higher.ID.String() || got.GetPreviousSubscriptionId() != sub.ID.String() ||
		got.GetStatus() != subscriptionv1.SubscriptionStatus_SUBSCRIPTION_STATUS_ACTIVE {
		t.Errorf("got = %v, want = active on %s replacing %s", got, higher.ID, sub.ID)
	}
	if prev := resp.GetPreviousSubscription(); prev.GetStatus() != subscriptionv1.SubscriptionStatus_SUBSCRIPTION_STATUS_TERMINATED {
		t.Errorf("got previous = %v, want = terminated", prev)
	}
}

// source: spec/requests/api/v1/subscriptions_controller_spec.rb:1076
// source: spec/requests/api/v1/subscriptions_controller_spec.rb:1131
func TestTerminateAndCancelSubscription(t *testing.T) {
	t.Parallel()
	f := newSubscriptionFixture(t)
	c := f.s.Fixtures.Customer()
	active := f.s.Fixtures.Subscription(c, f.plan)
	pending := f.s.Fixtures.Subscription(c, f.plan, billingtest.SubscribedAt(rpctest.Now.AddDate(0, 0, 5)))

	// The colon of the reference is escaped: the route also has verbs.
	code, body := f.s.REST(t, f.s.APIKey, http.MethodPost, "/v1/subscriptions/external_id%3A"+active.ExternalID+":terminate",
		`{"on_termination_invoice":"TERMINATION_INVOICE_SKIP"}`)
	if code != http.StatusOK {
		t.Fatalf("POST terminate = %d %s, want = 200", code, body)
	}
	out := decode[struct {
		Subscription struct {
			Status string `json:"status"`
		} `json:"subscription"`
	}](t, body)
	if out.Subscription.Status != "SUBSCRIPTION_STATUS_TERMINATED" {
		t.Errorf("got status = %s, want = terminated", out.Subscription.Status)
	}

	_, err := f.c.CancelSubscription(t.Context(), subscriptionv1.CancelSubscriptionRequest_builder{SubscriptionId: new(active.ID.String())}.Build())
	subWantCode(t, err, connect.CodeNotFound, "subscription_not_found")
	resp, err := f.c.CancelSubscription(t.Context(), subscriptionv1.CancelSubscriptionRequest_builder{SubscriptionId: new(pending.ID.String())}.Build())
	if err != nil || resp.GetSubscription().GetStatus() != subscriptionv1.SubscriptionStatus_SUBSCRIPTION_STATUS_CANCELED {
		t.Fatalf("CancelSubscription = %v, %v, want = canceled", resp, err)
	}
}

// source: spec/requests/api/v1/subscriptions/lifetime_usages_controller_spec.rb:39
// source: spec/requests/api/v1/subscriptions/lifetime_usages_controller_spec.rb:112
func TestLifetimeUsageRPCs(t *testing.T) {
	t.Parallel()
	f := newSubscriptionFixture(t)
	f.s.Fixtures.UsageThreshold(f.plan, 10000, false)
	sub := f.s.Fixtures.Subscription(f.s.Fixtures.Customer(), f.plan)
	ref := new(rpc.ExternalIDPrefix + sub.ExternalID)
	h := f.s.Fixtures
	h.Do(func(ctx context.Context, r port.Repos) error {
		return r.LifetimeUsages.Upsert(ctx, &subscription.LifetimeUsage{
			ID: core.NewID[core.LifetimeUsageID](h.IDs), OrganizationID: h.OrgID(), SubscriptionID: sub.ID,
			ExternalSubscriptionID: sub.ExternalID, Currency: money.MustCurrency("USD"), CreatedAt: rpctest.Now, UpdatedAt: rpctest.Now,
		})
	})

	resp, err := f.c.UpdateLifetimeUsage(t.Context(), subscriptionv1.UpdateLifetimeUsageRequest_builder{
		SubscriptionId: ref, ExternalHistoricalUsageAmount: new("25.5"),
	}.Build())
	if err != nil {
		t.Fatalf("UpdateLifetimeUsage: %v", err)
	}
	lu := resp.GetLifetimeUsage()
	if lu.GetExternalHistoricalUsageAmount().GetAmount() != "25.50" || len(lu.GetUsageThresholds()) != 1 ||
		lu.GetUsageThresholds()[0].GetRemainingAmount() != "74.50" {
		t.Errorf("got = %v, want = 25.50 historical and 74.50 left to the threshold", lu)
	}

	_, err = f.c.UpdateLifetimeUsage(t.Context(), subscriptionv1.UpdateLifetimeUsageRequest_builder{
		SubscriptionId: ref, ExternalHistoricalUsageAmount: new("1.005"),
	}.Build())
	subWantViolation(t, err, "external_historical_usage_amount", "invalid_value")
}
