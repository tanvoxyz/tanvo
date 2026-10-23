package entitlements_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/entitlements"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var now = time.Date(2025, 6, 2, 10, 0, 0, 0, time.UTC)

func setup(t *testing.T) (*billingtest.Harness, *entitlements.Service) {
	t.Helper()
	h := billingtest.New(t, now)
	s, err := entitlements.New(h.Deps)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return h, s
}

func ptr[T any](v T) *T { return &v }

// seats creates the feature seats with the privileges max (integer),
// reset (boolean) and signin (select of password and okta).
func seats(t *testing.T, h *billingtest.Harness, s *entitlements.Service) *catalog.Feature {
	t.Helper()
	f, err := s.CreateFeature(t.Context(), h.OrgID(), entitlements.CreateFeatureInput{
		Code: "seats", Name: ptr("Nb users"),
		Privileges: []entitlements.PrivilegeInput{
			{Code: "max", Name: ptr("Max"), ValueType: catalog.PrivilegeValueTypeInteger},
			{Code: "reset", Name: ptr("Password Reset"), ValueType: catalog.PrivilegeValueTypeBoolean},
			{Code: "signin", Name: ptr("Sign In"), ValueType: catalog.PrivilegeValueTypeSelect, SelectOptions: []string{"password", "okta"}},
		},
	})
	if err != nil {
		t.Fatalf("CreateFeature() error = %v", err)
	}
	return f
}

// feature creates a feature of string privileges.
func feature(t *testing.T, h *billingtest.Harness, s *entitlements.Service, code string, privileges ...string) *catalog.Feature {
	t.Helper()
	in := entitlements.CreateFeatureInput{Code: code}
	for _, p := range privileges {
		in.Privileges = append(in.Privileges, entitlements.PrivilegeInput{Code: p})
	}
	f, err := s.CreateFeature(t.Context(), h.OrgID(), in)
	if err != nil {
		t.Fatalf("CreateFeature() error = %v", err)
	}
	return f
}

func plan(h *billingtest.Harness) *catalog.Plan { return h.Plan(period.Monthly, 1000) }

// grant sets the plan's values of the feature.
func grant(t *testing.T, h *billingtest.Harness, s *entitlements.Service, p *catalog.Plan, f *catalog.Feature, kv ...string) {
	t.Helper()
	if _, err := s.UpsertPlanEntitlement(t.Context(), h.OrgID(), refs.ByID(p.ID), input(f, kv...)); err != nil {
		t.Fatalf("UpsertPlanEntitlement() error = %v", err)
	}
}

// input is the entitlement of f with the values kv (code, value, …).
func input(f *catalog.Feature, kv ...string) entitlements.EntitlementInput {
	in := entitlements.EntitlementInput{Feature: refs.ByCode(f.Code)}
	for i := 0; i+1 < len(kv); i += 2 {
		in.Values = append(in.Values, entitlements.ValueInput{Code: kv[i], Value: kv[i+1]})
	}
	return in
}

// values maps the privilege codes of e to their values.
func values(e entitlements.Entitlement) map[string]string {
	out := map[string]string{}
	for _, v := range e.Values {
		out[v.Privilege.Code] = v.Value
	}
	return out
}

// byCode maps the feature codes of es to their values.
func byCode(es []entitlements.Entitlement) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, e := range es {
		out[e.Feature.Code] = values(e)
	}
	return out
}

func equalValues(t *testing.T, what string, got, want map[string]string) {
	t.Helper()
	if !maps.Equal(got, want) {
		t.Errorf("%s = %v, want = %v", what, got, want)
	}
}

func planEntitlements(t *testing.T, h *billingtest.Harness, p *catalog.Plan) []*catalog.Entitlement {
	t.Helper()
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*catalog.Entitlement, error) {
		return r.Entitlements.ListByPlan(ctx, h.OrgID(), p.ID)
	})
}

func subEntitlements(t *testing.T, h *billingtest.Harness, id core.SubscriptionID) []*catalog.Entitlement {
	t.Helper()
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*catalog.Entitlement, error) {
		return r.Entitlements.ListBySubscription(ctx, h.OrgID(), id)
	})
}

func removals(t *testing.T, h *billingtest.Harness, id core.SubscriptionID) []string {
	t.Helper()
	rms := billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*catalog.EntitlementRemoval, error) {
		return r.Entitlements.ListRemovalsBySubscription(ctx, h.OrgID(), id)
	})
	var out []string
	for _, rm := range rms {
		out = append(out, rm.PrivilegeCode)
	}
	slices.Sort(out)
	return out
}

// addRemoval stores a removal of f (code "") or of its privilege code.
func addRemoval(t *testing.T, h *billingtest.Harness, sub core.SubscriptionID, f *catalog.Feature, code string) {
	t.Helper()
	h.Do(func(ctx context.Context, r port.Repos) error {
		return r.Entitlements.CreateRemoval(ctx, &catalog.EntitlementRemoval{
			ID: core.NewID[core.EntitlementRemovalID](h.Deps.IDs), OrganizationID: h.OrgID(),
			SubscriptionID: sub, FeatureID: f.ID, PrivilegeCode: code, CreatedAt: now,
		})
	})
}

func reloadFeature(t *testing.T, h *billingtest.Harness, id core.FeatureID) (*catalog.Feature, error) {
	t.Helper()
	var out *catalog.Feature
	err := h.Deps.Tx.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
		f, err := r.Features.Get(ctx, h.OrgID(), id)
		out = f
		return err
	})
	return out, err
}

// wantFieldError fails unless err carries exactly the field error.
func wantFieldError(t *testing.T, err error, field, code string) {
	t.Helper()
	var ve *core.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a validation error", err)
	}
	if len(ve.Errors) != 1 || !ve.Has(field, code) {
		t.Fatalf("errors = %v, want = %s %s", ve.Errors, field, code)
	}
}

// wantNotFound fails unless err is core.NotFound(resource).
func wantNotFound(t *testing.T, err error, resource string) {
	t.Helper()
	var nf *core.NotFoundError
	if !errors.As(err, &nf) || nf.Resource != resource {
		t.Fatalf("error = %v, want = %s not found", err, resource)
	}
}

func webhooks(h *billingtest.Harness, t webhook.EventType) int {
	return len(slices.DeleteFunc(h.Webhooks(), func(e webhook.EventType) bool { return e != t }))
}
