package usageattribution_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/usageattribution"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/attribution"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

var now = time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)

type fixture struct {
	h *billingtest.Harness
	s *usageattribution.Service
}

func setup(t *testing.T) fixture {
	t.Helper()
	h := billingtest.New(t, now)
	s, err := usageattribution.New(h.Deps)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return fixture{h, s}
}

func ptr[T any](v T) *T { return &v }

var none core.UsageAttributionTypeID

// create creates a type (the factory of spec/factories/usage_attribution_types.rb:
// hierarchical unless flat is asked).
func (f fixture) create(t *testing.T, code string, role attribution.Role, parent core.UsageAttributionTypeID, keys ...string) *attribution.Type {
	t.Helper()
	if len(keys) == 0 {
		keys = []string{code + "_id"}
	}
	e, err := f.s.Create(t.Context(), f.h.OrgID(), usageattribution.CreateInput{
		Code: code, Name: ptr(strings.ToUpper(code[:1]) + code[1:]), AttributionKeys: keys, Role: role, ParentID: parent,
	})
	if err != nil {
		t.Fatalf("Create(%s) error = %v", code, err)
	}
	f.h.Clock.Advance(time.Second)
	return e.Type
}

// otherOrg creates a second organization and a type in it.
func (f fixture) otherOrgType(t *testing.T, code string, keys ...string) *attribution.Type {
	t.Helper()
	ids := core.UUIDv7Generator{}
	o := organization.New(core.NewID[core.OrganizationID](ids), "Other", now)
	f.h.Do(func(ctx context.Context, r port.Repos) error { return r.Organizations.Create(ctx, o) })
	e, err := f.s.Create(t.Context(), o.ID, usageattribution.CreateInput{Code: code, AttributionKeys: keys, Role: attribution.RoleHierarchical})
	if err != nil {
		t.Fatalf("Create(other org) error = %v", err)
	}
	return e.Type
}

// value records a value of typ.
func (f fixture) value(t *testing.T, typ *attribution.Type) *attribution.Value {
	t.Helper()
	c := f.h.Customer()
	v := &attribution.Value{
		ID: core.NewID[core.UsageAttributionValueID](f.h.Deps.IDs), OrganizationID: f.h.OrgID(), TypeID: typ.ID,
		CustomerID: c.ID, Value: "u-1", CreatedAt: now, UpdatedAt: now,
	}
	f.h.Do(func(ctx context.Context, r port.Repos) error { return r.UsageAttributionValues.Create(ctx, v) })
	return v
}

func (f fixture) reload(t *testing.T, id core.UsageAttributionTypeID) *attribution.Type {
	t.Helper()
	m := billingtest.Read(f.h, func(ctx context.Context, r port.Repos) (map[core.UsageAttributionTypeID]*attribution.Type, error) {
		return r.UsageAttributionTypes.GetMany(ctx, f.h.OrgID(), []core.UsageAttributionTypeID{id})
	})
	return m[id]
}

// wantErrors fails unless err is a validation error with exactly want
// (field, code pairs, in order).
func wantErrors(t *testing.T, err error, want ...core.FieldError) {
	t.Helper()
	var ve *core.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error got = %v, want = a validation error", err)
	}
	if !slices.Equal(ve.Errors, want) {
		t.Fatalf("errors got = %v, want = %v", ve.Errors, want)
	}
}

// wantHas fails unless err is a validation error carrying field code.
func wantHas(t *testing.T, err error, field, code string) {
	t.Helper()
	var ve *core.ValidationError
	if !errors.As(err, &ve) || !ve.Has(field, code) {
		t.Fatalf("error got = %v, want = %s %s", err, field, code)
	}
}

func wantNotFound(t *testing.T, err error, resource string) {
	t.Helper()
	var nf *core.NotFoundError
	if !errors.As(err, &nf) || nf.Resource != resource {
		t.Fatalf("error got = %v, want = not found %s", err, resource)
	}
}

func TestCreate(t *testing.T) {
	t.Parallel()
	f := setup(t)
	parent := f.create(t, "account", attribution.RoleHierarchical, none)
	flat := f.create(t, "model", attribution.RoleFlat, none)
	f.create(t, "taken", attribution.RoleHierarchical, none, "taken_id", "shared_id")
	foreign := f.otherOrgType(t, "foreign", "foreign_id")

	cases := []struct {
		name   string
		source string
		in     usageattribution.CreateInput
		check  func(t *testing.T, e *usageattribution.Entry, err error)
	}{
		{
			name: "creates", source: "spec/services/usage_attribution_types/create_service_spec.rb:19",
			in: usageattribution.CreateInput{Code: "user", Name: ptr("User"), Description: ptr("A person using the product"), AttributionKeys: []string{"user_id"}, Role: attribution.RoleHierarchical},
			check: func(t *testing.T, e *usageattribution.Entry, err error) {
				if err != nil {
					t.Fatalf("error = %v", err)
				}
				x := e.Type
				if x.Code != "user" || *x.Name != "User" || *x.Description != "A person using the product" ||
					!slices.Equal(x.AttributionKeys, []string{"user_id"}) || x.Role != attribution.RoleHierarchical || e.Parent != nil {
					t.Errorf("type got = %+v", x)
				}
				if got := f.reload(t, x.ID); got == nil || got.OrganizationID != f.h.OrgID() {
					t.Errorf("stored got = %+v", got)
				}
			},
		},
		{
			name: "strips", source: "spec/services/usage_attribution_types/create_service_spec.rb:32",
			in: usageattribution.CreateInput{Code: "  user2 ", AttributionKeys: []string{" user2_id ", "", "user2_id"}, Role: attribution.RoleHierarchical},
			check: func(t *testing.T, e *usageattribution.Entry, err error) {
				if err != nil {
					t.Fatalf("error = %v", err)
				}
				if e.Type.Code != "user2" || !slices.Equal(e.Type.AttributionKeys, []string{"user2_id"}) {
					t.Errorf("code, keys got = %q, %v, want = user2, [user2_id]", e.Type.Code, e.Type.AttributionKeys)
				}
			},
		},
		{
			name: "several keys", source: "spec/services/usage_attribution_types/create_service_spec.rb:40",
			in: usageattribution.CreateInput{Code: "user3", AttributionKeys: []string{"u_id", "uId", "usr_id"}, Role: attribution.RoleHierarchical},
			check: func(t *testing.T, e *usageattribution.Entry, err error) {
				if err != nil || !slices.Equal(e.Type.AttributionKeys, []string{"u_id", "uId", "usr_id"}) {
					t.Errorf("got = %v, %v", e, err)
				}
			},
		},
		{
			name: "no description", source: "spec/services/usage_attribution_types/create_service_spec.rb:49",
			in: usageattribution.CreateInput{Code: "user4", AttributionKeys: []string{"user4_id"}, Role: attribution.RoleHierarchical},
			check: func(t *testing.T, e *usageattribution.Entry, err error) {
				if err != nil || e.Type.Description != nil {
					t.Errorf("got = %v, %v, want = nil description", e, err)
				}
			},
		},
		{
			name: "parent", source: "spec/services/usage_attribution_types/create_service_spec.rb:69",
			in: usageattribution.CreateInput{Code: "team", AttributionKeys: []string{"team_id"}, Role: attribution.RoleHierarchical, ParentID: parent.ID},
			check: func(t *testing.T, e *usageattribution.Entry, err error) {
				if err != nil || e.Type.ParentID != parent.ID || e.Parent.Code != "account" {
					t.Errorf("got = %v, %v, want = parent account", e, err)
				}
			},
		},
		{
			name: "missing parent", source: "spec/services/usage_attribution_types/create_service_spec.rb:78",
			in: usageattribution.CreateInput{Code: "x1", AttributionKeys: []string{"x1"}, Role: attribution.RoleHierarchical, ParentID: core.NewID[core.UsageAttributionTypeID](f.h.Deps.IDs)},
			check: func(t *testing.T, _ *usageattribution.Entry, err error) {
				wantNotFound(t, err, usageattribution.ParentResource)
			},
		},
		{
			name: "parent of another organization", source: "spec/services/usage_attribution_types/create_service_spec.rb:89",
			in: usageattribution.CreateInput{Code: "x2", AttributionKeys: []string{"x2"}, Role: attribution.RoleHierarchical, ParentID: foreign.ID},
			check: func(t *testing.T, _ *usageattribution.Entry, err error) {
				wantNotFound(t, err, usageattribution.ParentResource)
			},
		},
		{
			name: "flat with parent", source: "spec/services/usage_attribution_types/create_service_spec.rb:103",
			in: usageattribution.CreateInput{Code: "x3", AttributionKeys: []string{"x3"}, Role: attribution.RoleFlat, ParentID: parent.ID},
			check: func(t *testing.T, _ *usageattribution.Entry, err error) {
				wantErrors(t, err, core.FieldError{Field: "parent_id", Code: attribution.CodeForbiddenForFlatRole})
			},
		},
		{
			name: "flat parent", source: "spec/services/usage_attribution_types/create_service_spec.rb:114",
			in: usageattribution.CreateInput{Code: "x4", AttributionKeys: []string{"x4"}, Role: attribution.RoleHierarchical, ParentID: flat.ID},
			check: func(t *testing.T, _ *usageattribution.Entry, err error) {
				wantErrors(t, err, core.FieldError{Field: "parent_id", Code: attribution.CodeMustBeHierarchical})
			},
		},
		{
			name: "unsupported role", source: "spec/services/usage_attribution_types/create_service_spec.rb:123",
			in: usageattribution.CreateInput{Code: "x5", AttributionKeys: []string{"x5"}, Role: "tree"},
			check: func(t *testing.T, _ *usageattribution.Entry, err error) {
				wantErrors(t, err, core.FieldError{Field: "role", Code: core.CodeInvalid})
			},
		},
		{
			name: "code taken", source: "spec/services/usage_attribution_types/create_service_spec.rb:132",
			in: usageattribution.CreateInput{Code: "account", AttributionKeys: []string{"x6"}, Role: attribution.RoleHierarchical},
			check: func(t *testing.T, _ *usageattribution.Entry, err error) {
				wantErrors(t, err, core.FieldError{Field: "code", Code: core.CodeAlreadyExists})
			},
		},
		{
			name: "key taken", source: "spec/services/usage_attribution_types/create_service_spec.rb:141",
			in: usageattribution.CreateInput{Code: "x7", AttributionKeys: []string{"taken_id"}, Role: attribution.RoleHierarchical},
			check: func(t *testing.T, _ *usageattribution.Entry, err error) {
				wantErrors(t, err, core.FieldError{Field: "attribution_keys", Code: core.CodeAlreadyExists})
			},
		},
		{
			name: "one key taken", source: "spec/services/usage_attribution_types/create_service_spec.rb:153",
			in: usageattribution.CreateInput{Code: "x8", AttributionKeys: []string{"fresh_id", "shared_id"}, Role: attribution.RoleHierarchical},
			check: func(t *testing.T, _ *usageattribution.Entry, err error) {
				wantErrors(t, err, core.FieldError{Field: "attribution_keys", Code: core.CodeAlreadyExists})
			},
		},
		{
			name: "too many keys", source: "spec/services/usage_attribution_types/create_service_spec.rb:162",
			in: usageattribution.CreateInput{Code: "x9", AttributionKeys: []string{"a", "b", "c", "d", "e"}, Role: attribution.RoleHierarchical},
			check: func(t *testing.T, _ *usageattribution.Entry, err error) {
				wantErrors(t, err, core.FieldError{Field: "attribution_keys", Code: core.CodeTooLong})
			},
		},
		{
			name: "missing code", source: "spec/services/usage_attribution_types/create_service_spec.rb:171",
			in: usageattribution.CreateInput{Code: " ", AttributionKeys: []string{"x10"}, Role: attribution.RoleHierarchical},
			check: func(t *testing.T, _ *usageattribution.Entry, err error) {
				wantErrors(t, err, core.FieldError{Field: "code", Code: core.CodeRequired})
			},
		},
		{
			name: "missing key", source: "spec/services/usage_attribution_types/create_service_spec.rb:180",
			in: usageattribution.CreateInput{Code: "x11", AttributionKeys: []string{" "}, Role: attribution.RoleHierarchical},
			check: func(t *testing.T, _ *usageattribution.Entry, err error) {
				wantErrors(t, err, core.FieldError{Field: "attribution_keys", Code: core.CodeRequired})
			},
		},
		{
			name: "same key in another organization", source: "spec/models/usage_attribution_type_spec.rb:94",
			in: usageattribution.CreateInput{Code: "x12", AttributionKeys: []string{"foreign_id"}, Role: attribution.RoleHierarchical},
			check: func(t *testing.T, _ *usageattribution.Entry, err error) {
				if err != nil {
					t.Errorf("error = %v, want = nil", err)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Log("source:", c.source)
			e, err := f.s.Create(t.Context(), f.h.OrgID(), c.in)
			c.check(t, e, err)
		})
	}
}

func TestUpdate(t *testing.T) {
	t.Parallel()
	// The subject of spec/services/usage_attribution_types/update_service_spec.rb:9-18.
	user := func(t *testing.T, f fixture) *attribution.Type {
		e, err := f.s.Create(t.Context(), f.h.OrgID(), usageattribution.CreateInput{
			Code: "user", Name: ptr("User"), Description: ptr("A person using the product"),
			AttributionKeys: []string{"user_id"}, Role: attribution.RoleHierarchical,
		})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		return e.Type
	}
	update := func(t *testing.T, f fixture, id core.UsageAttributionTypeID, in usageattribution.UpdateInput) (*attribution.Type, error) {
		e, err := f.s.Update(t.Context(), f.h.OrgID(), id, in)
		if err != nil {
			return nil, err
		}
		return e.Type, nil
	}
	mustUpdate := func(t *testing.T, f fixture, id core.UsageAttributionTypeID, in usageattribution.UpdateInput) *attribution.Type {
		t.Helper()
		x, err := update(t, f, id, in)
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		return x
	}
	frozenAll := usageattribution.UpdateInput{
		Name: core.Some(ptr("Member")), Description: core.Some(ptr("A member of a team")), Code: core.Some("member"),
		AttributionKeys: core.Some([]string{"employee_id"}), Role: core.Some(attribution.RoleFlat),
	}
	frozenErrors := []core.FieldError{
		{Field: "code", Code: attribution.CodeUsageAlreadyAttributed},
		{Field: "role", Code: attribution.CodeUsageAlreadyAttributed},
		{Field: "parent_id", Code: attribution.CodeUsageAlreadyAttributed},
	}

	t.Run("name", func(t *testing.T) {
		t.Parallel()
		// source: spec/services/usage_attribution_types/update_service_spec.rb:21
		// source: spec/services/usage_attribution_types/update_service_spec.rb:26
		f := setup(t)
		u := user(t, f)
		x := mustUpdate(t, f, u.ID, usageattribution.UpdateInput{Name: core.Some(ptr("Member"))})
		if *x.Name != "Member" || x.Code != "user" || *x.Description != "A person using the product" ||
			!slices.Equal(x.AttributionKeys, []string{"user_id"}) || x.Role != attribution.RoleHierarchical {
			t.Errorf("got = %+v", x)
		}
	})
	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		// source: spec/services/usage_attribution_types/update_service_spec.rb:36
		f := setup(t)
		_, err := update(t, f, core.NewID[core.UsageAttributionTypeID](f.h.Deps.IDs), usageattribution.UpdateInput{})
		wantNotFound(t, err, usageattribution.Resource)
	})
	t.Run("description", func(t *testing.T) {
		t.Parallel()
		// source: spec/services/usage_attribution_types/update_service_spec.rb:45
		// source: spec/services/usage_attribution_types/update_service_spec.rb:53
		// source: spec/services/usage_attribution_types/update_service_spec.rb:62
		f := setup(t)
		u := user(t, f)
		x := mustUpdate(t, f, u.ID, usageattribution.UpdateInput{Description: core.Some(ptr("A member of a team"))})
		if *f.reload(t, x.ID).Description != "A member of a team" {
			t.Errorf("description not updated")
		}
		mustUpdate(t, f, u.ID, usageattribution.UpdateInput{Description: core.Some[*string](nil)})
		if f.reload(t, u.ID).Description != nil {
			t.Errorf("description not cleared")
		}
		f.value(t, u)
		mustUpdate(t, f, u.ID, usageattribution.UpdateInput{Description: core.Some(ptr("A member of a team"))})
		if *f.reload(t, u.ID).Description != "A member of a team" {
			t.Errorf("description not updated once attributed")
		}
	})
	t.Run("attribution keys", func(t *testing.T) {
		t.Parallel()
		// source: spec/services/usage_attribution_types/update_service_spec.rb:72
		// source: spec/services/usage_attribution_types/update_service_spec.rb:77
		// source: spec/services/usage_attribution_types/update_service_spec.rb:87
		f := setup(t)
		u := user(t, f)
		x := mustUpdate(t, f, u.ID, usageattribution.UpdateInput{AttributionKeys: core.Some([]string{" employee_id "})})
		if !slices.Equal(x.AttributionKeys, []string{"employee_id"}) {
			t.Errorf("keys got = %v, want = [employee_id]", x.AttributionKeys)
		}
		x = mustUpdate(t, f, u.ID, usageattribution.UpdateInput{AttributionKeys: core.Some([]string{"user_id", "userId", "usr_id"})})
		if !slices.Equal(x.AttributionKeys, []string{"user_id", "userId", "usr_id"}) {
			t.Errorf("keys got = %v", x.AttributionKeys)
		}
		f.value(t, u)
		x = mustUpdate(t, f, u.ID, usageattribution.UpdateInput{AttributionKeys: core.Some([]string{"employee_id"})})
		if !slices.Equal(x.AttributionKeys, []string{"employee_id"}) {
			t.Errorf("keys once attributed got = %v", x.AttributionKeys)
		}
	})
	t.Run("attribution key taken", func(t *testing.T) {
		t.Parallel()
		// source: spec/services/usage_attribution_types/update_service_spec.rb:96
		f := setup(t)
		u := user(t, f)
		f.create(t, "team", attribution.RoleHierarchical, none, "employee_id")
		_, err := update(t, f, u.ID, usageattribution.UpdateInput{AttributionKeys: core.Some([]string{"employee_id"})})
		wantErrors(t, err, core.FieldError{Field: "attribution_keys", Code: core.CodeAlreadyExists})
	})
	t.Run("code", func(t *testing.T) {
		t.Parallel()
		// source: spec/services/usage_attribution_types/update_service_spec.rb:106
		// source: spec/services/usage_attribution_types/update_service_spec.rb:114
		f := setup(t)
		u := user(t, f)
		if x := mustUpdate(t, f, u.ID, usageattribution.UpdateInput{Code: core.Some(" member ")}); x.Code != "member" {
			t.Errorf("code got = %q, want = member", x.Code)
		}
		f.value(t, u)
		_, err := update(t, f, u.ID, usageattribution.UpdateInput{Code: core.Some("other")})
		wantErrors(t, err, core.FieldError{Field: "code", Code: attribution.CodeUsageAlreadyAttributed})
	})
	t.Run("code taken", func(t *testing.T) {
		t.Parallel()
		// source: spec/services/usage_attribution_types/update_service_spec.rb:297
		f := setup(t)
		u := user(t, f)
		f.create(t, "department", attribution.RoleHierarchical, none)
		_, err := update(t, f, u.ID, usageattribution.UpdateInput{Code: core.Some("department")})
		wantHas(t, err, "code", core.CodeAlreadyExists)
	})
	t.Run("role", func(t *testing.T) {
		t.Parallel()
		// source: spec/services/usage_attribution_types/update_service_spec.rb:124
		f := setup(t)
		u := user(t, f)
		if x := mustUpdate(t, f, u.ID, usageattribution.UpdateInput{Role: core.Some(attribution.RoleFlat)}); x.Role != attribution.RoleFlat {
			t.Errorf("role got = %s, want = flat", x.Role)
		}
	})
	t.Run("role with children", func(t *testing.T) {
		t.Parallel()
		// source: spec/services/usage_attribution_types/update_service_spec.rb:132
		f := setup(t)
		u := user(t, f)
		f.create(t, "child", attribution.RoleHierarchical, u.ID)
		_, err := update(t, f, u.ID, usageattribution.UpdateInput{Role: core.Some(attribution.RoleFlat)})
		wantErrors(t, err, core.FieldError{Field: "role", Code: attribution.CodeCannotBeFlatWithChildren})
	})
	t.Run("role with a parent", func(t *testing.T) {
		t.Parallel()
		// source: spec/services/usage_attribution_types/update_service_spec.rb:143
		// source: spec/services/usage_attribution_types/update_service_spec.rb:155
		f := setup(t)
		u := user(t, f)
		parent := f.create(t, "department", attribution.RoleHierarchical, none)
		mustUpdate(t, f, u.ID, usageattribution.UpdateInput{ParentID: core.Some(parent.ID)})
		_, err := update(t, f, u.ID, usageattribution.UpdateInput{Role: core.Some(attribution.RoleFlat)})
		wantHas(t, err, "parent_id", attribution.CodeForbiddenForFlatRole)
		x := mustUpdate(t, f, u.ID, usageattribution.UpdateInput{Role: core.Some(attribution.RoleFlat), ParentID: core.Some(none)})
		if x.Role != attribution.RoleFlat || !x.ParentID.IsZero() {
			t.Errorf("got = %+v, want = flat root", x)
		}
	})
	t.Run("parent", func(t *testing.T) {
		t.Parallel()
		// source: spec/services/usage_attribution_types/update_service_spec.rb:167
		// source: spec/services/usage_attribution_types/update_service_spec.rb:177
		f := setup(t)
		u := user(t, f)
		parent := f.create(t, "department", attribution.RoleHierarchical, none)
		e, err := f.s.Update(t.Context(), f.h.OrgID(), u.ID, usageattribution.UpdateInput{ParentID: core.Some(parent.ID)})
		if err != nil || e.Type.ParentID != parent.ID || e.Parent.ID != parent.ID {
			t.Fatalf("got = %v, %v, want = parent department", e, err)
		}
		if x := mustUpdate(t, f, u.ID, usageattribution.UpdateInput{ParentID: core.Some(none)}); !x.ParentID.IsZero() {
			t.Errorf("parent got = %s, want = none", x.ParentID)
		}
	})
	t.Run("parent missing", func(t *testing.T) {
		t.Parallel()
		// source: spec/services/usage_attribution_types/update_service_spec.rb:186
		// source: spec/services/usage_attribution_types/update_service_spec.rb:195
		f := setup(t)
		u := user(t, f)
		_, err := update(t, f, u.ID, usageattribution.UpdateInput{ParentID: core.Some(core.NewID[core.UsageAttributionTypeID](f.h.Deps.IDs))})
		wantNotFound(t, err, usageattribution.ParentResource)
		foreign := f.otherOrgType(t, "foreign", "foreign_id")
		_, err = update(t, f, u.ID, usageattribution.UpdateInput{ParentID: core.Some(foreign.ID)})
		wantNotFound(t, err, usageattribution.ParentResource)
	})
	t.Run("flat parent", func(t *testing.T) {
		t.Parallel()
		// source: spec/services/usage_attribution_types/update_service_spec.rb:204
		f := setup(t)
		u := user(t, f)
		flat := f.create(t, "model", attribution.RoleFlat, none)
		_, err := update(t, f, u.ID, usageattribution.UpdateInput{ParentID: core.Some(flat.ID)})
		wantHas(t, err, "parent_id", attribution.CodeMustBeHierarchical)
	})
	t.Run("cycle", func(t *testing.T) {
		t.Parallel()
		// source: spec/services/usage_attribution_types/update_service_spec.rb:214
		// source: spec/models/usage_attribution_type_spec.rb:171
		// source: spec/models/usage_attribution_type_spec.rb:178
		f := setup(t)
		u := user(t, f)
		child := f.create(t, "child", attribution.RoleHierarchical, u.ID)
		grandchild := f.create(t, "grandchild", attribution.RoleHierarchical, child.ID)
		for _, p := range []core.UsageAttributionTypeID{u.ID, child.ID, grandchild.ID} {
			_, err := update(t, f, u.ID, usageattribution.UpdateInput{ParentID: core.Some(p)})
			wantHas(t, err, "parent_id", attribution.CodeCannotFormACycle)
		}
	})
	t.Run("attributed", func(t *testing.T) {
		t.Parallel()
		// source: spec/services/usage_attribution_types/update_service_spec.rb:236
		// source: spec/services/usage_attribution_types/update_service_spec.rb:245
		// source: spec/services/usage_attribution_types/update_service_spec.rb:254
		// source: spec/services/usage_attribution_types/update_service_spec.rb:263
		f := setup(t)
		u := user(t, f)
		parent := f.create(t, "department", attribution.RoleHierarchical, none)
		f.value(t, u)
		in := frozenAll
		in.ParentID = core.Some(parent.ID)
		_, err := update(t, f, u.ID, in)
		wantErrors(t, err, frozenErrors...)
		if got := f.reload(t, u.ID); *got.Name != "User" || got.Code != "user" {
			t.Errorf("stored got = %+v, want = unchanged", got)
		}
		x := mustUpdate(t, f, u.ID, usageattribution.UpdateInput{
			Name: core.Some(ptr("Member")), Code: core.Some("user"), AttributionKeys: core.Some([]string{"user_id"}),
			Role: core.Some(attribution.RoleHierarchical), ParentID: core.Some(none),
		})
		if *x.Name != "Member" {
			t.Errorf("name got = %q, want = Member", *x.Name)
		}
		if x := mustUpdate(t, f, u.ID, usageattribution.UpdateInput{Name: core.Some(ptr("Other"))}); *x.Name != "Other" {
			t.Errorf("name got = %q, want = Other", *x.Name)
		}
	})
	t.Run("attributed values deleted", func(t *testing.T) {
		t.Parallel()
		// source: spec/services/usage_attribution_types/update_service_spec.rb:272
		// source: spec/services/usage_attribution_types/update_service_spec.rb:284
		f := setup(t)
		u := user(t, f)
		parent := f.create(t, "department", attribution.RoleHierarchical, none)
		f.value(t, u)
		f.h.Do(func(ctx context.Context, r port.Repos) error {
			_, err := r.UsageAttributionValues.DeleteForType(ctx, f.h.OrgID(), u.ID, now)
			return err
		})
		in := frozenAll
		in.ParentID = core.Some(parent.ID)
		_, err := update(t, f, u.ID, in)
		wantErrors(t, err, frozenErrors...)
		if x := mustUpdate(t, f, u.ID, usageattribution.UpdateInput{Name: core.Some(ptr("Member"))}); *x.Name != "Member" {
			t.Errorf("name got = %q, want = Member", *x.Name)
		}
	})
	t.Run("deleted parent kept", func(t *testing.T) {
		t.Parallel()
		// source: spec/models/usage_attribution_type_spec.rb:27
		f := setup(t)
		parent := f.create(t, "department", attribution.RoleHierarchical, none)
		u := f.create(t, "user", attribution.RoleHierarchical, parent.ID)
		if _, err := f.s.Delete(t.Context(), f.h.OrgID(), parent.ID); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		e, err := f.s.Update(t.Context(), f.h.OrgID(), u.ID, usageattribution.UpdateInput{Name: core.Some(ptr("Member"))})
		if err != nil || e.Parent == nil || e.Parent.Code != "department" || e.Parent.DeletedAt == nil {
			t.Fatalf("got = %v, %v, want = the deleted parent", e, err)
		}
	})
}

func TestDelete(t *testing.T) {
	t.Parallel()
	// source: spec/services/usage_attribution_types/destroy_service_spec.rb:11
	// source: spec/services/usage_attribution_types/destroy_service_spec.rb:18
	// source: spec/services/usage_attribution_types/destroy_service_spec.rb:28
	// source: spec/services/usage_attribution_types/destroy_service_spec.rb:37
	// source: spec/services/usage_attribution_types/destroy_service_spec.rb:42
	// source: spec/services/usage_attribution_types/destroy_service_spec.rb:46
	f := setup(t)
	typ := f.create(t, "department", attribution.RoleHierarchical, none)
	child := f.create(t, "user", attribution.RoleHierarchical, typ.ID)
	v := f.value(t, typ)
	e, err := f.s.Delete(t.Context(), f.h.OrgID(), typ.ID)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if e.Type.ID != typ.ID || e.Type.DeletedAt == nil {
		t.Errorf("deleted got = %+v", e.Type)
	}
	if got := f.reload(t, typ.ID); got == nil || got.DeletedAt == nil {
		t.Errorf("stored got = %+v, want = kept row marked deleted", got)
	}
	got := billingtest.Read(f.h, func(ctx context.Context, r port.Repos) (*attribution.Value, error) {
		return r.UsageAttributionValues.Get(ctx, f.h.OrgID(), v.ID)
	})
	if got.DeletedAt == nil {
		t.Errorf("value got = kept, want = deleted")
	}
	c, err := f.s.Get(t.Context(), f.h.OrgID(), child.ID)
	if err != nil || c.Type.DeletedAt != nil || c.Type.ParentID != typ.ID || c.Parent == nil || c.Parent.DeletedAt == nil {
		t.Errorf("child got = %v, %v, want = kept, naming its deleted parent", c, err)
	}

	// source: spec/services/usage_attribution_types/destroy_service_spec.rb:58
	_, err = f.s.Delete(t.Context(), f.h.OrgID(), typ.ID)
	wantNotFound(t, err, usageattribution.Resource)
	// source: spec/models/usage_attribution_type_spec.rb:62
	// source: spec/models/usage_attribution_type_spec.rb:100
	f.create(t, "department", attribution.RoleHierarchical, none, "department_id")
}

func TestList(t *testing.T) {
	t.Parallel()
	f := setup(t)
	department := f.create(t, "department", attribution.RoleHierarchical, none, "department_id")
	user := f.create(t, "user", attribution.RoleHierarchical, department.ID, "user_id")
	model := f.create(t, "model", attribution.RoleFlat, none, "model_name")
	f.otherOrgType(t, "foreign", "foreign_id")
	gone := f.create(t, "gone", attribution.RoleFlat, none)
	if _, err := f.s.Delete(t.Context(), f.h.OrgID(), gone.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	list := func(t *testing.T, filter attribution.TypeFilter, size int) ([]core.UsageAttributionTypeID, []string) {
		t.Helper()
		var ids []core.UsageAttributionTypeID
		var parents []string
		page := core.PageRequest{Size: size}
		for {
			p, err := f.s.List(t.Context(), f.h.OrgID(), filter, page)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			for _, e := range p.Items {
				ids = append(ids, e.Type.ID)
				code := ""
				if e.Parent != nil {
					code = e.Parent.Code
				}
				parents = append(parents, code)
			}
			if p.NextToken == "" {
				return ids, parents
			}
			page.Token = p.NextToken
		}
	}
	type id = core.UsageAttributionTypeID
	cases := []struct {
		name   string
		source string
		filter attribution.TypeFilter
		want   []id
	}{
		{"all", "spec/queries/usage_attribution_types_query_spec.rb:26", attribution.TypeFilter{}, []id{model.ID, user.ID, department.ID}},
		{"role", "spec/queries/usage_attribution_types_query_spec.rb:57", attribution.TypeFilter{Role: attribution.RoleFlat}, []id{model.ID}},
		{"roots", "spec/queries/usage_attribution_types_query_spec.rb:65", attribution.TypeFilter{Root: ptr(true)}, []id{model.ID, department.ID}},
		{"search code", "spec/queries/usage_attribution_types_query_spec.rb:100", attribution.TypeFilter{Search: "depart"}, []id{department.ID}},
		{"search name", "spec/queries/usage_attribution_types_query_spec.rb:107", attribution.TypeFilter{Search: "Model"}, []id{model.ID}},
		{"search key", "spec/queries/usage_attribution_types_query_spec.rb:115", attribution.TypeFilter{Search: "model_name"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Log("source:", c.source)
			got, _ := list(t, c.filter, 2)
			if !slices.Equal(got, c.want) {
				t.Errorf("ids got = %v, want = %v", got, c.want)
			}
		})
	}
	t.Run("parent codes", func(t *testing.T) {
		_, parents := list(t, attribution.TypeFilter{}, 10)
		if !slices.Equal(parents, []string{"", "department", ""}) {
			t.Errorf("parent codes got = %v", parents)
		}
	})
	t.Run("invalid role", func(t *testing.T) {
		// source: spec/queries/usage_attribution_types_query_spec.rb:90
		_, err := f.s.List(t.Context(), f.h.OrgID(), attribution.TypeFilter{Role: "unknown"}, core.PageRequest{})
		wantErrors(t, err, core.FieldError{Field: "role", Code: core.CodeInvalid})
	})
	t.Run("roots after parent deleted", func(t *testing.T) {
		// source: spec/queries/usage_attribution_types_query_spec.rb:72
		if _, err := f.s.Delete(t.Context(), f.h.OrgID(), department.ID); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		got, _ := list(t, attribution.TypeFilter{Root: ptr(true)}, 1)
		if !slices.Equal(got, []id{model.ID, user.ID}) {
			t.Errorf("ids got = %v, want = model, user", got)
		}
	})
}
