package auth

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

const (
	admin   = permission.BuiltInRoleAdmin
	finance = permission.BuiltInRoleFinance
	manager = permission.BuiltInRoleManager
)

func TestMembershipsUpdateRoles(t *testing.T) {
	unknown := core.MustParseID[core.RoleID]("0190a000-0000-7000-8000-000000000099")
	tests := []struct {
		name        string
		source      string
		actor       []permission.BuiltInRole
		target      []permission.BuiltInRole
		otherAdmins int
		roles       []core.RoleID
		missing     bool
		want        []core.RoleID
		code        string
		log         map[string]any
	}{
		{
			name: "another admin exists", source: "spec/services/memberships/update_service_spec.rb:16",
			actor: []permission.BuiltInRole{manager}, target: []permission.BuiltInRole{admin}, otherAdmins: 1,
			roles: roleIDs(manager), want: roleIDs(manager),
			log: map[string]any{"deleted": []string{"admin"}, "added": []string{"manager"}},
		},
		{
			name: "admin grants admin", source: "spec/services/memberships/update_service_spec.rb:35",
			actor: []permission.BuiltInRole{admin}, target: []permission.BuiltInRole{manager},
			roles: roleIDs(admin), want: roleIDs(admin),
			log: map[string]any{"deleted": []string{"manager"}, "added": []string{"admin"}},
		},
		{
			name: "non-admin grants admin", source: "spec/services/memberships/update_service_spec.rb:57",
			actor: []permission.BuiltInRole{manager}, target: []permission.BuiltInRole{manager}, otherAdmins: 1,
			roles: roleIDs(admin), code: access.CodeCannotGrantAdmin,
		},
		{
			name: "last admin", source: "spec/services/memberships/update_service_spec.rb:78",
			actor: []permission.BuiltInRole{manager}, target: []permission.BuiltInRole{admin},
			roles: roleIDs(manager), code: access.CodeLastAdmin,
		},
		{
			name: "membership not found", source: "spec/services/memberships/update_service_spec.rb:93",
			actor: []permission.BuiltInRole{admin}, target: []permission.BuiltInRole{finance}, missing: true,
			roles: roleIDs(manager), code: "membership_not_found",
		},
		{
			name: "invalid role", source: "spec/services/memberships/update_service_spec.rb:106",
			actor: []permission.BuiltInRole{admin}, target: []permission.BuiltInRole{admin},
			roles: []core.RoleID{unknown}, code: "role_not_found",
		},
		{
			name: "no roles", source: "spec/services/memberships/update_service_spec.rb:106",
			actor: []permission.BuiltInRole{admin}, target: []permission.BuiltInRole{finance},
			code: "role_not_found",
		},
		{
			name: "unknown roles are ignored", source: "tanvo",
			actor: []permission.BuiltInRole{admin}, target: []permission.BuiltInRole{finance},
			roles: []core.RoleID{unknown, roleID(manager)}, want: roleIDs(manager),
			log: map[string]any{"deleted": []string{"finance"}, "added": []string{"manager"}},
		},
		{
			name: "adding a role keeps the others", source: "tanvo",
			actor: []permission.BuiltInRole{admin}, target: []permission.BuiltInRole{finance},
			roles: roleIDs(finance, manager), want: roleIDs(finance, manager),
			log: map[string]any{"added": []string{"manager"}},
		},
		{
			name: "admin with several roles keeps admin", source: "tanvo",
			actor: []permission.BuiltInRole{manager, admin}, target: []permission.BuiltInRole{admin, finance},
			roles: roleIDs(admin), want: roleIDs(admin),
			log: map[string]any{"deleted": []string{"finance"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			o := e.org()
			actor := e.user("actor@hooli.com", "pw")
			e.member(actor, o, tt.actor...)
			target := e.member(e.user("target@hooli.com", "pw"), o, tt.target...)
			for i := range tt.otherAdmins {
				e.member(e.user(string(rune('a'+i))+"@hooli.com", "pw"), o, admin)
			}
			id := target.ID
			if tt.missing {
				id = core.NewID[core.MembershipID](e.deps.IDs)
			}
			got, err := NewMemberships(e.deps).UpdateRoles(e.ctx, o.ID, actor.ID, id, tt.roles)
			if code(err) != tt.code {
				t.Fatalf("code got = %q, want = %q (%s)", code(err), tt.code, tt.source)
			}
			if tt.code != "" {
				if n := e.log.count(EventUserRoleEdited); n != 0 {
					t.Errorf("%s logs got = %d, want = 0", EventUserRoleEdited, n)
				}
				return
			}
			if !slices.Equal(got.RoleIDs, tt.want) {
				t.Errorf("roles got = %v, want = %v (%s)", got.RoleIDs, tt.want, tt.source)
			}
			var stored *access.Membership
			e.tx(func(ctx context.Context, r port.Repos) (err error) {
				stored, err = r.Memberships.Get(ctx, o.ID, target.ID)
				return err
			})
			if !slices.Equal(stored.RoleIDs, tt.want) {
				t.Errorf("stored roles got = %v, want = %v", stored.RoleIDs, tt.want)
			}
			ev := e.log.events[len(e.log.events)-1]
			if ev.Event != EventUserRoleEdited || !reflect.DeepEqual(ev.Resources, map[string]any{"email": "target@hooli.com", "roles": tt.log}) {
				t.Errorf("log got = %s %v, want = %s %v", ev.Event, ev.Resources, EventUserRoleEdited, tt.log)
			}
		})
	}
}

func TestMembershipsRevoke(t *testing.T) {
	tests := []struct {
		name          string
		source        string
		self          bool
		missing       bool
		target        []permission.BuiltInRole
		otherAdmins   int
		revokedAdmins int
		code          string
	}{
		{name: "own membership", source: "spec/services/memberships/revoke_service_spec.rb:19", self: true, code: access.CodeCannotRevokeOwnMembership},
		{name: "not found", source: "spec/services/memberships/revoke_service_spec.rb:37", missing: true, target: []permission.BuiltInRole{finance}, code: "membership_not_found"},
		{name: "another membership", source: "spec/services/memberships/revoke_service_spec.rb:52", target: []permission.BuiltInRole{finance}},
		{name: "last admin", source: "spec/services/memberships/revoke_service_spec.rb:71", target: []permission.BuiltInRole{admin, finance}, code: access.CodeLastAdmin},
		{name: "last active admin", source: "spec/services/memberships/revoke_service_spec.rb:89", target: []permission.BuiltInRole{admin}, revokedAdmins: 1, code: access.CodeLastAdmin},
		{name: "admin when another exists", source: "spec/models/membership_role_spec.rb:75", target: []permission.BuiltInRole{admin}, otherAdmins: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			o := e.org()
			actor := e.user("actor@hooli.com", "pw")
			am := e.member(actor, o, manager)
			for i := range tt.otherAdmins {
				e.member(e.user(string(rune('a'+i))+"@hooli.com", "pw"), o, admin)
			}
			for i := range tt.revokedAdmins {
				e.revoke(e.member(e.user(string(rune('r'+i))+"@hooli.com", "pw"), o, admin))
			}
			id := am.ID
			if !tt.self {
				id = e.member(e.user("target@hooli.com", "pw"), o, tt.target...).ID
			}
			if tt.missing {
				id = core.NewID[core.MembershipID](e.deps.IDs)
			}
			got, err := NewMemberships(e.deps).Revoke(e.ctx, o.ID, actor.ID, id)
			if code(err) != tt.code {
				t.Fatalf("code got = %q, want = %q (%s)", code(err), tt.code, tt.source)
			}
			if n, want := e.log.count(EventUserDeleted), map[bool]int{true: 1, false: 0}[tt.code == ""]; n != want {
				t.Errorf("%s logs got = %d, want = %d", EventUserDeleted, n, want)
			}
			if tt.code == "" && (got.Active() || got.RevokedAt == nil) {
				t.Errorf("membership got = %s, want = revoked", got.Status)
			}
		})
	}
	// source: app/services/memberships/revoke_service.rb:15-20 and
	// app/models/membership.rb:28-31: a revoked membership passes the same
	// checks, keeps its revocation time and is logged again.
	for _, tt := range []struct {
		name        string
		target      []permission.BuiltInRole
		otherAdmins int
		code        string
	}{
		{name: "revoked membership", target: []permission.BuiltInRole{finance}},
		{name: "revoked admin with one active admin", target: []permission.BuiltInRole{admin}, code: access.CodeLastAdmin},
		{name: "revoked admin with two active admins", target: []permission.BuiltInRole{admin}, otherAdmins: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			o := e.org()
			actor := e.user("actor@hooli.com", "pw")
			e.member(actor, o, admin)
			for i := range tt.otherAdmins {
				e.member(e.user(string(rune('a'+i))+"@hooli.com", "pw"), o, admin)
			}
			m := e.member(e.user("target@hooli.com", "pw"), o, tt.target...)
			e.revoke(m)
			revokedAt := *m.RevokedAt
			e.clock.Advance(time.Hour)
			got, err := NewMemberships(e.deps).Revoke(e.ctx, o.ID, actor.ID, m.ID)
			if code(err) != tt.code {
				t.Fatalf("code got = %q, want = %q", code(err), tt.code)
			}
			if n, want := e.log.count(EventUserDeleted), map[bool]int{true: 1, false: 0}[tt.code == ""]; n != want {
				t.Errorf("%s logs got = %d, want = %d", EventUserDeleted, n, want)
			}
			if tt.code == "" && (got.Active() || !got.RevokedAt.Equal(revokedAt)) {
				t.Errorf("membership got = %s at %v, want = revoked at %v", got.Status, got.RevokedAt, revokedAt)
			}
		})
	}
}
