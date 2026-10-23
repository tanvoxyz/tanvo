package porttest

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

// accessFixtureUser creates a user with email.
func accessFixtureUser(t *testing.T, e *env, email string) *access.User {
	t.Helper()
	u := access.NewUser(core.NewID[core.UserID](e.ids), email, []byte("argon2id$hash"), now)
	e.tx(func(ctx context.Context, r port.Repos) error { return r.Users.Create(ctx, u) })
	return u
}

// accessCustomRole returns a custom role of org.
func accessCustomRole(e *env, org core.OrganizationID, code string) *access.Role {
	return access.NewCustomRole(core.NewID[core.RoleID](e.ids), org, code, "Role "+code,
		[]permission.Permission{"invoices:create", "customers:read"}, now)
}

func membershipID(m *access.Membership) core.MembershipID { return m.ID }
func roleID(r *access.Role) core.RoleID                   { return r.ID }
func inviteID(i *access.Invite) core.InviteID             { return i.ID }
func apiKeyID(k *access.APIKey) core.APIKeyID             { return k.ID }

func testAccess(t *testing.T, h Harness) {
	adminID := access.BuiltInRoleID(permission.BuiltInRoleAdmin)
	financeID := access.BuiltInRoleID(permission.BuiltInRoleFinance)
	managerID := access.BuiltInRoleID(permission.BuiltInRoleManager)
	roles := func(ids ...core.RoleID) []core.RoleID { return ids }

	t.Run("users", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		u := access.NewUser(core.NewID[core.UserID](e.ids), " Alice@Example.test ", []byte("argon2id$1"), now)
		u.Name = ptr("Alice")
		u.LastSignInAt = ptr(at(-time.Hour))
		sso := access.NewUser(core.NewID[core.UserID](e.ids), "bob@example.test", nil, now)
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create()", r.Users.Create(ctx, u))
			noErr(t, "Create() without password", r.Users.Create(ctx, sso))
			wantErr(t, "Create() with the same ID", r.Users.Create(ctx, u), core.ErrConflict)
			dup := &access.User{ID: core.NewID[core.UserID](e.ids), Email: "ALICE@example.test", CreatedAt: now, UpdatedAt: now}
			wantErr(t, "Create() with a taken email in another case", r.Users.Create(ctx, dup), core.ErrConflict)

			got, err := r.Users.Get(ctx, u.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, u)
			got, err = r.Users.Get(ctx, sso.ID)
			noErr(t, "Get() without password", err)
			equal(t, "Get() without password", got, sso)
			got, err = r.Users.GetByEmail(ctx, "  ALICE@example.TEST")
			noErr(t, "GetByEmail()", err)
			equal(t, "GetByEmail()", got, u)
			_, err = r.Users.GetByEmail(ctx, "nobody@example.test")
			wantErr(t, "GetByEmail() unknown", err, core.ErrNotFound)
			_, err = r.Users.Get(ctx, core.NewID[core.UserID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			u.Name = nil
			u.PasswordHash = []byte("argon2id$2")
			u.SignedIn(at(time.Minute))
			noErr(t, "Update()", r.Users.Update(ctx, u))
			got, err = r.Users.Get(ctx, u.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, u)

			sso.Email = "alice@example.test"
			wantErr(t, "Update() to a taken email", r.Users.Update(ctx, sso), core.ErrConflict)
			sso.Email = "bob@example.test"
			missing := access.NewUser(core.NewID[core.UserID](e.ids), "missing@example.test", nil, now)
			wantErr(t, "Update() unknown", r.Users.Update(ctx, missing), core.ErrNotFound)
			// The transaction stays usable after the conflicts.
			got, err = r.Users.Get(ctx, sso.ID)
			noErr(t, "Get() after conflicts", err)
			equal(t, "Get() after conflicts", got, sso)
			return nil
		})
	})

	t.Run("memberships", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		u1 := accessFixtureUser(t, e, "u1@example.test")
		u2 := accessFixtureUser(t, e, "u2@example.test")
		u3 := accessFixtureUser(t, e, "u3@example.test")
		custom := accessCustomRole(e, org, "developer")
		mid := func() core.MembershipID { return core.NewID[core.MembershipID](e.ids) }
		// m1 has several roles (lago membership_roles).
		m1 := access.NewMembership(mid(), org, u1.ID, roles(custom.ID, adminID), now)
		m2 := access.NewMembership(mid(), org, u2.ID, roles(custom.ID), now)
		m3 := access.NewMembership(mid(), org, u3.ID, roles(adminID), now)
		mb := access.NewMembership(mid(), other, u1.ID, roles(financeID), now)
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Roles.Create()", r.Roles.Create(ctx, custom))
			for _, m := range []*access.Membership{m1, m2, m3, mb} {
				noErr(t, "Create()", r.Memberships.Create(ctx, m))
			}
			wantErr(t, "Create() with the same ID", r.Memberships.Create(ctx, m1), core.ErrConflict)
			dup := access.NewMembership(mid(), org, u1.ID, roles(financeID), now)
			wantErr(t, "Create() of a second active membership", r.Memberships.Create(ctx, dup), core.ErrConflict)

			got, err := r.Memberships.Get(ctx, org, m2.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, m2)
			got, err = r.Memberships.GetActiveByUser(ctx, org, u1.ID)
			noErr(t, "GetActiveByUser()", err)
			equal(t, "GetActiveByUser()", got, m1)
			_, err = r.Memberships.Get(ctx, org, core.NewID[core.MembershipID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			// Isolation.
			_, err = r.Memberships.Get(ctx, other, m1.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.Memberships.GetActiveByUser(ctx, other, u2.ID)
			wantErr(t, "GetActiveByUser() in another organization", err, core.ErrNotFound)
			moved := *m1
			moved.OrganizationID = other
			wantErr(t, "Update() through another organization", r.Memberships.Update(ctx, &moved), core.ErrNotFound)

			// Revoking frees the user for a new active membership.
			noErr(t, "Revoke()", m2.Revoke(at(time.Hour)))
			noErr(t, "Update() revoked", r.Memberships.Update(ctx, m2))
			got, err = r.Memberships.Get(ctx, org, m2.ID)
			noErr(t, "Get() revoked", err)
			equal(t, "Get() revoked", got, m2)
			_, err = r.Memberships.GetActiveByUser(ctx, org, u2.ID)
			wantErr(t, "GetActiveByUser() revoked", err, core.ErrNotFound)
			m4 := access.NewMembership(mid(), org, u2.ID, roles(financeID), at(time.Hour))
			noErr(t, "Create() after revocation", r.Memberships.Create(ctx, m4))
			reactivated := *m2
			reactivated.Status, reactivated.RevokedAt = access.MembershipStatusActive, nil
			wantErr(t, "Update() to a second active membership", r.Memberships.Update(ctx, &reactivated), core.ErrConflict)
			noErr(t, "ChangeRoles()", m3.ChangeRoles(roles(managerID, financeID), at(time.Minute)))
			noErr(t, "Update() after a conflict", r.Memberships.Update(ctx, m3))
			got, err = r.Memberships.Get(ctx, org, m3.ID)
			noErr(t, "Get() with added roles", err)
			equal(t, "Get() with added roles", got, m3)
			noErr(t, "ChangeRoles()", m3.ChangeRoles(roles(financeID), at(2*time.Minute)))
			noErr(t, "Update() removing a role", r.Memberships.Update(ctx, m3))
			got, err = r.Memberships.Get(ctx, org, m3.ID)
			noErr(t, "Get() with a removed role", err)
			equal(t, "Get() with a removed role", got, m3)
			// A removed role's row is discarded, so the role can be added
			// again (lago membership_role.rb:46-50).
			noErr(t, "ChangeRoles()", m3.ChangeRoles(roles(managerID, financeID), at(3*time.Minute)))
			noErr(t, "Update() adding a removed role again", r.Memberships.Update(ctx, m3))
			got, err = r.Memberships.Get(ctx, org, m3.ID)
			noErr(t, "Get() with a role added again", err)
			equal(t, "Get() with a role added again", got, m3)
			noErr(t, "ChangeRoles()", m3.ChangeRoles(roles(financeID), at(4*time.Minute)))
			noErr(t, "Update() removing a role again", r.Memberships.Update(ctx, m3))
			noRoles := *m3
			noRoles.RoleIDs = nil
			wantErr(t, "Update() without roles", r.Memberships.Update(ctx, &noRoles), core.ErrInvalidValue)
			wantErr(t, "Create() without roles", r.Memberships.Create(ctx, access.NewMembership(mid(), org, u3.ID, nil, now)), core.ErrInvalidValue)

			// Cross-organization: the user's active memberships everywhere.
			mine, err := r.Memberships.ListActiveByUser(ctx, u1.ID)
			noErr(t, "ListActiveByUser()", err)
			equal(t, "ListActiveByUser()", mine, []*access.Membership{m1, mb})
			mine, err = r.Memberships.ListActiveByUser(ctx, u2.ID)
			noErr(t, "ListActiveByUser() after revocation", err)
			equal(t, "ListActiveByUser() after revocation", ids(mine, membershipID), []core.MembershipID{m4.ID})

			all := listAll(t, 1, func(p core.PageRequest) (core.Page[*access.Membership], error) {
				return r.Memberships.List(ctx, org, access.MembershipFilter{}, p)
			})
			equal(t, "List()", all, []*access.Membership{m1, m2, m3, m4})
			active := listAll(t, 2, func(p core.PageRequest) (core.Page[*access.Membership], error) {
				return r.Memberships.List(ctx, org, access.MembershipFilter{Status: access.MembershipStatusActive}, p)
			})
			equal(t, "List() active", ids(active, membershipID), []core.MembershipID{m1.ID, m3.ID, m4.ID})
			revoked := listAll(t, 2, func(p core.PageRequest) (core.Page[*access.Membership], error) {
				return r.Memberships.List(ctx, org, access.MembershipFilter{Status: access.MembershipStatusRevoked}, p)
			})
			equal(t, "List() revoked", ids(revoked, membershipID), []core.MembershipID{m2.ID})
			finance := listAll(t, 1, func(p core.PageRequest) (core.Page[*access.Membership], error) {
				return r.Memberships.List(ctx, org, access.MembershipFilter{Status: access.MembershipStatusActive, RoleID: financeID}, p)
			})
			equal(t, "List() by role", ids(finance, membershipID), []core.MembershipID{m3.ID, m4.ID})
			withCustom := listAll(t, 1, func(p core.PageRequest) (core.Page[*access.Membership], error) {
				return r.Memberships.List(ctx, org, access.MembershipFilter{RoleID: custom.ID}, p)
			})
			equal(t, "List() by a role among several", withCustom, []*access.Membership{m1, m2})
			anyRole := listAll(t, 1, func(p core.PageRequest) (core.Page[*access.Membership], error) {
				return r.Memberships.List(ctx, org, access.MembershipFilter{RoleIDs: roles(custom.ID, managerID)}, p)
			})
			equal(t, "List() by any of several roles", ids(anyRole, membershipID), []core.MembershipID{m1.ID, m2.ID})
			byEmail := listAll(t, 1, func(p core.PageRequest) (core.Page[*access.Membership], error) {
				return r.Memberships.List(ctx, org, access.MembershipFilter{Search: "U2@"}, p)
			})
			equal(t, "List() by email", ids(byEmail, membershipID), []core.MembershipID{m2.ID, m4.ID})
			recent := listAll(t, 1, func(p core.PageRequest) (core.Page[*access.Membership], error) {
				return r.Memberships.List(ctx, org, access.MembershipFilter{Order: core.OrderRecent}, p)
			})
			equal(t, "List() newest first", ids(recent, membershipID), []core.MembershipID{m4.ID, m1.ID, m2.ID, m3.ID})
			p, err := r.Memberships.List(ctx, other, access.MembershipFilter{}, core.PageRequest{})
			noErr(t, "List() of another organization", err)
			equal(t, "List() of another organization", ids(p.Items, membershipID), []core.MembershipID{mb.ID})

			for _, c := range []struct {
				org  core.OrganizationID
				role core.RoleID
				want int
			}{{org, adminID, 1}, {org, financeID, 2}, {org, custom.ID, 1}, {org, managerID, 0}, {other, financeID, 1}, {other, adminID, 0}} {
				n, err := r.Memberships.CountActiveByRole(ctx, c.org, c.role)
				noErr(t, "CountActiveByRole()", err)
				if n != c.want {
					t.Errorf("CountActiveByRole(%s) got = %d, want = %d", c.role, n, c.want)
				}
			}

			// A discarded role is no longer among the roles of its
			// memberships (lago default scope on roles); a revoked
			// membership may be left without roles.
			noErr(t, "Roles.Discard()", r.Roles.Discard(ctx, org, custom.ID, at(5*time.Minute)))
			m1.RoleIDs, m2.RoleIDs = roles(adminID), nil
			for _, m := range []*access.Membership{m1, m2} {
				got, err = r.Memberships.Get(ctx, org, m.ID)
				noErr(t, "Get() holding a discarded role", err)
				equal(t, "Get() holding a discarded role", got, m)
			}
			noErr(t, "ChangeRoles()", m1.ChangeRoles(roles(adminID, financeID), at(6*time.Minute)))
			noErr(t, "Update() holding a discarded role", r.Memberships.Update(ctx, m1))
			got, err = r.Memberships.Get(ctx, org, m1.ID)
			noErr(t, "Get() after Update() holding a discarded role", err)
			equal(t, "Get() after Update() holding a discarded role", got, m1)
			noErr(t, "Update() revoked without roles", r.Memberships.Update(ctx, m2))
			return nil
		})
	})

	t.Run("roles", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		builtIns := access.BuiltInRoles()
		r1 := accessCustomRole(e, org, "developer")
		r1.Description = ptr("Builds integrations")
		r2 := accessCustomRole(e, org, "support")
		r3 := accessCustomRole(e, other, "developer")
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, role := range []*access.Role{r1, r2, r3} {
				noErr(t, "Create()", r.Roles.Create(ctx, role))
			}
			wantErr(t, "Create() with the same ID", r.Roles.Create(ctx, r1), core.ErrConflict)
			wantErr(t, "Create() with a taken code", r.Roles.Create(ctx, accessCustomRole(e, org, "support")), core.ErrConflict)
			wantErr(t, "Create() with a built-in code", r.Roles.Create(ctx, accessCustomRole(e, org, "admin")), core.ErrConflict)
			builtInCopy := *builtIns[1]
			builtInCopy.OrganizationID = org
			wantErr(t, "Create() with a built-in ID", r.Roles.Create(ctx, &builtInCopy), core.ErrConflict)

			got, err := r.Roles.Get(ctx, org, r1.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, r1)
			got, err = r.Roles.GetByCode(ctx, org, "support")
			noErr(t, "GetByCode()", err)
			equal(t, "GetByCode()", got, r2)
			got, err = r.Roles.GetByCode(ctx, other, "developer")
			noErr(t, "GetByCode() in another organization", err)
			equal(t, "GetByCode() in another organization", got, r3)
			for _, org := range []core.OrganizationID{org, other} {
				got, err = r.Roles.Get(ctx, org, adminID)
				noErr(t, "Get() built-in", err)
				equal(t, "Get() built-in", got, builtIns[0])
				got, err = r.Roles.GetByCode(ctx, org, "finance")
				noErr(t, "GetByCode() built-in", err)
				equal(t, "GetByCode() built-in", got, builtIns[1])
			}
			_, err = r.Roles.Get(ctx, other, r1.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.Roles.GetByCode(ctx, other, "support")
			wantErr(t, "GetByCode() from another organization", err, core.ErrNotFound)
			_, err = r.Roles.Get(ctx, org, core.NewID[core.RoleID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			r1.Name = "Developer"
			r1.Description = nil
			r1.Permissions = []permission.Permission{"customers:read"}
			r1.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.Roles.Update(ctx, r1))
			got, err = r.Roles.Get(ctx, org, r1.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, r1)
			r2.Code = "developer"
			wantErr(t, "Update() to a taken code", r.Roles.Update(ctx, r2), core.ErrConflict)
			r2.Code = "manager"
			wantErr(t, "Update() to a built-in code", r.Roles.Update(ctx, r2), core.ErrConflict)
			r2.Code = "support"
			moved := *r2
			moved.OrganizationID = other
			wantErr(t, "Update() through another organization", r.Roles.Update(ctx, &moved), core.ErrNotFound)
			noErr(t, "Update() after conflicts", r.Roles.Update(ctx, r2))

			for _, size := range []int{1, 2, 4} {
				all := listAll(t, size, func(p core.PageRequest) (core.Page[*access.Role], error) {
					return r.Roles.List(ctx, org, p)
				})
				equal(t, "List()", all, append(append([]*access.Role{}, builtIns...), r1, r2))
			}
			p, err := r.Roles.List(ctx, other, core.PageRequest{})
			noErr(t, "List() of another organization", err)
			equal(t, "List() of another organization", ids(p.Items, roleID), []core.RoleID{builtIns[0].ID, builtIns[1].ID, builtIns[2].ID, r3.ID})

			wantErr(t, "Discard() from another organization", r.Roles.Discard(ctx, other, r2.ID, at(time.Hour)), core.ErrNotFound)
			wantErr(t, "Discard() built-in", r.Roles.Discard(ctx, org, adminID, at(time.Hour)), core.ErrNotFound)
			noErr(t, "Discard()", r.Roles.Discard(ctx, org, r2.ID, at(time.Hour)))
			wantErr(t, "Discard() again", r.Roles.Discard(ctx, org, r2.ID, at(time.Hour)), core.ErrNotFound)
			_, err = r.Roles.Get(ctx, org, r2.ID)
			wantErr(t, "Get() discarded", err, core.ErrNotFound)
			_, err = r.Roles.GetByCode(ctx, org, "support")
			wantErr(t, "GetByCode() discarded", err, core.ErrNotFound)
			r2.Name = "Support"
			wantErr(t, "Update() discarded", r.Roles.Update(ctx, r2), core.ErrNotFound)
			p, err = r.Roles.List(ctx, org, core.PageRequest{})
			noErr(t, "List() after Discard()", err)
			equal(t, "List() after Discard()", ids(p.Items, roleID), []core.RoleID{builtIns[0].ID, builtIns[1].ID, builtIns[2].ID, r1.ID})
			// A discarded role frees its code (lago role.rb unique index
			// on kept rows) but not its ID.
			wantErr(t, "Create() with a discarded ID", r.Roles.Create(ctx, r2), core.ErrConflict)
			noErr(t, "Create() with a freed code", r.Roles.Create(ctx, accessCustomRole(e, org, "support")))
			return nil
		})
	})

	t.Run("invites", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		newInvite := func(org core.OrganizationID, email, token string) *access.Invite {
			return access.NewInvite(core.NewID[core.InviteID](e.ids), org, email, roles(managerID, financeID), access.HashToken(token), at(48*time.Hour), now)
		}
		i1 := newInvite(org, "Carol@Example.test", "t1")
		i2 := newInvite(org, "dave@example.test", "t2")
		ib := newInvite(other, "carol@example.test", "tb")
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, i := range []*access.Invite{i1, i2, ib} {
				noErr(t, "Create()", r.Invites.Create(ctx, i))
			}
			wantErr(t, "Create() with the same ID", r.Invites.Create(ctx, i1), core.ErrConflict)
			wantErr(t, "Create() of a second pending invite", r.Invites.Create(ctx, newInvite(org, "carol@example.test", "t3")), core.ErrConflict)
			wantErr(t, "Create() with a taken token", r.Invites.Create(ctx, newInvite(org, "erin@example.test", "t1")), core.ErrConflict)
			wantErr(t, "Create() with another organization's token", r.Invites.Create(ctx, newInvite(org, "erin@example.test", "tb")), core.ErrConflict)

			got, err := r.Invites.Get(ctx, org, i1.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, i1)
			got, err = r.Invites.GetPendingByEmail(ctx, org, " CAROL@example.test")
			noErr(t, "GetPendingByEmail()", err)
			equal(t, "GetPendingByEmail()", got, i1)
			got, err = r.Invites.GetPendingByEmail(ctx, other, "carol@example.test")
			noErr(t, "GetPendingByEmail() in another organization", err)
			equal(t, "GetPendingByEmail() in another organization", got, ib)
			_, err = r.Invites.GetPendingByEmail(ctx, other, "dave@example.test")
			wantErr(t, "GetPendingByEmail() from another organization", err, core.ErrNotFound)
			_, err = r.Invites.Get(ctx, other, i1.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.Invites.Get(ctx, org, core.NewID[core.InviteID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			// Cross-organization: tokens are found in any organization.
			got, err = r.Invites.GetByTokenHash(ctx, access.HashToken("tb"))
			noErr(t, "GetByTokenHash()", err)
			equal(t, "GetByTokenHash()", got, ib)
			_, err = r.Invites.GetByTokenHash(ctx, access.HashToken("nope"))
			wantErr(t, "GetByTokenHash() unknown", err, core.ErrNotFound)

			noErr(t, "Accept()", i1.Accept(at(time.Hour)))
			noErr(t, "Update() accepted", r.Invites.Update(ctx, i1))
			got, err = r.Invites.Get(ctx, org, i1.ID)
			noErr(t, "Get() accepted", err)
			equal(t, "Get() accepted", got, i1)
			_, err = r.Invites.GetPendingByEmail(ctx, org, "carol@example.test")
			wantErr(t, "GetPendingByEmail() accepted", err, core.ErrNotFound)
			i4 := newInvite(org, "carol@example.test", "t4")
			noErr(t, "Create() after acceptance", r.Invites.Create(ctx, i4))
			i4.RoleIDs, i4.UpdatedAt = roles(adminID), at(2*time.Hour)
			noErr(t, "Update() of the roles", r.Invites.Update(ctx, i4))
			got, err = r.Invites.Get(ctx, org, i4.ID)
			noErr(t, "Get() after a role change", err)
			equal(t, "Get() after a role change", got, i4)
			pending := *i1
			pending.Status, pending.AcceptedAt = access.InviteStatusPending, nil
			wantErr(t, "Update() to a second pending invite", r.Invites.Update(ctx, &pending), core.ErrConflict)
			moved := *i2
			moved.OrganizationID = other
			wantErr(t, "Update() through another organization", r.Invites.Update(ctx, &moved), core.ErrNotFound)
			stolen := *i2
			stolen.TokenHash = access.HashToken("tb")
			wantErr(t, "Update() to another invite's token", r.Invites.Update(ctx, &stolen), core.ErrConflict)
			noErr(t, "Revoke()", i2.Revoke(at(time.Hour)))
			noErr(t, "Update() after conflicts", r.Invites.Update(ctx, i2))

			all := listAll(t, 1, func(p core.PageRequest) (core.Page[*access.Invite], error) {
				return r.Invites.List(ctx, org, access.InviteFilter{}, p)
			})
			equal(t, "List()", all, []*access.Invite{i1, i2, i4})
			for status, want := range map[access.InviteStatus][]core.InviteID{
				access.InviteStatusPending:  {i4.ID},
				access.InviteStatusAccepted: {i1.ID},
				access.InviteStatusRevoked:  {i2.ID},
			} {
				got := listAll(t, 1, func(p core.PageRequest) (core.Page[*access.Invite], error) {
					return r.Invites.List(ctx, org, access.InviteFilter{Status: status}, p)
				})
				equal(t, "List() "+string(status), ids(got, inviteID), want)
			}
			byRole := listAll(t, 1, func(p core.PageRequest) (core.Page[*access.Invite], error) {
				return r.Invites.List(ctx, org, access.InviteFilter{RoleIDs: roles(adminID, core.NewID[core.RoleID](e.ids))}, p)
			})
			equal(t, "List() by role", ids(byRole, inviteID), []core.InviteID{i4.ID})
			byEmail := listAll(t, 1, func(p core.PageRequest) (core.Page[*access.Invite], error) {
				return r.Invites.List(ctx, org, access.InviteFilter{Search: "CAROL", Status: access.InviteStatusPending}, p)
			})
			equal(t, "List() by email", ids(byEmail, inviteID), []core.InviteID{i4.ID})
			recentInvites := listAll(t, 1, func(p core.PageRequest) (core.Page[*access.Invite], error) {
				return r.Invites.List(ctx, org, access.InviteFilter{Order: core.OrderRecent, Status: access.InviteStatusPending}, p)
			})
			equal(t, "List() newest first", len(recentInvites) > 0, true)
			for i := 1; i < len(recentInvites); i++ {
				a, b := recentInvites[i-1], recentInvites[i]
				if a.CreatedAt.Before(b.CreatedAt) || (a.CreatedAt.Equal(b.CreatedAt) && a.ID.String() > b.ID.String()) {
					t.Errorf("List() newest first: %s before %s", a.ID, b.ID)
				}
			}
			p, err := r.Invites.List(ctx, other, access.InviteFilter{}, core.PageRequest{})
			noErr(t, "List() of another organization", err)
			equal(t, "List() of another organization", ids(p.Items, inviteID), []core.InviteID{ib.ID})
			return nil
		})
	})

	t.Run("password resets", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		u := accessFixtureUser(t, e, "frank@example.test")
		p1 := access.NewPasswordReset(core.NewID[core.PasswordResetID](e.ids), u.ID, access.HashToken("r1"), now)
		p2 := access.NewPasswordReset(core.NewID[core.PasswordResetID](e.ids), u.ID, access.HashToken("r2"), at(time.Hour))
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create()", r.PasswordResets.Create(ctx, p1))
			noErr(t, "Create() second", r.PasswordResets.Create(ctx, p2))
			wantErr(t, "Create() with the same ID", r.PasswordResets.Create(ctx, p1), core.ErrConflict)
			dup := access.NewPasswordReset(core.NewID[core.PasswordResetID](e.ids), u.ID, access.HashToken("r1"), now)
			wantErr(t, "Create() with a taken token", r.PasswordResets.Create(ctx, dup), core.ErrConflict)

			got, err := r.PasswordResets.GetByTokenHash(ctx, access.HashToken("r1"))
			noErr(t, "GetByTokenHash()", err)
			equal(t, "GetByTokenHash()", got, p1)
			_, err = r.PasswordResets.GetByTokenHash(ctx, access.HashToken("nope"))
			wantErr(t, "GetByTokenHash() unknown", err, core.ErrNotFound)

			stolen := *p2
			stolen.TokenHash = p1.TokenHash
			wantErr(t, "Update() to another reset's token", r.PasswordResets.Update(ctx, &stolen), core.ErrConflict)
			noErr(t, "Use()", p1.Use(at(time.Minute)))
			noErr(t, "Update()", r.PasswordResets.Update(ctx, p1))
			got, err = r.PasswordResets.GetByTokenHash(ctx, access.HashToken("r1"))
			noErr(t, "GetByTokenHash() after Update()", err)
			equal(t, "GetByTokenHash() after Update()", got, p1)
			missing := access.NewPasswordReset(core.NewID[core.PasswordResetID](e.ids), u.ID, access.HashToken("r9"), now)
			wantErr(t, "Update() unknown", r.PasswordResets.Update(ctx, missing), core.ErrNotFound)

			// p1 expires at now+30m, p2 at now+1h30m.
			n, err := r.PasswordResets.DeleteExpired(ctx, p1.ExpiresAt)
			noErr(t, "DeleteExpired() at the expiry", err)
			if n != 0 {
				t.Errorf("DeleteExpired() at the expiry got = %d, want = 0", n)
			}
			n, err = r.PasswordResets.DeleteExpired(ctx, at(time.Hour))
			noErr(t, "DeleteExpired()", err)
			if n != 1 {
				t.Errorf("DeleteExpired() got = %d, want = 1", n)
			}
			_, err = r.PasswordResets.GetByTokenHash(ctx, access.HashToken("r1"))
			wantErr(t, "GetByTokenHash() deleted", err, core.ErrNotFound)
			got, err = r.PasswordResets.GetByTokenHash(ctx, access.HashToken("r2"))
			noErr(t, "GetByTokenHash() kept", err)
			equal(t, "GetByTokenHash() kept", got, p2)
			return nil
		})
	})

	t.Run("api keys", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		newKey := func(org core.OrganizationID, secret string) *access.APIKey {
			return &access.APIKey{
				ID: core.NewID[core.APIKeyID](e.ids), OrganizationID: org, TokenHash: access.HashToken(secret),
				Prefix: secret[:4], CreatedAt: now, UpdatedAt: now,
			}
		}
		k1 := newKey(org, "secret-1")
		k1.Name = ptr("Production")
		k1.Permissions = map[permission.APIResource]permission.Level{
			permission.APIResourceInvoice: permission.LevelRead, permission.APIResourceAlert: permission.LevelWrite,
		}
		k1.LastUsedAt = ptr(at(-time.Minute))
		k2 := newKey(org, "secret-2")
		k2.ExpiresAt = ptr(at(time.Hour))
		k3 := newKey(org, "secret-3")
		k3.ExpiresAt = ptr(at(2 * time.Hour))
		kb := newKey(other, "secret-b")
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, k := range []*access.APIKey{k1, k2, k3, kb} {
				noErr(t, "Create()", r.APIKeys.Create(ctx, k))
			}
			wantErr(t, "Create() with the same ID", r.APIKeys.Create(ctx, k1), core.ErrConflict)
			wantErr(t, "Create() with another organization's token", r.APIKeys.Create(ctx, newKey(org, "secret-b")), core.ErrConflict)

			got, err := r.APIKeys.Get(ctx, org, k1.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, k1)
			got, err = r.APIKeys.Get(ctx, org, k2.ID)
			noErr(t, "Get() without name and permissions", err)
			equal(t, "Get() without name and permissions", got, k2)
			_, err = r.APIKeys.Get(ctx, other, k1.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.APIKeys.Get(ctx, org, core.NewID[core.APIKeyID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			// Cross-organization: authentication by token hash.
			got, err = r.APIKeys.GetByTokenHash(ctx, access.HashToken("secret-b"))
			noErr(t, "GetByTokenHash()", err)
			equal(t, "GetByTokenHash()", got, kb)
			_, err = r.APIKeys.GetByTokenHash(ctx, access.HashToken("nope"))
			wantErr(t, "GetByTokenHash() unknown", err, core.ErrNotFound)

			k2.Name = ptr("Rotated")
			k2.Permissions = map[permission.APIResource]permission.Level{permission.APIResourceCustomer: permission.LevelRead}
			k2.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.APIKeys.Update(ctx, k2))
			got, err = r.APIKeys.Get(ctx, org, k2.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, k2)
			taken := *k3
			taken.TokenHash = k1.TokenHash
			wantErr(t, "Update() to a taken token", r.APIKeys.Update(ctx, &taken), core.ErrConflict)
			moved := *k3
			moved.OrganizationID = other
			wantErr(t, "Update() through another organization", r.APIKeys.Update(ctx, &moved), core.ErrNotFound)

			noErr(t, "TouchLastUsed()", r.APIKeys.TouchLastUsed(ctx, org, k3.ID, at(5*time.Minute)))
			k3.Touch(at(5 * time.Minute))
			got, err = r.APIKeys.Get(ctx, org, k3.ID)
			noErr(t, "Get() after TouchLastUsed()", err)
			equal(t, "Get() after TouchLastUsed()", got, k3)
			wantErr(t, "TouchLastUsed() from another organization", r.APIKeys.TouchLastUsed(ctx, other, k3.ID, now), core.ErrNotFound)

			// Recorded uses reach last_used_at when applied; the latest
			// recorded use of a key wins, last_used_at only moves forward,
			// and uses of another organization's keys are dropped.
			noErr(t, "RecordUses()", r.APIKeys.RecordUses(ctx, org, map[core.APIKeyID]time.Time{k3.ID: at(10 * time.Minute), k1.ID: at(2 * time.Minute), kb.ID: at(3 * time.Minute)}))
			noErr(t, "RecordUses() earlier", r.APIKeys.RecordUses(ctx, org, map[core.APIKeyID]time.Time{k3.ID: at(7 * time.Minute)}))
			orgs, err := r.APIKeys.UseOrganizations(ctx)
			noErr(t, "UseOrganizations()", err)
			if !slices.Contains(orgs, org) || slices.Contains(orgs, other) {
				t.Errorf("UseOrganizations() got = %v, want = %s and not %s", orgs, org, other)
			}
			applied, err := r.APIKeys.ApplyUses(ctx, org)
			noErr(t, "ApplyUses()", err)
			if applied != 2 {
				t.Errorf("ApplyUses() got = %d, want = 2", applied)
			}
			k3.Touch(at(10 * time.Minute))
			k1.Touch(at(2 * time.Minute))
			for _, k := range []*access.APIKey{k1, k3} {
				got, err = r.APIKeys.Get(ctx, org, k.ID)
				noErr(t, "Get() after ApplyUses()", err)
				equal(t, "Get() after ApplyUses()", got, k)
			}
			got, err = r.APIKeys.GetByTokenHash(ctx, access.HashToken("secret-b"))
			noErr(t, "GetByTokenHash() after ApplyUses()", err)
			equal(t, "use of another organization's key", got, kb)
			applied, err = r.APIKeys.ApplyUses(ctx, org)
			noErr(t, "ApplyUses() again", err)
			if applied != 0 {
				t.Errorf("ApplyUses() again got = %d, want = 0", applied)
			}
			noErr(t, "RecordUses() older", r.APIKeys.RecordUses(ctx, org, map[core.APIKeyID]time.Time{k3.ID: at(time.Minute)}))
			_, err = r.APIKeys.ApplyUses(ctx, org)
			noErr(t, "ApplyUses() older", err)
			got, err = r.APIKeys.Get(ctx, org, k3.ID)
			noErr(t, "Get() after an older use", err)
			equal(t, "Get() after an older use", got, k3)

			all := listAll(t, 1, func(p core.PageRequest) (core.Page[*access.APIKey], error) {
				return r.APIKeys.List(ctx, org, access.APIKeyFilter{}, p)
			})
			equal(t, "List()", all, []*access.APIKey{k1, k2, k3})
			// k2 expires at now+1h: not active at that instant.
			active := listAll(t, 1, func(p core.PageRequest) (core.Page[*access.APIKey], error) {
				return r.APIKeys.List(ctx, org, access.APIKeyFilter{ActiveAt: ptr(at(time.Hour))}, p)
			})
			equal(t, "List() active", ids(active, apiKeyID), []core.APIKeyID{k1.ID, k3.ID})
			p, err := r.APIKeys.List(ctx, other, access.APIKeyFilter{}, core.PageRequest{})
			noErr(t, "List() of another organization", err)
			equal(t, "List() of another organization", ids(p.Items, apiKeyID), []core.APIKeyID{kb.ID})

			n, err := r.APIKeys.CountNonExpiring(ctx, org)
			noErr(t, "CountNonExpiring()", err)
			if n != 1 {
				t.Errorf("CountNonExpiring() got = %d, want = 1", n)
			}

			wantErr(t, "Delete() from another organization", r.APIKeys.Delete(ctx, other, k2.ID), core.ErrNotFound)
			noErr(t, "Delete()", r.APIKeys.Delete(ctx, org, k2.ID))
			wantErr(t, "Delete() again", r.APIKeys.Delete(ctx, org, k2.ID), core.ErrNotFound)
			_, err = r.APIKeys.GetByTokenHash(ctx, access.HashToken("secret-2"))
			wantErr(t, "GetByTokenHash() deleted", err, core.ErrNotFound)
			// A deleted key frees its token hash.
			noErr(t, "Create() with a freed token", r.APIKeys.Create(ctx, newKey(org, "secret-2")))
			return nil
		})
	})
}
