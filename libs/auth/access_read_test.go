package auth

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func TestMembershipsList(t *testing.T) {
	e := newEnv(t)
	o, other := e.org(), e.org()
	admin := e.member(e.user("admin@tanvo.test", ""), o, permission.BuiltInRoleAdmin)
	e.clock.Advance(time.Second)
	fin := e.member(e.user("finance@tanvo.test", ""), o, permission.BuiltInRoleFinance)
	e.clock.Advance(time.Second)
	gone := e.member(e.user("gone_100%@tanvo.test", ""), o, permission.BuiltInRoleAdmin)
	e.revoke(gone)
	e.clock.Advance(time.Second)
	both := e.member(e.user("Both@tanvo.test", ""), o, permission.BuiltInRoleManager, permission.BuiltInRoleFinance)
	e.member(e.user("finance@other.test", ""), other, permission.BuiltInRoleAdmin)
	svc := NewMemberships(e.deps)

	ids := func(p MemberPage) []core.MembershipID {
		var out []core.MembershipID
		for _, v := range p.Items {
			out = append(out, v.Membership.ID)
		}
		return out
	}
	tests := []struct {
		name   string
		source string
		filter access.MembershipFilter
		want   []core.MembershipID
	}{
		{name: "active, newest first", source: "spec/queries/memberships_query_spec.rb:27", want: []core.MembershipID{both.ID, fin.ID, admin.ID}},
		{name: "status and order ignored", filter: access.MembershipFilter{Status: access.MembershipStatusRevoked, Order: core.OrderCreated}, want: []core.MembershipID{both.ID, fin.ID, admin.ID}},
		{name: "search, partial", source: "spec/queries/memberships_query_spec.rb:63", filter: access.MembershipFilter{Search: "finance"}, want: []core.MembershipID{fin.ID}},
		{name: "search, case-insensitive", source: "spec/queries/memberships_query_spec.rb:72", filter: access.MembershipFilter{Search: "BOTH"}, want: []core.MembershipID{both.ID}},
		{name: "search, wildcards literal", source: "spec/queries/memberships_query_spec.rb:81", filter: access.MembershipFilter{Search: "%"}},
		{name: "search, other organization", source: "spec/queries/memberships_query_spec.rb:101", filter: access.MembershipFilter{Search: "other.test"}},
		{name: "one role", source: "spec/queries/memberships_query_spec.rb:120", filter: access.MembershipFilter{RoleIDs: roleIDs(permission.BuiltInRoleManager)}, want: []core.MembershipID{both.ID}},
		{name: "several roles", source: "spec/queries/memberships_query_spec.rb:129", filter: access.MembershipFilter{RoleIDs: roleIDs(permission.BuiltInRoleAdmin, permission.BuiltInRoleFinance)}, want: []core.MembershipID{both.ID, fin.ID, admin.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			got, err := svc.List(e.ctx, o.ID, tt.filter, core.PageRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(ids(got), tt.want) {
				t.Errorf("got = %v, want = %v", ids(got), tt.want)
			}
			if got.AdminCount != 1 {
				t.Errorf("admin count got = %d, want = 1", got.AdminCount)
			}
		})
	}

	t.Run("view", func(t *testing.T) {
		v, err := svc.Describe(e.ctx, o.ID, both.ID)
		if err != nil {
			t.Fatal(err)
		}
		if v.User.Email != "both@tanvo.test" || len(v.Roles) != 2 {
			t.Errorf("got user = %q, roles = %d, want = both@tanvo.test, 2", v.User.Email, len(v.Roles))
		}
		// The permissions are the union of the roles' (membership.rb permissions_hash).
		perms := v.Permissions()
		for _, r := range []permission.BuiltInRole{permission.BuiltInRoleManager, permission.BuiltInRoleFinance} {
			for _, p := range permission.All() {
				if r.Grants(p) && !slices.Contains(perms, p) {
					t.Errorf("permissions lack %s of %s", p, r)
				}
			}
		}
	})
	t.Run("describe other organization", func(t *testing.T) {
		_, err := svc.Describe(e.ctx, other.ID, fin.ID)
		if got := code(err); got != "membership_not_found" {
			t.Errorf("got = %q, want = membership_not_found", got)
		}
	})
}

func TestInvitesList(t *testing.T) {
	e := newEnv(t)
	o := e.org()
	invite := func(email string, roles ...permission.BuiltInRole) *access.Invite {
		t.Helper()
		now := e.clock.Now()
		i := access.NewInvite(core.NewID[core.InviteID](e.deps.IDs), o.ID, email, roleIDs(roles...), []byte(email), now.Add(24*time.Hour), now)
		e.tx(func(ctx context.Context, r port.Repos) error { return r.Invites.Create(ctx, i) })
		e.clock.Advance(time.Second)
		return i
	}
	a := invite("alice@tanvo.test", permission.BuiltInRoleFinance)
	b := invite("Bob@tanvo.test", permission.BuiltInRoleManager)
	c := invite("carol@tanvo.test", permission.BuiltInRoleAdmin)
	e.tx(func(ctx context.Context, r port.Repos) error {
		if err := c.Revoke(e.clock.Now()); err != nil {
			return err
		}
		return r.Invites.Update(ctx, c)
	})

	tests := []struct {
		name   string
		source string
		filter access.InviteFilter
		want   []core.InviteID
	}{
		{name: "pending, newest first", source: "spec/queries/invites_query_spec.rb:28", want: []core.InviteID{b.ID, a.ID}},
		{name: "search, partial", source: "spec/queries/invites_query_spec.rb:62", filter: access.InviteFilter{Search: "alice"}, want: []core.InviteID{a.ID}},
		{name: "search, case-insensitive", source: "spec/queries/invites_query_spec.rb:71", filter: access.InviteFilter{Search: "BOB"}, want: []core.InviteID{b.ID}},
		{name: "search, wildcards literal", source: "spec/queries/invites_query_spec.rb:80", filter: access.InviteFilter{Search: "_"}},
		{name: "search, not pending", source: "spec/queries/invites_query_spec.rb:91", filter: access.InviteFilter{Search: "carol"}},
		{name: "one role", source: "spec/queries/invites_query_spec.rb:118", filter: access.InviteFilter{RoleIDs: roleIDs(permission.BuiltInRoleManager)}, want: []core.InviteID{b.ID}},
		{name: "several roles", source: "spec/queries/invites_query_spec.rb:127", filter: access.InviteFilter{RoleIDs: roleIDs(permission.BuiltInRoleManager, permission.BuiltInRoleFinance)}, want: []core.InviteID{b.ID, a.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			got, err := e.invites.List(e.ctx, o.ID, tt.filter, core.PageRequest{})
			if err != nil {
				t.Fatal(err)
			}
			var ids []core.InviteID
			for _, i := range got.Items {
				ids = append(ids, i.ID)
			}
			if !slices.Equal(ids, tt.want) {
				t.Errorf("got = %v, want = %v", ids, tt.want)
			}
		})
	}

	if got, err := e.invites.Get(e.ctx, o.ID, c.ID); err != nil || got.ID != c.ID {
		t.Errorf("get revoked got = %v, %v, want the invite", got, err)
	}
	if _, err := e.invites.Get(e.ctx, e.org().ID, a.ID); code(err) != "invite_not_found" {
		t.Errorf("get from another organization got = %q, want = invite_not_found", code(err))
	}
}

func TestAPIKeysRead(t *testing.T) {
	// source: app/models/api_key.rb (default_scope: non-expired keys)
	e := newEnv(t)
	o := e.org()
	first := e.key(o)
	e.clock.Advance(time.Second)
	second := e.key(o)
	if _, err := e.keys.Destroy(e.ctx, o.ID, first.Key.ID, false); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Second)

	if _, err := e.keys.Get(e.ctx, o.ID, first.Key.ID); code(err) != "api_key_not_found" {
		t.Errorf("get expired got = %q, want = api_key_not_found", code(err))
	}
	if got, err := e.keys.Get(e.ctx, o.ID, second.Key.ID); err != nil || got.ID != second.Key.ID {
		t.Errorf("get active got = %v, %v", got, err)
	}
	if _, err := e.keys.Get(e.ctx, e.org().ID, second.Key.ID); code(err) != "api_key_not_found" {
		t.Errorf("get from another organization got = %q, want = api_key_not_found", code(err))
	}
	page, err := e.keys.List(e.ctx, o.ID, core.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != second.Key.ID {
		t.Errorf("list got = %d keys, want = the active key", len(page.Items))
	}
}
