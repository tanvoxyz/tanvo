package entitlements_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/entitlements"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

func privilegeCodes(f *catalog.Feature) []string {
	var out []string
	for _, p := range f.Privileges {
		out = append(out, p.Code)
	}
	return out
}

func TestCreateFeature(t *testing.T) {
	t.Parallel()
	// source: spec/services/entitlement/feature_create_service_spec.rb:22
	t.Run("creates the feature and its privileges and emits feature.created", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		got, err := reloadFeature(t, h, f.ID)
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		if got.Code != "seats" || *got.Name != "Nb users" || got.Description != nil {
			t.Errorf("feature = %+v, want = seats Nb users", got)
		}
		if codes := privilegeCodes(got); !slices.Equal(codes, []string{"max", "reset", "signin"}) {
			t.Errorf("privileges = %v, want = [max reset signin]", codes)
		}
		if p, _ := got.Privilege("signin"); !slices.Equal(p.SelectOptions, []string{"password", "okta"}) {
			t.Errorf("select options = %v, want = [password okta]", p.SelectOptions)
		}
		if n := webhooks(h, webhook.EventTypeFeatureCreated); n != 1 {
			t.Errorf("feature.created events = %d, want = 1", n)
		}
	})
	// source: spec/services/entitlement/feature_create_service_spec.rb:33
	t.Run("trims the code", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f, err := s.CreateFeature(t.Context(), h.OrgID(), entitlements.CreateFeatureInput{Code: "  seats  "})
		if err != nil {
			t.Fatalf("CreateFeature() error = %v", err)
		}
		if f.Code != "seats" {
			t.Errorf("code = %q, want = seats", f.Code)
		}
	})
	// source: spec/services/entitlement/feature_create_service_spec.rb:112
	t.Run("defaults the value type to string", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := feature(t, h, s, "seats", "max_admins")
		if got := f.Privileges[0].ValueType; got != catalog.PrivilegeValueTypeString {
			t.Errorf("value type = %q, want = string", got)
		}
	})
	// source: spec/services/entitlement/feature_create_service_spec.rb:98
	t.Run("rejects a code already used", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		feature(t, h, s, "seats")
		_, err := s.CreateFeature(t.Context(), h.OrgID(), entitlements.CreateFeatureInput{Code: "seats"})
		wantFieldError(t, err, "code", core.CodeAlreadyExists)
	})
	cases := []struct {
		name        string
		source      string
		in          entitlements.CreateFeatureInput
		field, code string
	}{
		{
			name: "empty code", source: "feature_create_service_spec.rb:80",
			in:    entitlements.CreateFeatureInput{Code: "", Name: ptr("Number of seats")},
			field: "code", code: core.CodeRequired,
		},
		{
			name: "duplicated privilege code", source: "feature_create_service_spec.rb:132",
			in: entitlements.CreateFeatureInput{Code: "seats", Privileges: []entitlements.PrivilegeInput{
				{Code: "max_admins"}, {Code: "max_admins"},
			}},
			field: "privilege.code", code: entitlements.CodeDuplicated,
		},
		{
			name: "empty privilege code", source: "feature_create_service_spec.rb:173",
			in: entitlements.CreateFeatureInput{Code: "seats", Privileges: []entitlements.PrivilegeInput{
				{ValueType: catalog.PrivilegeValueTypeInteger},
			}},
			field: "privilege.code", code: core.CodeRequired,
		},
		{
			name: "select privilege without options", source: "feature_create_service_spec.rb:233",
			in: entitlements.CreateFeatureInput{Code: "seats", Privileges: []entitlements.PrivilegeInput{
				{Code: "signin", ValueType: catalog.PrivilegeValueTypeSelect},
			}},
			field: "privilege.select_options", code: core.CodeInvalidFormat,
		},
	}
	for _, tc := range cases {
		// source: spec/services/entitlement/<tc.source>
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, s := setup(t)
			_, err := s.CreateFeature(t.Context(), h.OrgID(), tc.in)
			wantFieldError(t, err, tc.field, tc.code)
			if n := webhooks(h, webhook.EventTypeFeatureCreated); n != 0 {
				t.Errorf("feature.created events = %d, want = 0", n)
			}
		})
	}
}

func TestUpdateFeature(t *testing.T) {
	t.Parallel()
	// source: spec/services/entitlement/feature_update_service_spec.rb:362
	t.Run("updates the name and description and emits feature.updated", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		got, err := s.UpdateFeature(t.Context(), h.OrgID(), f.ID, entitlements.UpdateFeatureInput{
			Name: core.Some(ptr("Users")), Description: core.Some(ptr("Seats of the account")),
		})
		if err != nil {
			t.Fatalf("UpdateFeature() error = %v", err)
		}
		if *got.Name != "Users" || *got.Description != "Seats of the account" {
			t.Errorf("feature = %+v, want = Users, Seats of the account", got)
		}
		if n := webhooks(h, webhook.EventTypeFeatureUpdated); n != 1 {
			t.Errorf("feature.updated events = %d, want = 1", n)
		}
	})
	// source: spec/services/entitlement/feature_update_service_spec.rb:379
	t.Run("keeps the attributes not given", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		got, err := s.UpdateFeature(t.Context(), h.OrgID(), f.ID, entitlements.UpdateFeatureInput{Description: core.Some(ptr("d"))})
		if err != nil {
			t.Fatalf("UpdateFeature() error = %v", err)
		}
		if *got.Name != "Nb users" {
			t.Errorf("name = %q, want = Nb users", *got.Name)
		}
	})
	// source: spec/services/entitlement/feature_update_service_spec.rb:401,409,419
	t.Run("updates the given privileges and keeps the others", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		got, err := s.UpdateFeature(t.Context(), h.OrgID(), f.ID, entitlements.UpdateFeatureInput{
			Privileges: []entitlements.PrivilegeInput{
				{Code: "max", Name: ptr("Maximum"), ValueType: catalog.PrivilegeValueTypeString},
				{Code: "reset"},
			},
		})
		if err != nil {
			t.Fatalf("UpdateFeature() error = %v", err)
		}
		got, _ = reloadFeature(t, h, got.ID)
		if codes := privilegeCodes(got); !slices.Equal(codes, []string{"max", "reset", "signin"}) {
			t.Errorf("privileges = %v, want = [max reset signin]", codes)
		}
		max, _ := got.Privilege("max")
		if *max.Name != "Maximum" || max.ValueType != catalog.PrivilegeValueTypeInteger || max.ID != f.Privileges[0].ID {
			t.Errorf("max = %+v, want = Maximum, integer, same ID", max)
		}
		if reset, _ := got.Privilege("reset"); *reset.Name != "Password Reset" {
			t.Errorf("reset name = %q, want = Password Reset", *reset.Name)
		}
	})
	// source: spec/services/entitlement/feature_update_service_spec.rb:445
	t.Run("appends the new select options", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		got, err := s.UpdateFeature(t.Context(), h.OrgID(), f.ID, entitlements.UpdateFeatureInput{
			Privileges: []entitlements.PrivilegeInput{{Code: "signin", SelectOptions: []string{"okta", "google"}}},
		})
		if err != nil {
			t.Fatalf("UpdateFeature() error = %v", err)
		}
		if p, _ := got.Privilege("signin"); !slices.Equal(p.SelectOptions, []string{"password", "okta", "google"}) {
			t.Errorf("select options = %v, want = [password okta google]", p.SelectOptions)
		}
	})
	// source: spec/services/entitlement/feature_update_service_spec.rb:525
	t.Run("creates a new privilege", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		got, err := s.UpdateFeature(t.Context(), h.OrgID(), f.ID, entitlements.UpdateFeatureInput{
			Privileges: []entitlements.PrivilegeInput{{Code: "new_privilege", Name: ptr("New Privilege")}},
		})
		if err != nil {
			t.Fatalf("UpdateFeature() error = %v", err)
		}
		p, ok := got.Privilege("new_privilege")
		if !ok || len(got.Privileges) != 4 || p.ValueType != catalog.PrivilegeValueTypeString {
			t.Errorf("privileges = %+v, want = 4 with new_privilege string", got.Privileges)
		}
	})
	// source: spec/services/entitlement/feature_update_service_spec.rb:496,507
	t.Run("sets empty names", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		got, err := s.UpdateFeature(t.Context(), h.OrgID(), f.ID, entitlements.UpdateFeatureInput{
			Name:       core.Some(ptr("")),
			Privileges: []entitlements.PrivilegeInput{{Code: "max", Name: ptr("")}},
		})
		if err != nil {
			t.Fatalf("UpdateFeature() error = %v", err)
		}
		if p, _ := got.Privilege("max"); *got.Name != "" || *p.Name != "" {
			t.Errorf("names = %q, %q, want = empty", *got.Name, *p.Name)
		}
	})
	// source: spec/services/entitlement/feature_update_service_spec.rb:565
	t.Run("emits plan.updated for the plans granting the feature", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		p1, p2 := plan(h), plan(h)
		grant(t, h, s, p1, f, "max", "10")
		grant(t, h, s, p2, f, "reset", "true")
		before := webhooks(h, webhook.EventTypePlanUpdated)
		if _, err := s.UpdateFeature(t.Context(), h.OrgID(), f.ID, entitlements.UpdateFeatureInput{}); err != nil {
			t.Fatalf("UpdateFeature() error = %v", err)
		}
		if n := webhooks(h, webhook.EventTypePlanUpdated) - before; n != 2 {
			t.Errorf("plan.updated events = %d, want = 2", n)
		}
	})
	errCases := []struct {
		name        string
		in          entitlements.UpdateFeatureInput
		field, code string
	}{
		// source: spec/services/entitlement/feature_update_service_spec.rb:334
		{"duplicated privilege code", entitlements.UpdateFeatureInput{Privileges: []entitlements.PrivilegeInput{
			{Code: "max"}, {Code: "max"},
		}}, "privilege.code", entitlements.CodeDuplicated},
		// source: spec/services/entitlement/feature_update_service_spec.rb:535
		{"invalid new privilege", entitlements.UpdateFeatureInput{Privileges: []entitlements.PrivilegeInput{
			{Code: "new_privilege", ValueType: "invalid_type"},
		}}, "privilege.value_type", core.CodeInvalid},
	}
	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, s := setup(t)
			f := seats(t, h, s)
			_, err := s.UpdateFeature(t.Context(), h.OrgID(), f.ID, tc.in)
			wantFieldError(t, err, tc.field, tc.code)
		})
	}
	// source: spec/services/entitlement/feature_update_service_spec.rb:475
	t.Run("missing feature", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		_, err := s.UpdateFeature(t.Context(), h.OrgID(), core.NewID[core.FeatureID](h.Deps.IDs), entitlements.UpdateFeatureInput{})
		wantNotFound(t, err, "feature")
	})
}

func TestDeleteFeature(t *testing.T) {
	t.Parallel()
	// source: spec/services/entitlement/feature_destroy_service_spec.rb:20,24,28,35
	t.Run("discards the feature and emits feature.deleted", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		got, err := s.DeleteFeature(t.Context(), h.OrgID(), f.ID)
		if err != nil {
			t.Fatalf("DeleteFeature() error = %v", err)
		}
		if got.DeletedAt == nil || got.ID != f.ID {
			t.Errorf("feature = %+v, want = discarded %s", got, f.ID)
		}
		if _, err := reloadFeature(t, h, f.ID); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("reload error = %v, want = not found", err)
		}
		if n := webhooks(h, webhook.EventTypeFeatureDeleted); n != 1 {
			t.Errorf("feature.deleted events = %d, want = 1", n)
		}
		// The code is free again.
		feature(t, h, s, "seats")
	})
	// source: spec/services/entitlement/feature_destroy_service_spec.rb:73
	t.Run("discards the entitlements and emits plan.updated", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		other := feature(t, h, s, "sso", "provider")
		p := plan(h)
		grant(t, h, s, p, f, "max", "10")
		grant(t, h, s, p, other, "provider", "okta")
		before := webhooks(h, webhook.EventTypePlanUpdated)
		if _, err := s.DeleteFeature(t.Context(), h.OrgID(), f.ID); err != nil {
			t.Fatalf("DeleteFeature() error = %v", err)
		}
		if ents := planEntitlements(t, h, p); len(ents) != 1 || ents[0].FeatureID != other.ID {
			t.Errorf("plan entitlements = %+v, want = only sso", ents)
		}
		if n := webhooks(h, webhook.EventTypePlanUpdated) - before; n != 1 {
			t.Errorf("plan.updated events = %d, want = 1", n)
		}
	})
	// source: spec/services/entitlement/feature_destroy_service_spec.rb:44
	t.Run("missing feature", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		_, err := s.DeleteFeature(t.Context(), h.OrgID(), core.NewID[core.FeatureID](h.Deps.IDs))
		wantNotFound(t, err, "feature")
	})
}

func TestDeleteFeaturePrivilege(t *testing.T) {
	t.Parallel()
	// source: spec/services/entitlement/privilege_destroy_service_spec.rb:17,21,28,55
	t.Run("discards the privilege and its values", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		p := plan(h)
		grant(t, h, s, p, f, "max", "10", "reset", "true")
		before := webhooks(h, webhook.EventTypePlanUpdated)
		got, err := s.DeleteFeaturePrivilege(t.Context(), h.OrgID(), f.ID, "max")
		if err != nil {
			t.Fatalf("DeleteFeaturePrivilege() error = %v", err)
		}
		if codes := privilegeCodes(got); !slices.Equal(codes, []string{"reset", "signin"}) {
			t.Errorf("privileges = %v, want = [reset signin]", codes)
		}
		equalValues(t, "plan values", planEntitlements(t, h, p)[0].Values, map[string]string{"reset": "true"})
		if n := webhooks(h, webhook.EventTypeFeatureUpdated); n != 1 {
			t.Errorf("feature.updated events = %d, want = 1", n)
		}
		if n := webhooks(h, webhook.EventTypePlanUpdated) - before; n != 1 {
			t.Errorf("plan.updated events = %d, want = 1", n)
		}
	})
	// source: spec/services/entitlement/privilege_destroy_service_spec.rb:37
	t.Run("missing privilege", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		f := seats(t, h, s)
		_, err := s.DeleteFeaturePrivilege(t.Context(), h.OrgID(), f.ID, "unknown")
		wantNotFound(t, err, "privilege")
	})
}

func TestListFeatures(t *testing.T) {
	t.Parallel()
	// source: spec/queries/entitlement/features_query_spec.rb:19,36,47,69
	h, s := setup(t)
	feature(t, h, s, "seats")
	h.Clock.Advance(time.Second)
	feature(t, h, s, "sso")
	h.Clock.Advance(time.Second)
	feature(t, h, s, "storage_seats")
	for _, tc := range []struct {
		search string
		want   []string
	}{
		{"", []string{"storage_seats", "sso", "seats"}},
		{"SEATS", []string{"storage_seats", "seats"}},
		{"none", nil},
	} {
		page, err := s.ListFeatures(t.Context(), h.OrgID(), tc.search, core.PageRequest{})
		if err != nil {
			t.Fatalf("ListFeatures() error = %v", err)
		}
		var got []string
		for _, f := range page.Items {
			got = append(got, f.Code)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("ListFeatures(%q) = %v, want = %v", tc.search, got, tc.want)
		}
	}
}
