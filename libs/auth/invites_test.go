package auth

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func roleID(r permission.BuiltInRole) core.RoleID { return access.BuiltInRoleID(r) }

// roleIDs returns the normalized IDs of built-in roles.
func roleIDs(rs ...permission.BuiltInRole) []core.RoleID {
	out := make([]core.RoleID, len(rs))
	for i, r := range rs {
		out[i] = roleID(r)
	}
	return access.NormalizeRoleIDs(out)
}

func TestInvitesCreate(t *testing.T) {
	type fixture struct {
		e     *env
		org   *organization.Organization
		admin *access.User
		mgr   *access.User
	}
	setup := func(t *testing.T) fixture {
		e := newEnv(t)
		o := e.org()
		admin := e.user("admin@hooli.com", "pw")
		e.member(admin, o, permission.BuiltInRoleAdmin)
		mgr := e.user("manager@hooli.com", "pw")
		e.member(mgr, o, permission.BuiltInRoleManager)
		return fixture{e, o, admin, mgr}
	}
	// source: spec/services/invites/create_service_spec.rb:26
	t.Run("creates an invite", func(t *testing.T) {
		f := setup(t)
		out, err := f.e.invites.Create(f.e.ctx, InviteRequest{OrganizationID: f.org.ID, Actor: f.admin.ID, Email: "New@Hooli.com", RoleIDs: roleIDs(permission.BuiltInRoleFinance)})
		if err != nil {
			t.Fatal(err)
		}
		if out.Invite.Email != "new@hooli.com" || out.Invite.Status != access.InviteStatusPending || !slices.Equal(out.Invite.RoleIDs, roleIDs(permission.BuiltInRoleFinance)) {
			t.Errorf("invite = %+v, want a pending finance invite for new@hooli.com", out.Invite)
		}
		if out.URL != "https://app.tanvo.test/invitation/"+out.Token || len(out.Token) < 40 {
			t.Errorf("url = %s, want the invitation link", out.URL)
		}
		if string(out.Invite.TokenHash) == out.Token || len(out.Invite.TokenHash) != 32 {
			t.Error("token stored in clear")
		}
		if !out.Invite.ExpiresAt.Equal(f.e.clock.Now().Add(DefaultInviteValidity)) {
			t.Errorf("expires_at = %s, want now + %s", out.Invite.ExpiresAt, DefaultInviteValidity)
		}
		if f.e.log.count(EventUserInvited) != 1 {
			t.Errorf("events = %v, want user.invited", f.e.log.names())
		}
	})
	tests := []struct {
		name  string
		actor func(f fixture) core.UserID
		email string
		roles []core.RoleID
		prep  func(t *testing.T, f fixture)
		field string
		code  string
	}{
		// source: spec/services/invites/create_service_spec.rb:35
		{name: "non-admin invites with admin role", actor: func(f fixture) core.UserID { return f.mgr.ID }, email: "x@hooli.com", roles: roleIDs(permission.BuiltInRoleAdmin), code: CodeCannotGrantAdmin},
		{name: "system caller grants admin", actor: func(fixture) core.UserID { return core.UserID{} }, email: "x@hooli.com", roles: roleIDs(permission.BuiltInRoleAdmin)},
		{name: "admin grants admin", email: "x@hooli.com", roles: roleIDs(permission.BuiltInRoleAdmin)},
		{name: "manager invites a finance user", actor: func(f fixture) core.UserID { return f.mgr.ID }, email: "x@hooli.com", roles: roleIDs(permission.BuiltInRoleFinance)},
		{name: "several roles", actor: func(f fixture) core.UserID { return f.mgr.ID }, email: "x@hooli.com", roles: roleIDs(permission.BuiltInRoleFinance, permission.BuiltInRoleManager)},
		// lago app/services/invites/create_service.rb:56-62: any admin role among several needs an admin actor.
		{name: "non-admin invites with admin among roles", actor: func(f fixture) core.UserID { return f.mgr.ID }, email: "x@hooli.com", roles: roleIDs(permission.BuiltInRoleFinance, permission.BuiltInRoleAdmin), code: CodeCannotGrantAdmin},
		// lago app/services/invites/validate_service.rb:41-45: every role must exist.
		{name: "one unknown role among several", email: "x@hooli.com", roles: []core.RoleID{roleID(permission.BuiltInRoleFinance), core.MustParseID[core.RoleID]("0190a000-0000-7000-8000-000000000099")}, field: "roles", code: CodeInvalidRole},
		// source: spec/services/invites/create_service_spec.rb:59
		{name: "invalid email", email: "gavin", roles: roleIDs(permission.BuiltInRoleFinance), field: "email", code: core.CodeInvalidEmail},
		// source: spec/services/invites/create_service_spec.rb:81
		{name: "missing role", email: "x@hooli.com", field: "roles", code: CodeInvalidRole},
		// source: spec/services/invites/create_service_spec.rb:103
		// source: spec/services/invites/validate_service_spec.rb:58
		{name: "unknown role", email: "x@hooli.com", roles: []core.RoleID{core.MustParseID[core.RoleID]("0190a000-0000-7000-8000-000000000099")}, field: "roles", code: CodeInvalidRole},
		// source: spec/services/invites/create_service_spec.rb:126
		// source: spec/services/invites/validate_service_spec.rb:26
		{name: "already existing invite", email: "x@hooli.com", roles: roleIDs(permission.BuiltInRoleFinance), prep: func(t *testing.T, f fixture) {
			if _, err := f.e.invites.Create(f.e.ctx, InviteRequest{OrganizationID: f.org.ID, Actor: f.admin.ID, Email: "x@hooli.com", RoleIDs: roleIDs(permission.BuiltInRoleFinance)}); err != nil {
				t.Fatal(err)
			}
		}, field: "invite", code: CodeInviteAlreadyExists},
		// source: spec/services/invites/create_service_spec.rb:144
		// source: spec/services/invites/validate_service_spec.rb:43
		{name: "already existing member", email: "MANAGER@hooli.com", roles: roleIDs(permission.BuiltInRoleFinance), field: "email", code: CodeEmailAlreadyUsed},
		{name: "revoked member can be invited again", email: "gone@hooli.com", roles: roleIDs(permission.BuiltInRoleFinance), prep: func(t *testing.T, f fixture) {
			f.e.revoke(f.e.member(f.e.user("gone@hooli.com", "pw"), f.org, permission.BuiltInRoleFinance))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := setup(t)
			if tt.prep != nil {
				tt.prep(t, f)
			}
			actor := f.admin.ID
			if tt.actor != nil {
				actor = tt.actor(f)
			}
			_, err := f.e.invites.Create(f.e.ctx, InviteRequest{OrganizationID: f.org.ID, Actor: actor, Email: tt.email, RoleIDs: tt.roles})
			if code(err) != tt.code || fieldOf(err) != tt.field {
				t.Errorf("got = %s on %q, want = %s on %q", code(err), fieldOf(err), tt.code, tt.field)
			}
		})
	}
	// source: spec/services/invites/validate_service_spec.rb:22
	t.Run("collects every validation error", func(t *testing.T) {
		f := setup(t)
		_, err := f.e.invites.Create(f.e.ctx, InviteRequest{OrganizationID: f.org.ID, Actor: f.admin.ID, Email: "manager@hooli.com"})
		if fe := core.FieldErrors(err); len(fe) != 2 {
			t.Errorf("errors = %v, want email and role", fe)
		}
	})
}

func newInvite(t *testing.T, e *env, o *organization.Organization, email string, role permission.BuiltInRole) *CreatedInvite {
	t.Helper()
	out, err := e.invites.Create(e.ctx, InviteRequest{OrganizationID: o.ID, Email: email, RoleIDs: roleIDs(role)})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInvitesUpdate(t *testing.T) {
	tests := []struct {
		name  string
		actor permission.BuiltInRole
		roles []core.RoleID
		state func(t *testing.T, e *env, inv *access.Invite)
		field string
		code  string
	}{
		// source: spec/services/invites/update_service_spec.rb:13
		{name: "pending invite", actor: permission.BuiltInRoleAdmin, roles: roleIDs(permission.BuiltInRoleManager)},
		// source: spec/services/invites/update_service_spec.rb:27
		{name: "non-admin sets admin role", actor: permission.BuiltInRoleManager, roles: roleIDs(permission.BuiltInRoleAdmin), code: CodeCannotGrantAdmin},
		// source: spec/services/invites/update_service_spec.rb:42
		{name: "admin sets admin role", actor: permission.BuiltInRoleAdmin, roles: roleIDs(permission.BuiltInRoleAdmin)},
		// source: spec/services/invites/update_service_spec.rb:70
		{name: "revoked invite", actor: permission.BuiltInRoleAdmin, roles: roleIDs(permission.BuiltInRoleManager), state: func(t *testing.T, e *env, inv *access.Invite) {
			if _, err := e.invites.Revoke(e.ctx, inv.OrganizationID, inv.ID); err != nil {
				t.Fatal(err)
			}
		}, code: CodeCannotUpdateRevokedInvite},
		// source: spec/services/invites/update_service_spec.rb:81
		{name: "accepted invite", actor: permission.BuiltInRoleAdmin, roles: roleIDs(permission.BuiltInRoleManager), state: func(t *testing.T, e *env, inv *access.Invite) {
			e.tx(func(ctx context.Context, r port.Repos) error {
				if err := inv.Accept(e.clock.Now()); err != nil {
					return err
				}
				return r.Invites.Update(ctx, inv)
			})
		}, code: CodeCannotUpdateAcceptedInvite},
		// source: spec/services/invites/update_service_spec.rb:92
		{name: "empty role", actor: permission.BuiltInRoleAdmin, field: "roles", code: CodeInvalidRole},
		// source: spec/services/invites/update_service_spec.rb:105
		{name: "unknown role", actor: permission.BuiltInRoleAdmin, roles: []core.RoleID{core.MustParseID[core.RoleID]("0190a000-0000-7000-8000-000000000099")}, field: "roles", code: CodeInvalidRole},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			o := e.org()
			actor := e.user("actor@hooli.com", "pw")
			e.member(actor, o, tt.actor)
			inv := newInvite(t, e, o, "x@hooli.com", permission.BuiltInRoleFinance).Invite
			if tt.state != nil {
				tt.state(t, e, inv)
			}
			got, err := e.invites.Update(e.ctx, o.ID, actor.ID, inv.ID, tt.roles)
			if code(err) != tt.code || fieldOf(err) != tt.field {
				t.Fatalf("got = %s on %q, want = %s on %q", code(err), fieldOf(err), tt.code, tt.field)
			}
			if err == nil && !slices.Equal(got.RoleIDs, access.NormalizeRoleIDs(tt.roles)) {
				t.Errorf("roles = %v, want = %v", got.RoleIDs, tt.roles)
			}
		})
	}
	// source: spec/services/invites/update_service_spec.rb:59
	t.Run("invite not found", func(t *testing.T) {
		e := newEnv(t)
		_, err := e.invites.Update(e.ctx, e.org().ID, core.UserID{}, core.NewID[core.InviteID](e.deps.IDs), roleIDs(permission.BuiltInRoleManager))
		if code(err) != "invite_not_found" {
			t.Errorf("got = %s, want = invite_not_found", code(err))
		}
	})
}

func TestInvitesRevoke(t *testing.T) {
	tests := []struct {
		name  string
		state func(t *testing.T, e *env, inv *access.Invite)
		code  string
	}{
		// source: spec/services/invites/revoke_service_spec.rb:13
		{name: "revokes the invite"},
		// source: spec/services/invites/revoke_service_spec.rb:35
		{name: "revoked invite", state: func(t *testing.T, e *env, inv *access.Invite) {
			if _, err := e.invites.Revoke(e.ctx, inv.OrganizationID, inv.ID); err != nil {
				t.Fatal(err)
			}
		}, code: "invite_not_found"},
		// source: spec/services/invites/revoke_service_spec.rb:46
		{name: "accepted invite", state: func(t *testing.T, e *env, inv *access.Invite) {
			if _, err := e.invites.Accept(e.ctx, "", "", SessionMeta{}); code(err) != "invite_not_found" {
				t.Fatal(err)
			}
			e.tx(func(ctx context.Context, r port.Repos) error {
				if err := inv.Accept(e.clock.Now()); err != nil {
					return err
				}
				return r.Invites.Update(ctx, inv)
			})
		}, code: "invite_not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			o := e.org()
			inv := newInvite(t, e, o, "x@hooli.com", permission.BuiltInRoleFinance).Invite
			if tt.state != nil {
				tt.state(t, e, inv)
			}
			got, err := e.invites.Revoke(e.ctx, o.ID, inv.ID)
			if code(err) != tt.code {
				t.Fatalf("got = %s, want = %s", code(err), tt.code)
			}
			if err == nil && (got.Status != access.InviteStatusRevoked || !got.RevokedAt.Equal(e.clock.Now())) {
				t.Errorf("invite = %+v, want revoked now", got)
			}
		})
	}
	// source: spec/services/invites/revoke_service_spec.rb:24
	t.Run("invite not found", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.invites.Revoke(e.ctx, e.org().ID, core.NewID[core.InviteID](e.deps.IDs)); code(err) != "invite_not_found" {
			t.Errorf("got = %s, want = invite_not_found", code(err))
		}
	})
}

func TestInvitesAccept(t *testing.T) {
	// source: spec/services/invites/accept_service_spec.rb:28
	// source: spec/services/invites/accept_service_spec.rb:33
	// source: spec/services/invites/accept_service_spec.rb:41
	// source: spec/services/users_service_spec.rb:145
	t.Run("new user", func(t *testing.T) {
		e := newEnv(t)
		o := e.org()
		ci := newInvite(t, e, o, "new@hooli.com", permission.BuiltInRoleManager)
		out, err := e.invites.Accept(e.ctx, ci.Token, "ILoveLago", SessionMeta{})
		if err != nil {
			t.Fatal(err)
		}
		if out.User.Email != "new@hooli.com" || out.Organization.ID != o.ID || !slices.Equal(out.Membership.RoleIDs, roleIDs(permission.BuiltInRoleManager)) || out.Token == "" {
			t.Errorf("got = %+v, want a manager membership in the inviting organization and a session", out)
		}
		inv, err := e.invites.Lookup(e.ctx, ci.Token)
		if code(err) != "invite_not_found" || inv != nil {
			t.Errorf("invite still pending after acceptance")
		}
		if _, err := e.users.Login(e.ctx, "new@hooli.com", "ILoveLago", SessionMeta{}); err != nil {
			t.Errorf("login with the invite password: %v", err)
		}
		if e.log.count(EventUserJoined) != 1 {
			t.Errorf("events = %v, want user.joined", e.log.names())
		}
	})
	tests := []struct {
		name        string
		active      bool // existing user with another active membership
		wantNewPass bool
	}{
		// source: spec/services/users_service_spec.rb:118
		{name: "existing user without active memberships takes the password", wantNewPass: true},
		// source: spec/services/users_service_spec.rb:131
		{name: "existing user with active memberships keeps the password", active: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// source: spec/services/users_service_spec.rb:108
			e := newEnv(t)
			u := e.user("old@hooli.com", "old_password")
			if tt.active {
				e.member(u, e.org(), permission.BuiltInRoleAdmin)
			}
			o := e.org()
			ci := newInvite(t, e, o, "old@hooli.com", permission.BuiltInRoleFinance)
			out, err := e.invites.Accept(e.ctx, ci.Token, "new_password", SessionMeta{})
			if err != nil {
				t.Fatal(err)
			}
			if out.User.ID != u.ID {
				t.Errorf("user = %s, want the existing user %s", out.User.ID, u.ID)
			}
			got, _ := e.getUser("old@hooli.com")
			okNew, _, _ := e.hasher.Verify(got.PasswordHash, "new_password")
			okOld, _, _ := e.hasher.Verify(got.PasswordHash, "old_password")
			if okNew != tt.wantNewPass || okOld == tt.wantNewPass {
				t.Errorf("new password = %v, old password = %v, want new = %v", okNew, okOld, tt.wantNewPass)
			}
		})
	}
	// source: spec/services/invites/accept_service_spec.rb:50
	t.Run("user invited again after revocation", func(t *testing.T) {
		e := newEnv(t)
		o := e.org()
		u := e.user("back@hooli.com", "pw")
		e.revoke(e.member(u, o, permission.BuiltInRoleFinance))
		ci := newInvite(t, e, o, "back@hooli.com", permission.BuiltInRoleManager)
		out, err := e.invites.Accept(e.ctx, ci.Token, "pw2", SessionMeta{})
		if err != nil {
			t.Fatal(err)
		}
		if out.Membership.UserID != u.ID || e.countMemberships(o) != 2 {
			t.Errorf("membership = %+v, want a second membership of the user", out.Membership)
		}
	})
	errs := []struct {
		name    string
		token   func(ci *CreatedInvite) string
		pass    string
		methods []organization.AuthenticationMethod
		prep    func(t *testing.T, e *env, ci *CreatedInvite)
		field   string
		code    string
	}{
		// source: spec/services/invites/accept_service_spec.rb:72
		{name: "already accepted", pass: "pw", prep: func(t *testing.T, e *env, ci *CreatedInvite) {
			if _, err := e.invites.Accept(e.ctx, ci.Token, "pw", SessionMeta{}); err != nil {
				t.Fatal(err)
			}
		}, code: "invite_not_found"},
		// source: spec/services/invites/accept_service_spec.rb:87
		{name: "revoked", pass: "pw", prep: func(t *testing.T, e *env, ci *CreatedInvite) {
			if _, err := e.invites.Revoke(e.ctx, ci.Invite.OrganizationID, ci.Invite.ID); err != nil {
				t.Fatal(err)
			}
		}, code: "invite_not_found"},
		// source: spec/services/invites/accept_service_spec.rb:102
		{name: "without password", field: "password", code: core.CodeRequired},
		// source: spec/services/invites/accept_service_spec.rb:113
		{name: "without token", token: func(*CreatedInvite) string { return "" }, pass: "pw", code: "invite_not_found"},
		{name: "unknown token", token: func(*CreatedInvite) string { return "nope" }, pass: "pw", code: "invite_not_found"},
		// source: spec/services/invites/accept_service_spec.rb:123
		{name: "login method not allowed", pass: "pw", methods: []organization.AuthenticationMethod{organization.AuthenticationMethodOkta}, field: "email_password", code: CodeLoginMethodNotAuthorized},
		{name: "expired", pass: "pw", prep: func(_ *testing.T, e *env, _ *CreatedInvite) { e.clock.Advance(DefaultInviteValidity) }, code: access.CodeInviteExpired},
		{name: "email became a member meanwhile", pass: "pw", prep: func(_ *testing.T, e *env, ci *CreatedInvite) {
			o := &organization.Organization{ID: ci.Invite.OrganizationID}
			e.member(e.user("x@hooli.com", "pw"), o, permission.BuiltInRoleFinance)
		}, field: "email", code: CodeEmailAlreadyUsed},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			o := e.org(tt.methods...)
			ci := newInvite(t, e, o, "x@hooli.com", permission.BuiltInRoleFinance)
			if tt.prep != nil {
				tt.prep(t, e, ci)
			}
			tok := ci.Token
			if tt.token != nil {
				tok = tt.token(ci)
			}
			_, err := e.invites.Accept(e.ctx, tok, tt.pass, SessionMeta{})
			if code(err) != tt.code || fieldOf(err) != tt.field {
				t.Errorf("got = %s on %q, want = %s on %q", code(err), fieldOf(err), tt.code, tt.field)
			}
		})
	}
	t.Run("custom invite validity", func(t *testing.T) {
		e := newEnv(t)
		inv := NewInvites(e.deps, e.users, InvitesConfig{Validity: time.Hour})
		ci, err := inv.Create(e.ctx, InviteRequest{OrganizationID: e.org().ID, Email: "x@hooli.com", RoleIDs: roleIDs(permission.BuiltInRoleFinance)})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(ci.URL, "/invitation/") || !ci.Invite.ExpiresAt.Equal(e.clock.Now().Add(time.Hour)) {
			t.Errorf("got = %s, %s", ci.URL, ci.Invite.ExpiresAt)
		}
	})
}

func TestInvitesMultipleRoles(t *testing.T) {
	e := newEnv(t)
	o := e.org()
	custom := access.NewCustomRole(core.NewID[core.RoleID](e.deps.IDs), o.ID, "developer", "Developer", []permission.Permission{"addons:read"}, e.clock.Now())
	e.tx(func(ctx context.Context, r port.Repos) error { return r.Roles.Create(ctx, custom) })
	want := access.NormalizeRoleIDs([]core.RoleID{custom.ID, roleID(permission.BuiltInRoleFinance)})
	ci, err := e.invites.Create(e.ctx, InviteRequest{OrganizationID: o.ID, Email: "x@hooli.com", RoleIDs: want})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ci.Invite.RoleIDs, want) {
		t.Errorf("invite roles got = %v, want = %v", ci.Invite.RoleIDs, want)
	}
	// source: app/services/users_service.rb:108-115 (one membership role per invite role)
	out, err := e.invites.Accept(e.ctx, ci.Token, "pw", SessionMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out.Membership.RoleIDs, want) {
		t.Errorf("membership roles got = %v, want = %v", out.Membership.RoleIDs, want)
	}
	sess, err := e.sessions.Authenticate(e.ctx, out.Token)
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.authz.UserPrincipal(e.ctx, sess, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	// spec/models/membership_spec.rb:124: the union of the roles' permissions.
	for _, c := range []struct {
		perm permission.Permission
		want bool
	}{{"addons:read", true}, {"analytics:read", true}, {"coupons:attach", false}} {
		if got := e.authz.Check(p, c.perm) == nil; got != c.want {
			t.Errorf("Check(%s) got = %v, want = %v", c.perm, got, c.want)
		}
	}

	// source: app/services/users_service.rb:108-109 (first! fails on a discarded role)
	ci, err = e.invites.Create(e.ctx, InviteRequest{OrganizationID: o.ID, Email: "y@hooli.com", RoleIDs: []core.RoleID{custom.ID}})
	if err != nil {
		t.Fatal(err)
	}
	e.tx(func(ctx context.Context, r port.Repos) error {
		return r.Roles.Discard(ctx, o.ID, custom.ID, e.clock.Now())
	})
	if _, err := e.invites.Accept(e.ctx, ci.Token, "pw", SessionMeta{}); code(err) != "role_not_found" {
		t.Errorf("Accept() with a discarded role got = %v, want = role_not_found", err)
	}
}
