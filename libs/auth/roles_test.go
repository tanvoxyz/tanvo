package auth

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func TestRolesDelete(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		missing bool
		builtIn bool
		active  bool
		revoked bool
		code    string
	}{
		{name: "no assigned members", source: "spec/services/roles/destroy_service_spec.rb:14"},
		{name: "nil role", source: "spec/services/roles/destroy_service_spec.rb:29", missing: true, code: "role_not_found"},
		{name: "predefined role", source: "spec/services/roles/destroy_service_spec.rb:43", builtIn: true, code: access.CodePredefinedRole},
		{name: "assigned members", source: "spec/services/roles/destroy_service_spec.rb:61", active: true, code: access.CodeRoleAssignedToMembers},
		{name: "only revoked memberships", source: "spec/services/roles/destroy_service_spec.rb:81", revoked: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			o := e.org()
			custom := access.NewCustomRole(core.NewID[core.RoleID](e.deps.IDs), o.ID, "developer", "Developer", []permission.Permission{"addons:read"}, e.clock.Now())
			e.tx(func(ctx context.Context, r port.Repos) error { return r.Roles.Create(ctx, custom) })
			var holder *access.Membership
			if tt.active || tt.revoked {
				holder = access.NewMembership(core.NewID[core.MembershipID](e.deps.IDs), o.ID, e.user("member@hooli.com", "pw").ID, []core.RoleID{custom.ID, roleID(finance)}, e.clock.Now())
				e.tx(func(ctx context.Context, r port.Repos) error { return r.Memberships.Create(ctx, holder) })
				if tt.revoked {
					e.revoke(holder)
				}
			}
			id := custom.ID
			switch {
			case tt.missing:
				id = core.NewID[core.RoleID](e.deps.IDs)
			case tt.builtIn:
				id = roleID(finance)
			}
			got, err := NewRoles(e.deps).Delete(e.ctx, o.ID, id)
			if code(err) != tt.code {
				t.Fatalf("code got = %q, want = %q (%s)", code(err), tt.code, tt.source)
			}
			if n, want := e.log.count(EventRoleDeleted), map[bool]int{true: 1, false: 0}[tt.code == ""]; n != want {
				t.Errorf("%s logs got = %d, want = %d (%s)", EventRoleDeleted, n, want, tt.source)
			}
			e.tx(func(ctx context.Context, r port.Repos) error {
				_, err := r.Roles.Get(ctx, o.ID, custom.ID)
				if discarded := errors.Is(err, core.ErrNotFound); discarded != (tt.code == "" && !tt.missing) {
					t.Errorf("discarded got = %v, want = %v (%s)", discarded, !discarded, tt.source)
				}
				if holder != nil {
					m, err := r.Memberships.Get(ctx, o.ID, holder.ID)
					if err != nil {
						return err
					}
					want := access.NormalizeRoleIDs([]core.RoleID{custom.ID, roleID(finance)})
					if tt.code == "" {
						want = []core.RoleID{roleID(finance)}
					}
					if !slices.Equal(m.RoleIDs, want) {
						t.Errorf("membership roles got = %v, want = %v", m.RoleIDs, want)
					}
				}
				return nil
			})
			if tt.code != "" {
				return
			}
			if got.Code != "developer" {
				t.Errorf("role got = %s, want = developer", got.Code)
			}
			ev := e.log.events[len(e.log.events)-1]
			if ev.Type != SecurityLogRole || ev.Resources["role_code"] != "developer" || !ev.UserID.IsZero() {
				t.Errorf("log got = %+v, want = role log with role_code developer and no user", ev)
			}
			if _, err := NewRoles(e.deps).Delete(e.ctx, o.ID, custom.ID); code(err) != "role_not_found" {
				t.Errorf("Delete() again got = %q, want = role_not_found", code(err))
			}
		})
	}
}
