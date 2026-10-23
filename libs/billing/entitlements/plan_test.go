package entitlements_test

import (
	"context"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/entitlements"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

func TestReplacePlanEntitlements(t *testing.T) {
	t.Parallel()
	// source: spec/services/entitlement/plan_entitlements_update_service-full_spec.rb:26,30,34,38,43
	t.Run("creates the entitlements and emits plan.updated", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		p := plan(h)
		got, err := s.ReplacePlanEntitlements(t.Context(), h.OrgID(), refs.ByCode(p.Code), []entitlements.EntitlementInput{
			input(f, "max", "25", "reset", "true", "signin", "okta"),
		})
		if err != nil {
			t.Fatalf("ReplacePlanEntitlements() error = %v", err)
		}
		equalValues(t, "seats", byCode(got)["seats"], map[string]string{"max": "25", "reset": "true", "signin": "okta"})
		if len(got[0].Values) != 3 || got[0].Values[0].Privilege.Code != "max" || got[0].Values[0].Source != "" {
			t.Errorf("values = %+v, want = in privilege order, no source", got[0].Values)
		}
		if n := webhooks(h, webhook.EventTypePlanUpdated); n != 1 {
			t.Errorf("plan.updated events = %d, want = 1", n)
		}
	})
	// source: spec/services/entitlement/plan_entitlements_update_service-full_spec.rb:75
	t.Run("removes the features not given", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		other := feature(t, h, s, "sso", "provider")
		p := plan(h)
		grant(t, h, s, p, other, "provider", "okta")
		got, err := s.ReplacePlanEntitlements(t.Context(), h.OrgID(), refs.ByID(p.ID), []entitlements.EntitlementInput{input(f, "max", "25")})
		if err != nil {
			t.Fatalf("ReplacePlanEntitlements() error = %v", err)
		}
		if len(got) != 1 || got[0].Feature.Code != "seats" {
			t.Errorf("entitlements = %v, want = only seats", byCode(got))
		}
		if ents := planEntitlements(t, h, p); len(ents) != 1 {
			t.Errorf("stored entitlements = %d, want = 1", len(ents))
		}
	})
	// source: app/services/entitlement/plan_entitlements_update_service.rb:72-76
	t.Run("removes the values not given of a given feature", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		p := plan(h)
		grant(t, h, s, p, f, "max", "10", "reset", "true")
		got, err := s.ReplacePlanEntitlements(t.Context(), h.OrgID(), refs.ByID(p.ID), []entitlements.EntitlementInput{input(f, "max", "25")})
		if err != nil {
			t.Fatalf("ReplacePlanEntitlements() error = %v", err)
		}
		equalValues(t, "seats", byCode(got)["seats"], map[string]string{"max": "25"})
	})
	// source: spec/services/entitlement/plan_entitlements_update_service-full_spec.rb:100,180
	t.Run("empty input and features without values", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		p := plan(h)
		got, err := s.ReplacePlanEntitlements(t.Context(), h.OrgID(), refs.ByID(p.ID), nil)
		if err != nil || len(got) != 0 {
			t.Fatalf("ReplacePlanEntitlements(nil) = %v, %v, want = none", got, err)
		}
		got, err = s.ReplacePlanEntitlements(t.Context(), h.OrgID(), refs.ByID(p.ID), []entitlements.EntitlementInput{input(f)})
		if err != nil || len(got) != 1 || len(got[0].Values) != 0 {
			t.Fatalf("ReplacePlanEntitlements(seats) = %v, %v, want = seats without values", got, err)
		}
	})
	errCases := []struct {
		name        string
		source      string
		kv          []string
		field, code string
	}{
		{"invalid integer", "plan_entitlements_update_service-full_spec.rb:226", []string{"max", "12.5"}, "max_privilege_value", catalog.CodePrivilegeValueInvalid},
		{"missing value", "plan_entitlements_update_service-full_spec.rb:242", []string{"max", ""}, "privilege.value", core.CodeRequired},
		{"not a select option", "plan_entitlements_update_service-full_spec.rb:258", []string{"signin", "slack"}, "signin_privilege_value", catalog.CodePrivilegeValueNotInSelectOptions},
		{"invalid boolean", "app/services/entitlement/concerns/create_or_update_concern.rb:20", []string{"reset", "yes"}, "reset_privilege_value", catalog.CodePrivilegeValueInvalid},
	}
	for _, tc := range errCases {
		// source: spec/services/entitlement/<tc.source>
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, s := setup(t)
			f := seats(t, h, s)
			p := plan(h)
			_, err := s.ReplacePlanEntitlements(t.Context(), h.OrgID(), refs.ByID(p.ID), []entitlements.EntitlementInput{input(f, tc.kv...)})
			wantFieldError(t, err, tc.field, tc.code)
			if ents := planEntitlements(t, h, p); len(ents) != 0 {
				t.Errorf("stored entitlements = %d, want = 0", len(ents))
			}
		})
	}
	// source: spec/services/entitlement/plan_entitlements_update_service-full_spec.rb:141,156,171
	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		p := plan(h)
		_, err := s.ReplacePlanEntitlements(t.Context(), h.OrgID(), refs.ByID(p.ID), []entitlements.EntitlementInput{{Feature: refs.ByCode("unknown")}})
		wantNotFound(t, err, "feature")
		_, err = s.ReplacePlanEntitlements(t.Context(), h.OrgID(), refs.ByID(p.ID), []entitlements.EntitlementInput{input(f, "unknown", "1")})
		wantNotFound(t, err, "privilege")
		_, err = s.ReplacePlanEntitlements(t.Context(), h.OrgID(), refs.ByCode("unknown"), nil)
		wantNotFound(t, err, "plan")
	})
}

func TestUpsertPlanEntitlement(t *testing.T) {
	t.Parallel()
	// source: spec/services/entitlement/plan_entitlements_update_service-partial_spec.rb:35,39,43,60
	t.Run("updates and adds values, keeping the others", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		p := plan(h)
		grant(t, h, s, p, f, "max", "10", "reset", "true")
		got, err := s.UpsertPlanEntitlement(t.Context(), h.OrgID(), refs.ByID(p.ID), input(f, "max", "25", "signin", "okta"))
		if err != nil {
			t.Fatalf("UpsertPlanEntitlement() error = %v", err)
		}
		equalValues(t, "seats", values(got), map[string]string{"max": "25", "reset": "true", "signin": "okta"})
		if ents := planEntitlements(t, h, p); len(ents) != 1 {
			t.Errorf("stored entitlements = %d, want = 1", len(ents))
		}
		if n := webhooks(h, webhook.EventTypePlanUpdated); n != 2 {
			t.Errorf("plan.updated events = %d, want = 2", n)
		}
	})
	// source: spec/services/entitlement/plan_entitlements_update_service-partial_spec.rb:81
	t.Run("keeps the other features", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		other := feature(t, h, s, "sso", "provider")
		p := plan(h)
		grant(t, h, s, p, other, "provider", "okta")
		grant(t, h, s, p, f, "max", "10")
		got, err := s.ListPlanEntitlements(t.Context(), h.OrgID(), refs.ByID(p.ID))
		if err != nil {
			t.Fatalf("ListPlanEntitlements() error = %v", err)
		}
		if b := byCode(got); len(b) != 2 || b["sso"]["provider"] != "okta" || b["seats"]["max"] != "10" {
			t.Errorf("entitlements = %v, want = sso and seats", b)
		}
	})
	// source: spec/services/entitlement/plan_entitlements_update_service-partial_spec.rb:144
	t.Run("an override plan is not found", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		parent := plan(h)
		child := plan(h)
		h.Do(func(ctx context.Context, r port.Repos) error {
			child.ParentID = parent.ID
			return r.Plans.Update(ctx, child)
		})
		_, err := s.UpsertPlanEntitlement(t.Context(), h.OrgID(), refs.ByID(child.ID), input(f, "max", "1"))
		wantNotFound(t, err, "plan")
	})
}

func TestPlanEntitlementGetAndDelete(t *testing.T) {
	t.Parallel()
	// source: app/controllers/api/v1/plans/entitlements_controller.rb:24-31,111-119
	t.Run("get", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		other := feature(t, h, s, "sso")
		p := plan(h)
		grant(t, h, s, p, f, "max", "10")
		got, err := s.GetPlanEntitlement(t.Context(), h.OrgID(), refs.ByID(p.ID), refs.ByCode("seats"))
		if err != nil {
			t.Fatalf("GetPlanEntitlement() error = %v", err)
		}
		equalValues(t, "seats", values(got), map[string]string{"max": "10"})
		_, err = s.GetPlanEntitlement(t.Context(), h.OrgID(), refs.ByID(p.ID), refs.ByID(other.ID))
		wantNotFound(t, err, "entitlement")
		_, err = s.GetPlanEntitlement(t.Context(), h.OrgID(), refs.ByID(p.ID), refs.ByCode("unknown"))
		wantNotFound(t, err, "entitlement")
	})
	// source: spec/services/entitlement/plan_entitlement_destroy_service_spec.rb:20,24,28,36
	t.Run("delete", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		p := plan(h)
		grant(t, h, s, p, f, "max", "10")
		if err := s.DeletePlanEntitlement(t.Context(), h.OrgID(), refs.ByID(p.ID), refs.ByCode("seats")); err != nil {
			t.Fatalf("DeletePlanEntitlement() error = %v", err)
		}
		if ents := planEntitlements(t, h, p); len(ents) != 0 {
			t.Errorf("stored entitlements = %d, want = 0", len(ents))
		}
		if n := webhooks(h, webhook.EventTypePlanUpdated); n != 2 {
			t.Errorf("plan.updated events = %d, want = 2", n)
		}
		// source: spec/services/entitlement/plan_entitlement_destroy_service_spec.rb:45
		err := s.DeletePlanEntitlement(t.Context(), h.OrgID(), refs.ByID(p.ID), refs.ByCode("seats"))
		wantNotFound(t, err, "entitlement")
	})
	// source: spec/services/entitlement/plan_entitlement_privilege_destroy_service_spec.rb:24,28,32,38,47
	t.Run("delete privilege", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		p := plan(h)
		grant(t, h, s, p, f, "max", "10", "reset", "true")
		got, err := s.DeletePlanEntitlementPrivilege(t.Context(), h.OrgID(), refs.ByID(p.ID), refs.ByCode("seats"), "max")
		if err != nil {
			t.Fatalf("DeletePlanEntitlementPrivilege() error = %v", err)
		}
		equalValues(t, "seats", values(got), map[string]string{"reset": "true"})
		equalValues(t, "stored", planEntitlements(t, h, p)[0].Values, map[string]string{"reset": "true"})
		if n := webhooks(h, webhook.EventTypePlanUpdated); n != 2 {
			t.Errorf("plan.updated events = %d, want = 2", n)
		}
		// source: spec/services/entitlement/plan_entitlement_privilege_destroy_service_spec.rb:65,74
		_, err = s.DeletePlanEntitlementPrivilege(t.Context(), h.OrgID(), refs.ByID(p.ID), refs.ByCode("seats"), "max")
		wantNotFound(t, err, "privilege")
		// source: spec/services/entitlement/plan_entitlement_privilege_destroy_service_spec.rb:56
		_, err = s.DeletePlanEntitlementPrivilege(t.Context(), h.OrgID(), refs.ByID(p.ID), refs.ByCode("sso"), "max")
		wantNotFound(t, err, "entitlement")
	})
}
