package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/attribution"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// attributionType returns a kept type of org created at now+d.
func attributionType(e *env, org core.OrganizationID, code string, role attribution.Role, parent core.UsageAttributionTypeID, d time.Duration, keys ...string) *attribution.Type {
	return &attribution.Type{
		ID: core.NewID[core.UsageAttributionTypeID](e.ids), OrganizationID: org, Code: code, Name: ptr("Type " + code),
		AttributionKeys: keys, Role: role, ParentID: parent, CreatedAt: at(d), UpdatedAt: at(d),
	}
}

func testAttribution(t *testing.T, h Harness) {
	t.Run("type_crud", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		root := attributionType(e, org, "org", attribution.RoleHierarchical, core.UsageAttributionTypeID{}, 0, "org_id")
		root.Description = ptr("Top level")
		team := attributionType(e, org, "team", attribution.RoleHierarchical, root.ID, time.Second, "team_id", "squad_id")
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create(root)", r.UsageAttributionTypes.Create(ctx, root))
			return r.UsageAttributionTypes.Create(ctx, team)
		})
		e.tx(func(ctx context.Context, r port.Repos) error {
			got, err := r.UsageAttributionTypes.Get(ctx, org, team.ID)
			noErr(t, "Get", err)
			equal(t, "Get", got, team)
			got, err = r.UsageAttributionTypes.GetByCode(ctx, org, "org")
			noErr(t, "GetByCode", err)
			equal(t, "GetByCode", got, root)
			_, err = r.UsageAttributionTypes.Get(ctx, b.OrganizationID, team.ID)
			wantErr(t, "Get(other org)", err, core.ErrNotFound)

			has, err := r.UsageAttributionTypes.HasChildren(ctx, org, root.ID)
			noErr(t, "HasChildren", err)
			equal(t, "HasChildren(root)", has, true)
			has, err = r.UsageAttributionTypes.HasChildren(ctx, org, team.ID)
			noErr(t, "HasChildren", err)
			equal(t, "HasChildren(team)", has, false)

			taken, err := r.UsageAttributionTypes.KeysTaken(ctx, org, []string{"x", "squad_id"}, core.UsageAttributionTypeID{})
			noErr(t, "KeysTaken", err)
			equal(t, "KeysTaken(squad_id)", taken, true)
			taken, err = r.UsageAttributionTypes.KeysTaken(ctx, org, []string{"squad_id"}, team.ID)
			noErr(t, "KeysTaken", err)
			equal(t, "KeysTaken(squad_id, except team)", taken, false)
			taken, err = r.UsageAttributionTypes.KeysTaken(ctx, b.OrganizationID, []string{"org_id"}, core.UsageAttributionTypeID{})
			noErr(t, "KeysTaken", err)
			equal(t, "KeysTaken(other org)", taken, false)

			// A kept type's code is unique.
			dup := attributionType(e, org, "org", attribution.RoleFlat, core.UsageAttributionTypeID{}, 0, "other")
			wantErr(t, "Create(code taken)", r.UsageAttributionTypes.Create(ctx, dup), core.ErrConflict)
			return nil
		})

		team.Name = nil
		team.AttributionKeys = []string{"team_id"}
		team.UpdatedAt = at(time.Minute)
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Update", r.UsageAttributionTypes.Update(ctx, team))
			got, err := r.UsageAttributionTypes.Get(ctx, org, team.ID)
			noErr(t, "Get", err)
			equal(t, "Get(updated)", got, team)
			wantErr(t, "Update(missing)", r.UsageAttributionTypes.Update(ctx,
				attributionType(e, org, "ghost", attribution.RoleFlat, core.UsageAttributionTypeID{}, 0, "g")), core.ErrNotFound)
			return nil
		})

		// Delete keeps the row: GetMany still returns it, Get does not, and
		// the code is free again.
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Delete", r.UsageAttributionTypes.Delete(ctx, org, root.ID, at(time.Hour)))
			wantErr(t, "Delete(again)", r.UsageAttributionTypes.Delete(ctx, org, root.ID, at(time.Hour)), core.ErrNotFound)
			_, err := r.UsageAttributionTypes.Get(ctx, org, root.ID)
			wantErr(t, "Get(deleted)", err, core.ErrNotFound)
			many, err := r.UsageAttributionTypes.GetMany(ctx, org, []core.UsageAttributionTypeID{root.ID, team.ID, core.NewID[core.UsageAttributionTypeID](e.ids)})
			noErr(t, "GetMany", err)
			equal(t, "GetMany size", len(many), 2)
			equal(t, "GetMany(deleted).DeletedAt", many[root.ID].DeletedAt, ptr(at(time.Hour)))
			reuse := attributionType(e, org, "org", attribution.RoleFlat, core.UsageAttributionTypeID{}, 0, "org_id")
			noErr(t, "Create(code freed)", r.UsageAttributionTypes.Create(ctx, reuse))
			return nil
		})
	})

	t.Run("type_list", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		none := core.UsageAttributionTypeID{}
		root := attributionType(e, org, "account", attribution.RoleHierarchical, none, 0, "k1")
		child := attributionType(e, org, "team", attribution.RoleHierarchical, root.ID, time.Second, "k2")
		flat := attributionType(e, org, "model", attribution.RoleFlat, none, 2*time.Second, "k3")
		orphanParent := attributionType(e, org, "gone", attribution.RoleHierarchical, none, 3*time.Second, "k4")
		orphan := attributionType(e, org, "orphan", attribution.RoleHierarchical, orphanParent.ID, 4*time.Second, "k5")
		// Same instant as orphan: the ID breaks the tie.
		tie := attributionType(e, org, "tie", attribution.RoleFlat, none, 4*time.Second, "k6")
		other := attributionType(e, b.OrganizationID, "team-b", attribution.RoleFlat, none, 0, "k1")
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, x := range []*attribution.Type{root, child, flat, orphanParent, orphan, tie, other} {
				noErr(t, "Create", r.UsageAttributionTypes.Create(ctx, x))
			}
			return r.UsageAttributionTypes.Delete(ctx, org, orphanParent.ID, at(time.Hour))
		})
		id := func(x *attribution.Type) core.UsageAttributionTypeID { return x.ID }
		first, second := orphan, tie
		if tie.ID.String() < orphan.ID.String() {
			first, second = tie, orphan
		}
		cases := []struct {
			name string
			f    attribution.TypeFilter
			want []*attribution.Type
		}{
			{"all", attribution.TypeFilter{}, []*attribution.Type{first, second, flat, child, root}},
			{"flat", attribution.TypeFilter{Role: attribution.RoleFlat}, []*attribution.Type{tie, flat}},
			{"roots", attribution.TypeFilter{Root: ptr(true)}, []*attribution.Type{first, second, flat, root}},
			{"children", attribution.TypeFilter{Root: ptr(false)}, []*attribution.Type{child}},
			{"search code", attribution.TypeFilter{Search: " TEA "}, []*attribution.Type{child}},
			{"search name", attribution.TypeFilter{Search: "type mod"}, []*attribution.Type{flat}},
			{"search literal", attribution.TypeFilter{Search: "%"}, nil},
		}
		for _, c := range cases {
			e.tx(func(ctx context.Context, r port.Repos) error {
				got := listAll(t, 2, func(p core.PageRequest) (core.Page[*attribution.Type], error) {
					return r.UsageAttributionTypes.List(ctx, org, c.f, p)
				})
				equal(t, "List("+c.name+")", ids(got, id), ids(c.want, id))
				return nil
			})
		}
	})

	t.Run("values", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, _ := e.twoOrgs()
		org := a.OrganizationID
		cust := walletFixtureCustomer(e, a, "cust-1")
		typ := attributionType(e, org, "team", attribution.RoleHierarchical, core.UsageAttributionTypeID{}, 0, "team_id")
		e.tx(func(ctx context.Context, r port.Repos) error { return r.UsageAttributionTypes.Create(ctx, typ) })
		e.tx(func(ctx context.Context, r port.Repos) error {
			exists, err := r.UsageAttributionValues.ExistsForType(ctx, org, typ.ID)
			noErr(t, "ExistsForType", err)
			equal(t, "ExistsForType(none)", exists, false)
			return nil
		})
		parent := &attribution.Value{
			ID: core.NewID[core.UsageAttributionValueID](e.ids), OrganizationID: org, TypeID: typ.ID, CustomerID: cust,
			Value: "eng", LastSeenAt: ptr(now), CreatedAt: now, UpdatedAt: now,
		}
		child := &attribution.Value{
			ID: core.NewID[core.UsageAttributionValueID](e.ids), OrganizationID: org, TypeID: typ.ID, CustomerID: cust,
			ParentID: parent.ID, Value: "eng-web", CreatedAt: now, UpdatedAt: now,
		}
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create(parent)", r.UsageAttributionValues.Create(ctx, parent))
			noErr(t, "Create(child)", r.UsageAttributionValues.Create(ctx, child))
			dup := *parent
			dup.ID = core.NewID[core.UsageAttributionValueID](e.ids)
			wantErr(t, "Create(value taken)", r.UsageAttributionValues.Create(ctx, &dup), core.ErrConflict)
			return nil
		})
		e.tx(func(ctx context.Context, r port.Repos) error {
			got, err := r.UsageAttributionValues.Get(ctx, org, child.ID)
			noErr(t, "Get", err)
			equal(t, "Get", got, child)
			n, err := r.UsageAttributionValues.DeleteForType(ctx, org, typ.ID, at(time.Hour))
			noErr(t, "DeleteForType", err)
			equal(t, "DeleteForType", n, 2)
			n, err = r.UsageAttributionValues.DeleteForType(ctx, org, typ.ID, at(2*time.Hour))
			noErr(t, "DeleteForType(again)", err)
			equal(t, "DeleteForType(again)", n, 0)
			got, err = r.UsageAttributionValues.Get(ctx, org, parent.ID)
			noErr(t, "Get(deleted)", err)
			equal(t, "Get(deleted).DeletedAt", got.DeletedAt, ptr(at(time.Hour)))
			exists, err := r.UsageAttributionValues.ExistsForType(ctx, org, typ.ID)
			noErr(t, "ExistsForType", err)
			equal(t, "ExistsForType(deleted values)", exists, true)
			// Unique including deleted values.
			again := *parent
			again.ID, again.DeletedAt = core.NewID[core.UsageAttributionValueID](e.ids), nil
			wantErr(t, "Create(deleted value taken)", r.UsageAttributionValues.Create(ctx, &again), core.ErrConflict)
			return nil
		})
	})
}
