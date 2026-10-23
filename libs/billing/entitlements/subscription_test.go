package entitlements_test

import (
	"context"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/entitlements"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// subFixture is a subscription to a plan granting seats max 2, reset
// false and signin okta.
type subFixture struct {
	h    *billingtest.Harness
	s    *entitlements.Service
	f    *catalog.Feature
	plan *catalog.Plan
	sub  *subscription.Subscription
	ref  refs.SubscriptionRef
}

func newSubFixture(t *testing.T, planned bool) *subFixture {
	t.Helper()
	h, s := setup(t)
	x := &subFixture{h: h, s: s, f: seats(t, h, s), plan: plan(h)}
	if planned {
		grant(t, h, s, x.plan, x.f, "max", "2", "reset", "false", "signin", "okta")
	}
	x.sub = h.Subscription(h.Customer(), x.plan)
	x.ref = refs.SubscriptionRef{ID: x.sub.ID}
	return x
}

func (x *subFixture) upsert(t *testing.T, kv ...string) entitlements.Entitlement {
	t.Helper()
	got, err := x.s.UpsertSubscriptionEntitlement(t.Context(), x.h.OrgID(), x.ref, input(x.f, kv...))
	if err != nil {
		t.Fatalf("UpsertSubscriptionEntitlement() error = %v", err)
	}
	return got
}

func (x *subFixture) list(t *testing.T) map[string]map[string]string {
	t.Helper()
	got, err := x.s.ListSubscriptionEntitlements(t.Context(), x.h.OrgID(), x.ref)
	if err != nil {
		t.Fatalf("ListSubscriptionEntitlements() error = %v", err)
	}
	return byCode(got)
}

func (x *subFixture) own(t *testing.T) []*catalog.Entitlement {
	t.Helper()
	return subEntitlements(t, x.h, x.sub.ID)
}

func TestUpsertSubscriptionEntitlement(t *testing.T) {
	t.Parallel()
	// source: spec/services/entitlement/subscription_entitlement_core_update_service_spec.rb:44
	t.Run("plan without the feature: adds it to the subscription", func(t *testing.T) {
		t.Parallel()
		x := newSubFixture(t, false)
		got := x.upsert(t, "max", "100", "reset", "true")
		equalValues(t, "entitlement", values(got), map[string]string{"max": "100", "reset": "true"})
		if got.Source != entitlements.SourceSubscription || got.Values[0].Source != entitlements.SourceSubscription {
			t.Errorf("sources = %q, %q, want = subscription", got.Source, got.Values[0].Source)
		}
		if own := x.own(t); len(own) != 1 || len(own[0].Values) != 2 {
			t.Errorf("own entitlements = %+v, want = one with 2 values", own)
		}
		if n := webhooks(x.h, webhook.EventTypeSubscriptionUpdated); n != 1 {
			t.Errorf("subscription.updated events = %d, want = 1", n)
		}
	})
	// source: spec/services/entitlement/subscription_entitlement_core_update_service_spec.rb:84
	t.Run("plan without the feature: updates the existing values", func(t *testing.T) {
		t.Parallel()
		x := newSubFixture(t, false)
		x.upsert(t, "max", "2", "reset", "false", "signin", "okta")
		x.upsert(t, "max", "100", "reset", "true")
		equalValues(t, "entitlement", x.list(t)["seats"], map[string]string{"max": "100", "reset": "true", "signin": "okta"})
		if own := x.own(t); len(own) != 1 {
			t.Errorf("own entitlements = %d, want = 1", len(own))
		}
	})
	// source: spec/services/entitlement/subscription_entitlement_core_update_service_spec.rb:140
	t.Run("values equal to the plan's in another order: no override", func(t *testing.T) {
		t.Parallel()
		x := newSubFixture(t, true)
		x.upsert(t, "signin", "okta", "reset", "false", "max", "2")
		if own := x.own(t); len(own) != 0 {
			t.Errorf("own entitlements = %d, want = 0", len(own))
		}
	})
	// source: spec/services/entitlement/subscription_entitlement_core_update_service_spec.rb:153
	t.Run("values equal to the plan's: discards the override and the removals", func(t *testing.T) {
		t.Parallel()
		x := newSubFixture(t, true)
		x.upsert(t, "max", "50")
		addRemoval(t, x.h, x.sub.ID, x.f, "reset")
		x.upsert(t, "signin", "okta", "reset", "false", "max", "2")
		if own := x.own(t); len(own) != 0 {
			t.Errorf("own entitlements = %d, want = 0", len(own))
		}
		if rms := removals(t, x.h, x.sub.ID); len(rms) != 0 {
			t.Errorf("removals = %v, want = none", rms)
		}
	})
	// source: spec/services/entitlement/subscription_entitlement_core_update_service_spec.rb:165
	t.Run("plan with the feature: adds overrides", func(t *testing.T) {
		t.Parallel()
		x := newSubFixture(t, true)
		got := x.upsert(t, "max", "100", "reset", "true")
		equalValues(t, "entitlement", values(got), map[string]string{"max": "100", "reset": "true", "signin": "okta"})
		own := x.own(t)
		if len(own) != 1 {
			t.Fatalf("own entitlements = %d, want = 1", len(own))
		}
		equalValues(t, "override", own[0].Values, map[string]string{"max": "100", "reset": "true"})
		if got.Source != entitlements.SourcePlan {
			t.Errorf("source = %q, want = plan", got.Source)
		}
		sources := map[string]entitlements.Source{}
		for _, v := range got.Values {
			sources[v.Privilege.Code] = v.Source
		}
		if sources["max"] != entitlements.SourceSubscription || sources["signin"] != entitlements.SourcePlan {
			t.Errorf("value sources = %v, want = max subscription, signin plan", sources)
		}
	})
	// source: spec/services/entitlement/subscription_entitlement_core_update_service_spec.rb:203
	t.Run("plan with the feature: updates the overrides", func(t *testing.T) {
		t.Parallel()
		x := newSubFixture(t, true)
		x.upsert(t, "max", "50", "signin", "password")
		x.upsert(t, "max", "100", "reset", "true")
		equalValues(t, "entitlement", x.list(t)["seats"], map[string]string{"max": "100", "reset": "true", "signin": "password"})
		if own := x.own(t); len(own) != 1 || len(own[0].Values) != 3 {
			t.Errorf("own entitlements = %+v, want = one with 3 values", own)
		}
	})
	// source: spec/services/entitlement/subscription_entitlement_core_update_service_spec.rb:239
	t.Run("re-adding a removed privilege at the plan's value lifts the removal", func(t *testing.T) {
		t.Parallel()
		x := newSubFixture(t, true)
		addRemoval(t, x.h, x.sub.ID, x.f, "max")
		if _, ok := x.list(t)["seats"]["max"]; ok {
			t.Fatalf("max is not removed")
		}
		x.upsert(t, "max", "2")
		if rms := removals(t, x.h, x.sub.ID); len(rms) != 0 {
			t.Errorf("removals = %v, want = none", rms)
		}
		if got := x.list(t)["seats"]["max"]; got != "2" {
			t.Errorf("max = %q, want = 2", got)
		}
	})
	// source: spec/services/entitlement/subscription_entitlement_core_update_service_spec.rb:265
	t.Run("lifts the removals of the privileges given", func(t *testing.T) {
		t.Parallel()
		x := newSubFixture(t, true)
		addRemoval(t, x.h, x.sub.ID, x.f, "max")
		addRemoval(t, x.h, x.sub.ID, x.f, "reset")
		addRemoval(t, x.h, x.sub.ID, x.f, "signin")
		x.upsert(t, "max", "100", "reset", "true")
		equalValues(t, "entitlement", x.list(t)["seats"], map[string]string{"max": "100", "reset": "true"})
		if rms := removals(t, x.h, x.sub.ID); !slices.Equal(rms, []string{"signin"}) {
			t.Errorf("removals = %v, want = [signin]", rms)
		}
	})
	// source: app/services/entitlement/subscription_entitlement_core_update_service.rb:59-61
	t.Run("lifts the removal of the feature", func(t *testing.T) {
		t.Parallel()
		x := newSubFixture(t, true)
		addRemoval(t, x.h, x.sub.ID, x.f, "")
		if _, ok := x.list(t)["seats"]; ok {
			t.Fatalf("seats is not removed")
		}
		x.upsert(t, "max", "100")
		equalValues(t, "entitlement", x.list(t)["seats"], map[string]string{"max": "100", "reset": "false", "signin": "okta"})
	})
	// source: spec/services/entitlement/subscription_entitlements_update_service-partial_spec.rb:68,77,92,107
	t.Run("errors", func(t *testing.T) {
		t.Parallel()
		x := newSubFixture(t, true)
		ctx, org := t.Context(), x.h.OrgID()
		_, err := x.s.UpsertSubscriptionEntitlement(ctx, org, refs.SubscriptionRef{ExternalID: "unknown"}, input(x.f, "max", "1"))
		wantNotFound(t, err, "subscription")
		_, err = x.s.UpsertSubscriptionEntitlement(ctx, org, x.ref, entitlements.EntitlementInput{Feature: refs.ByCode("unknown")})
		wantNotFound(t, err, "feature")
		_, err = x.s.UpsertSubscriptionEntitlement(ctx, org, x.ref, input(x.f, "unknown", "1"))
		wantNotFound(t, err, "privilege")
		_, err = x.s.UpsertSubscriptionEntitlement(ctx, org, x.ref, input(x.f, "max", "abc"))
		wantFieldError(t, err, "max_privilege_value", catalog.CodePrivilegeValueInvalid)
		if own := x.own(t); len(own) != 0 {
			t.Errorf("own entitlements = %d, want = 0", len(own))
		}
	})
	// source: app/controllers/api/v1/subscriptions/entitlements_controller.rb:66-77
	t.Run("by external ID, the active subscription", func(t *testing.T) {
		t.Parallel()
		x := newSubFixture(t, false)
		ref := refs.SubscriptionRef{ExternalID: x.sub.ExternalID}
		if _, err := x.s.UpsertSubscriptionEntitlement(t.Context(), x.h.OrgID(), ref, input(x.f, "max", "1")); err != nil {
			t.Fatalf("UpsertSubscriptionEntitlement() error = %v", err)
		}
		x.h.UpdateSubscription(x.sub, func(s *subscription.Subscription) { s.Status = subscription.StatusPending })
		_, err := x.s.UpsertSubscriptionEntitlement(t.Context(), x.h.OrgID(), ref, input(x.f, "max", "1"))
		wantNotFound(t, err, "subscription")
	})
}

func TestListSubscriptionEntitlements(t *testing.T) {
	t.Parallel()
	// source: app/models/entitlement/subscription_entitlement.rb:20-28
	t.Run("the original plan of an override plan", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		parent := plan(h)
		grant(t, h, s, parent, f, "max", "2")
		child := plan(h)
		h.Do(func(ctx context.Context, r port.Repos) error {
			child.ParentID = parent.ID
			return r.Plans.Update(ctx, child)
		})
		sub := h.Subscription(h.Customer(), child)
		got, err := s.ListSubscriptionEntitlements(t.Context(), h.OrgID(), refs.SubscriptionRef{ID: sub.ID})
		if err != nil {
			t.Fatalf("ListSubscriptionEntitlements() error = %v", err)
		}
		equalValues(t, "seats", byCode(got)["seats"], map[string]string{"max": "2"})
	})
}

func TestDeleteSubscriptionEntitlement(t *testing.T) {
	t.Parallel()
	// source: spec/services/entitlement/subscription_feature_remove_service_spec.rb:36
	t.Run("feature on neither the plan nor the subscription", func(t *testing.T) {
		t.Parallel()
		x := newSubFixture(t, false)
		if err := x.s.DeleteSubscriptionEntitlement(t.Context(), x.h.OrgID(), x.ref, refs.ByCode("seats")); err != nil {
			t.Fatalf("DeleteSubscriptionEntitlement() error = %v", err)
		}
		if rms := removals(t, x.h, x.sub.ID); len(rms) != 0 {
			t.Errorf("removals = %v, want = none", rms)
		}
		if n := webhooks(x.h, webhook.EventTypeSubscriptionUpdated); n != 1 {
			t.Errorf("subscription.updated events = %d, want = 1", n)
		}
	})
	// source: spec/services/entitlement/subscription_feature_remove_service_spec.rb:58,74
	t.Run("feature on the plan only: adds a removal once", func(t *testing.T) {
		t.Parallel()
		x := newSubFixture(t, true)
		for range 2 {
			if err := x.s.DeleteSubscriptionEntitlement(t.Context(), x.h.OrgID(), x.ref, refs.ByID(x.f.ID)); err != nil {
				t.Fatalf("DeleteSubscriptionEntitlement() error = %v", err)
			}
		}
		if rms := removals(t, x.h, x.sub.ID); !slices.Equal(rms, []string{""}) {
			t.Errorf("removals = %q, want = one feature removal", rms)
		}
		if _, ok := x.list(t)["seats"]; ok {
			t.Errorf("seats is still granted")
		}
	})
	// source: spec/services/entitlement/subscription_feature_remove_service_spec.rb:93,98,102
	t.Run("feature on the subscription only", func(t *testing.T) {
		t.Parallel()
		x := newSubFixture(t, false)
		x.upsert(t, "max", "10")
		if err := x.s.DeleteSubscriptionEntitlement(t.Context(), x.h.OrgID(), x.ref, refs.ByCode("seats")); err != nil {
			t.Fatalf("DeleteSubscriptionEntitlement() error = %v", err)
		}
		if own := x.own(t); len(own) != 0 {
			t.Errorf("own entitlements = %d, want = 0", len(own))
		}
		if rms := removals(t, x.h, x.sub.ID); len(rms) != 0 {
			t.Errorf("removals = %v, want = none", rms)
		}
	})
	// source: spec/services/entitlement/subscription_feature_remove_service_spec.rb:106
	t.Run("feature also on the plan", func(t *testing.T) {
		t.Parallel()
		x := newSubFixture(t, true)
		x.upsert(t, "max", "10")
		addRemoval(t, x.h, x.sub.ID, x.f, "reset")
		if err := x.s.DeleteSubscriptionEntitlement(t.Context(), x.h.OrgID(), x.ref, refs.ByCode("seats")); err != nil {
			t.Fatalf("DeleteSubscriptionEntitlement() error = %v", err)
		}
		if own := x.own(t); len(own) != 0 {
			t.Errorf("own entitlements = %d, want = 0", len(own))
		}
		if rms := removals(t, x.h, x.sub.ID); !slices.Equal(rms, []string{""}) {
			t.Errorf("removals = %q, want = only the feature removal", rms)
		}
	})
	// source: spec/services/entitlement/subscription_feature_remove_service_spec.rb:16,26
	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		x := newSubFixture(t, true)
		err := x.s.DeleteSubscriptionEntitlement(t.Context(), x.h.OrgID(), refs.SubscriptionRef{ExternalID: "unknown"}, refs.ByCode("seats"))
		wantNotFound(t, err, "subscription")
		err = x.s.DeleteSubscriptionEntitlement(t.Context(), x.h.OrgID(), x.ref, refs.ByCode("unknown"))
		wantNotFound(t, err, "feature")
	})
}

func TestDeleteSubscriptionEntitlementPrivilege(t *testing.T) {
	t.Parallel()
	// source: spec/services/entitlement/subscription_feature_privilege_remove_service_spec.rb:48
	t.Run("privilege on neither the plan nor the subscription", func(t *testing.T) {
		t.Parallel()
		x := newSubFixture(t, false)
		if _, err := x.s.DeleteSubscriptionEntitlementPrivilege(t.Context(), x.h.OrgID(), x.ref, refs.ByCode("seats"), "max"); err != nil {
			t.Fatalf("DeleteSubscriptionEntitlementPrivilege() error = %v", err)
		}
		if rms := removals(t, x.h, x.sub.ID); len(rms) != 0 {
			t.Errorf("removals = %v, want = none", rms)
		}
		if n := webhooks(x.h, webhook.EventTypeSubscriptionUpdated); n != 1 {
			t.Errorf("subscription.updated events = %d, want = 1", n)
		}
	})
	// source: spec/services/entitlement/subscription_feature_privilege_remove_service_spec.rb:71,87
	t.Run("privilege on the plan: adds a removal once", func(t *testing.T) {
		t.Parallel()
		x := newSubFixture(t, true)
		var got entitlements.Entitlement
		for range 2 {
			var err error
			if got, err = x.s.DeleteSubscriptionEntitlementPrivilege(t.Context(), x.h.OrgID(), x.ref, refs.ByCode("seats"), "max"); err != nil {
				t.Fatalf("DeleteSubscriptionEntitlementPrivilege() error = %v", err)
			}
		}
		if rms := removals(t, x.h, x.sub.ID); !slices.Equal(rms, []string{"max"}) {
			t.Errorf("removals = %q, want = [max]", rms)
		}
		equalValues(t, "entitlement", values(got), map[string]string{"reset": "false", "signin": "okta"})
	})
	// source: spec/services/entitlement/subscription_feature_privilege_remove_service_spec.rb:96,110,114
	t.Run("privilege on the subscription", func(t *testing.T) {
		t.Parallel()
		x := newSubFixture(t, false)
		x.upsert(t, "max", "10", "reset", "true")
		got, err := x.s.DeleteSubscriptionEntitlementPrivilege(t.Context(), x.h.OrgID(), x.ref, refs.ByCode("seats"), "max")
		if err != nil {
			t.Fatalf("DeleteSubscriptionEntitlementPrivilege() error = %v", err)
		}
		equalValues(t, "entitlement", values(got), map[string]string{"reset": "true"})
		if own := x.own(t); len(own) != 1 {
			t.Errorf("own entitlements = %d, want = 1 (kept)", len(own))
		}
	})
	// source: spec/services/entitlement/subscription_feature_privilege_remove_service_spec.rb:18,28,38
	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		x := newSubFixture(t, true)
		ctx, org := t.Context(), x.h.OrgID()
		_, err := x.s.DeleteSubscriptionEntitlementPrivilege(ctx, org, refs.SubscriptionRef{ExternalID: "unknown"}, refs.ByCode("seats"), "max")
		wantNotFound(t, err, "subscription")
		_, err = x.s.DeleteSubscriptionEntitlementPrivilege(ctx, org, x.ref, refs.ByCode("unknown"), "max")
		wantNotFound(t, err, "feature")
		_, err = x.s.DeleteSubscriptionEntitlementPrivilege(ctx, org, x.ref, refs.ByCode("seats"), "unknown")
		wantNotFound(t, err, "privilege")
	})
}
