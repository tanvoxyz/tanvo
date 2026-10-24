package entitlements_test

import (
	"context"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/entitlements"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func logged(h *billingtest.Harness) {
	h.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceAPI})
}

func TestFeatureActivityLogs(t *testing.T) {
	t.Log("source: spec/services/entitlement/feature_create_service_spec.rb:43")
	t.Log("source: spec/services/entitlement/feature_update_service_spec.rb:68")
	t.Log("source: spec/services/entitlement/feature_update_service_spec.rb:299")
	t.Log("source: spec/services/entitlement/privilege_destroy_service_spec.rb:34")
	t.Log("source: spec/services/entitlement/privilege_destroy_service_spec.rb:73")
	t.Log("source: spec/services/entitlement/feature_destroy_service_spec.rb:41")
	t.Log("source: spec/services/entitlement/feature_destroy_service_spec.rb:92")
	h, s := setup(t)
	logged(h)
	f := seats(t, h, s)
	p := plan(h)
	grant(t, h, s, p, f, "max", "2")
	if _, err := s.UpdateFeature(t.Context(), h.OrgID(), f.ID, entitlements.UpdateFeatureInput{Name: core.Some(ptr("Seats"))}); err != nil {
		t.Fatalf("UpdateFeature() error = %v", err)
	}
	if _, err := s.DeleteFeaturePrivilege(t.Context(), h.OrgID(), f.ID, "max"); err != nil {
		t.Fatalf("DeleteFeaturePrivilege() error = %v", err)
	}
	if _, err := s.DeleteFeature(t.Context(), h.OrgID(), f.ID); err != nil {
		t.Fatalf("DeleteFeature() error = %v", err)
	}
	fid, pid := f.ID.UUID(), p.ID.UUID()
	planUpdated := billingtest.ActivityWant{Type: auditlog.ActivityPlanUpdated, ResourceType: auditlog.ResourcePlan, ResourceID: pid}
	h.WantActivities(
		billingtest.ActivityWant{Type: auditlog.ActivityFeatureCreated, ResourceType: auditlog.ResourceFeature, ResourceID: fid},
		planUpdated, // the grant
		planUpdated,
		billingtest.ActivityWant{Type: auditlog.ActivityFeatureUpdated, ResourceType: auditlog.ResourceFeature, ResourceID: fid, Changes: []string{"name"}},
		planUpdated,
		billingtest.ActivityWant{Type: auditlog.ActivityFeatureUpdated, ResourceType: auditlog.ResourceFeature, ResourceID: fid, Changes: []string{"privileges"}},
		planUpdated,
		billingtest.ActivityWant{Type: auditlog.ActivityFeatureDeleted, ResourceType: auditlog.ResourceFeature, ResourceID: fid},
	)
}

func TestPlanEntitlementActivityLogs(t *testing.T) {
	t.Log("source: spec/services/entitlement/plan_entitlements_update_service-full_spec.rb:49")
	t.Log("source: spec/services/entitlement/plan_entitlements_update_service-partial_spec.rb:57")
	t.Log("source: spec/services/entitlement/plan_entitlement_privilege_destroy_service_spec.rb:53")
	t.Log("source: spec/services/entitlement/plan_entitlement_destroy_service_spec.rb:42")
	h, s := setup(t)
	f := seats(t, h, s)
	p := plan(h)
	logged(h)
	if _, err := s.ReplacePlanEntitlements(t.Context(), h.OrgID(), refs.ByID(p.ID), []entitlements.EntitlementInput{input(f, "max", "2", "reset", "true")}); err != nil {
		t.Fatalf("ReplacePlanEntitlements() error = %v", err)
	}
	grant(t, h, s, p, f, "max", "3")
	if _, err := s.DeletePlanEntitlementPrivilege(t.Context(), h.OrgID(), refs.ByID(p.ID), refs.ByCode(f.Code), "reset"); err != nil {
		t.Fatalf("DeletePlanEntitlementPrivilege() error = %v", err)
	}
	if err := s.DeletePlanEntitlement(t.Context(), h.OrgID(), refs.ByID(p.ID), refs.ByCode(f.Code)); err != nil {
		t.Fatalf("DeletePlanEntitlement() error = %v", err)
	}
	w := billingtest.ActivityWant{Type: auditlog.ActivityPlanUpdated, ResourceType: auditlog.ResourcePlan, ResourceID: p.ID.UUID()}
	h.WantActivities(w, w, w, w)

	t.Run("a failed change logs nothing", func(t *testing.T) {
		h, s := setup(t)
		f := seats(t, h, s)
		p := plan(h)
		logged(h)
		if _, err := s.UpsertPlanEntitlement(t.Context(), h.OrgID(), refs.ByID(p.ID), input(f, "max", "many")); err == nil {
			t.Fatal("UpsertPlanEntitlement() error = nil, want an error")
		}
		h.WantActivities()
	})
}

func TestSubscriptionEntitlementActivityLogs(t *testing.T) {
	t.Log("source: spec/services/entitlement/subscription_entitlement_update_service_spec.rb:62")
	t.Log("source: spec/services/entitlement/subscription_feature_privilege_remove_service_spec.rb:67")
	t.Log("source: spec/services/entitlement/subscription_feature_remove_service_spec.rb:54")
	x := newSubFixture(t, true)
	logged(x.h)
	x.upsert(t, "max", "5")
	if _, err := x.s.DeleteSubscriptionEntitlementPrivilege(t.Context(), x.h.OrgID(), x.ref, refs.ByCode(x.f.Code), "reset"); err != nil {
		t.Fatalf("DeleteSubscriptionEntitlementPrivilege() error = %v", err)
	}
	if err := x.s.DeleteSubscriptionEntitlement(t.Context(), x.h.OrgID(), x.ref, refs.ByCode(x.f.Code)); err != nil {
		t.Fatalf("DeleteSubscriptionEntitlement() error = %v", err)
	}
	cust := billingtest.Read(x.h, func(ctx context.Context, r port.Repos) (string, error) {
		c, err := r.Customers.Get(ctx, x.h.OrgID(), x.sub.CustomerID)
		if err != nil {
			return "", err
		}
		return c.ExternalID, nil
	})
	w := billingtest.ActivityWant{
		Type: auditlog.ActivitySubscriptionUpdated, ResourceType: auditlog.ResourceSubscription, ResourceID: x.sub.ID.UUID(),
		ExternalCustomerID: cust, ExternalSubscriptionID: x.sub.ExternalID,
	}
	x.h.WantActivities(w, w, w)
}
