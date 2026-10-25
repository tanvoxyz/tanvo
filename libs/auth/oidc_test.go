package auth

import (
	"context"
	"net/url"
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

type oidcEnv struct {
	*env
	idp   *idp
	conns *StaticConnections
	oidc  *OIDC
}

// newOIDCEnv wires the OIDC service to a local provider; prefix is the
// issuer path ("/tenant/v2.0" for Entra ID).
func newOIDCEnv(t *testing.T, prefix string, cfg ...UsersConfig) *oidcEnv {
	t.Helper()
	e := &oidcEnv{env: newEnv(t, cfg...)}
	e.idp = newIdP(t, e.clock, prefix)
	e.conns = NewStaticConnections()
	google := GoogleConnection(e.idp.clientID, e.idp.clientSecret, "https://app.tanvo.test/auth/google/callback")
	google.Issuer = e.idp.issuer
	e.oidc = NewOIDC(e.deps, e.users, e.invites, OIDCConfig{Google: google, Connections: e.conns, AllowInsecure: true})
	return e
}

// connect adds an organization connection of method claiming domains,
// provisioning memberships with the finance role.
func (e *oidcEnv) connect(o *organization.Organization, method organization.AuthenticationMethod, domains ...string) *Connection {
	e.t.Helper()
	c := &Connection{
		ID: string(method) + "-" + o.ID.String(), OrganizationID: o.ID, Method: method, Issuer: e.idp.issuer,
		ClientID: e.idp.clientID, ClientSecret: e.idp.clientSecret, RedirectURL: "https://app.tanvo.test/auth/" + string(method) + "/callback",
		Domains: NormalizeDomains(domains), DefaultRoleID: roleID(permission.BuiltInRoleFinance), CreatedAt: e.clock.Now(),
	}
	if method == organization.AuthenticationMethodEntraID {
		c.EmailFallbackClaim = "preferred_username"
	}
	if err := c.Validate(); err != nil {
		e.t.Fatal(err)
	}
	if err := e.conns.Add(c); err != nil {
		e.t.Fatal(err)
	}
	return c
}

// run begins a flow, lets the provider authenticate claims and completes
// it.
func (e *oidcEnv) run(req BeginRequest, claims map[string]any) (*SignedIn, error) {
	e.t.Helper()
	u, err := e.oidc.Begin(e.ctx, req)
	if err != nil {
		return nil, err
	}
	state, code := e.idp.authorize(u, claims)
	return e.oidc.Complete(e.ctx, CallbackRequest{State: state, Code: code})
}

func email(addr string) map[string]any { return map[string]any{"email": addr} }

func (e *oidcEnv) membershipOf(u *access.User, o *organization.Organization) *access.Membership {
	e.t.Helper()
	var m *access.Membership
	e.tx(func(ctx context.Context, r port.Repos) error {
		var err error
		m, err = r.Memberships.GetActiveByUser(ctx, o.ID, u.ID)
		return err
	})
	return m
}

var (
	google = organization.AuthenticationMethodGoogleOAuth
	okta   = organization.AuthenticationMethodOkta
	entraM = organization.AuthenticationMethodEntraID
)

func TestOIDCBegin(t *testing.T) {
	// source: spec/services/auth/google_service_spec.rb:12
	t.Run("google authorization url", func(t *testing.T) {
		e := newOIDCEnv(t, "")
		raw, err := e.oidc.Begin(e.ctx, BeginRequest{Method: google})
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(raw)
		q := u.Query()
		if q.Get("redirect_uri") != "https://app.tanvo.test/auth/google/callback" || q.Get("scope") != "openid email profile" || q.Has("login_hint") {
			t.Errorf("query = %v", q)
		}
		if q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 || q.Get("nonce") == "" {
			t.Errorf("query = %v, want PKCE S256 and a nonce", q)
		}
	})
	// source: spec/services/auth/okta/authorize_service_spec.rb:15
	// source: spec/services/auth/entra_id/authorize_service_spec.rb:15
	t.Run("organization connection authorization url", func(t *testing.T) {
		e := newOIDCEnv(t, "")
		e.connect(e.org(), okta, "hooli.com")
		raw, err := e.oidc.Begin(e.ctx, BeginRequest{Method: okta, Email: "Gavin@Hooli.com"})
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(raw)
		if q := u.Query(); q.Get("login_hint") != "gavin@hooli.com" || q.Get("redirect_uri") != "https://app.tanvo.test/auth/okta/callback" {
			t.Errorf("query = %v", q)
		}
	})
	tests := []struct {
		name   string
		google bool // keep the Google connection
		req    func(e *oidcEnv) BeginRequest
		field  string
		code   string
	}{
		// source: spec/services/auth/google_service_spec.rb:21
		{name: "google not set up", req: func(*oidcEnv) BeginRequest { return BeginRequest{Method: google} }, code: CodeOIDCMissingSetup},
		// source: spec/services/auth/google_service_spec.rb:175
		{name: "google sign-up not set up", req: func(*oidcEnv) BeginRequest { return BeginRequest{Method: google, Intent: IntentSignUp} }, code: CodeOIDCMissingSetup},
		// source: spec/services/auth/okta/authorize_service_spec.rb:23
		// source: spec/services/auth/entra_id/authorize_service_spec.rb:60
		{name: "domain not configured", google: true, req: func(*oidcEnv) BeginRequest { return BeginRequest{Method: okta, Email: "gavin@nope.com"} }, code: CodeDomainNotConfigured},
		{name: "domain of another method", google: true, req: func(*oidcEnv) BeginRequest { return BeginRequest{Method: entraM, Email: "gavin@hooli.com"} }, code: CodeDomainNotConfigured},
		{name: "missing email", google: true, req: func(*oidcEnv) BeginRequest { return BeginRequest{Method: okta} }, field: "email", code: core.CodeRequired},
		{name: "email password", google: true, req: func(*oidcEnv) BeginRequest {
			return BeginRequest{Method: organization.AuthenticationMethodEmailPassword, Email: "gavin@hooli.com"}
		}, field: "method", code: core.CodeInvalid},
		{name: "sign-up through an organization connection", google: true, req: func(*oidcEnv) BeginRequest {
			return BeginRequest{Method: okta, Intent: IntentSignUp, Email: "gavin@hooli.com"}
		}, field: "intent", code: core.CodeInvalid},
		// source: spec/services/auth/okta/authorize_service_spec.rb:47
		// source: spec/services/auth/entra_id/authorize_service_spec.rb:84
		{name: "invite for another email", google: true, req: func(e *oidcEnv) BeginRequest {
			ci := newInvite(e.t, e.env, e.org(), "other@hooli.com", permission.BuiltInRoleFinance)
			return BeginRequest{Method: okta, Intent: IntentAcceptInvite, Email: "gavin@hooli.com", InviteToken: ci.Token}
		}, code: CodeInviteEmailMismatch},
		// source: spec/services/auth/okta/authorize_service_spec.rb:58
		// source: spec/services/auth/entra_id/authorize_service_spec.rb:95
		// source: spec/services/auth/google_service_spec.rb:219
		{name: "invite not found", google: true, req: func(*oidcEnv) BeginRequest {
			return BeginRequest{Method: okta, Intent: IntentAcceptInvite, Email: "gavin@hooli.com", InviteToken: "nope"}
		}, code: "invite_not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newOIDCEnv(t, "")
			if !tt.google {
				e.oidc.cfg.Google = nil
			}
			e.connect(e.org(), okta, "hooli.com")
			_, err := e.oidc.Begin(e.ctx, tt.req(e))
			if code(err) != tt.code || fieldOf(err) != tt.field {
				t.Errorf("got = %s on %q, want = %s on %q", code(err), fieldOf(err), tt.code, tt.field)
			}
		})
	}
	// source: spec/services/auth/okta/authorize_service_spec.rb:39
	// source: spec/services/auth/entra_id/authorize_service_spec.rb:76
	t.Run("with invite token", func(t *testing.T) {
		e := newOIDCEnv(t, "")
		o := e.org()
		e.connect(o, okta, "hooli.com")
		ci := newInvite(t, e.env, o, "gavin@hooli.com", permission.BuiltInRoleFinance)
		if _, err := e.oidc.Begin(e.ctx, BeginRequest{Method: okta, Intent: IntentAcceptInvite, Email: "gavin@hooli.com", InviteToken: ci.Token}); err != nil {
			t.Error(err)
		}
	})
	t.Run("discovery failure", func(t *testing.T) {
		e := newOIDCEnv(t, "")
		e.oidc.cfg.Google.Issuer = e.idp.issuer + "/missing"
		if _, err := e.oidc.Begin(e.ctx, BeginRequest{Method: google}); code(err) != CodeOIDCRequestError {
			t.Errorf("got = %s, want = %s", code(err), CodeOIDCRequestError)
		}
	})
}

func TestOIDCGoogle(t *testing.T) {
	tests := []struct {
		name   string
		intent Intent
		setup  func(e *oidcEnv) BeginRequest
		claims map[string]any
		cfg    UsersConfig
		field  string
		code   string
		check  func(t *testing.T, e *oidcEnv, out *SignedIn)
	}{
		// source: spec/services/auth/google_service_spec.rb:64
		{name: "login", setup: func(e *oidcEnv) BeginRequest {
			e.member(e.user("foo@bar.com", "foobar"), e.org(), permission.BuiltInRoleAdmin)
			return BeginRequest{Method: google}
		}, claims: email("foo@bar.com"), check: func(t *testing.T, e *oidcEnv, out *SignedIn) {
			if out.User.Email != "foo@bar.com" || out.Session.Method != google {
				t.Errorf("got = %s via %s", out.User.Email, out.Session.Method)
			}
			// A sign-in is logged only from a new device (user_devices/register_service.rb:36-46).
			if out.User.LastSignInAt == nil || len(e.log.names()) != 0 {
				t.Errorf("last sign-in = %v, events = %v, want a sign-in time and no event", out.User.LastSignInAt, e.log.names())
			}
		}},
		// source: spec/services/auth/google_service_spec.rb:77
		{name: "user does not exist", setup: func(*oidcEnv) BeginRequest { return BeginRequest{Method: google} }, claims: email("foo@bar.com"), code: CodeUserDoesNotExist},
		// source: spec/services/auth/google_service_spec.rb:86
		{name: "login method not allowed", setup: func(e *oidcEnv) BeginRequest {
			e.member(e.user("foo@bar.com", "foobar"), e.org(organization.AuthenticationMethodEmailPassword), permission.BuiltInRoleAdmin)
			return BeginRequest{Method: google}
		}, claims: email("foo@bar.com"), field: "google_oauth", code: CodeLoginMethodNotAuthorized},
		// source: spec/services/auth/google_service_spec.rb:102
		{name: "no active memberships", setup: func(e *oidcEnv) BeginRequest {
			e.revoke(e.member(e.user("foo@bar.com", "foobar"), e.org(), permission.BuiltInRoleAdmin))
			return BeginRequest{Method: google}
		}, claims: email("foo@bar.com"), code: CodeUserDoesNotExist},
		// source: spec/services/auth/google_service_spec.rb:152
		{name: "register", setup: func(*oidcEnv) BeginRequest {
			return BeginRequest{Method: google, Intent: IntentSignUp, OrganizationName: "FooBar"}
		}, claims: email("Foo@Bar.com"), check: func(t *testing.T, e *oidcEnv, out *SignedIn) {
			if out.User.Email != "foo@bar.com" || out.Organization.Name != "FooBar" || !slices.Equal(out.Membership.RoleIDs, roleIDs(permission.BuiltInRoleAdmin)) || out.Session.Method != google {
				t.Errorf("got = %+v", out)
			}
			if len(out.User.PasswordHash) != 0 {
				t.Error("SSO sign-up set a password")
			}
		}},
		// source: spec/services/auth/google_service_spec.rb:164
		{name: "register an existing user", setup: func(e *oidcEnv) BeginRequest {
			e.user("foo@bar.com", "")
			return BeginRequest{Method: google, Intent: IntentSignUp, OrganizationName: "FooBar"}
		}, claims: email("foo@bar.com"), field: "email", code: CodeUserAlreadyExists},
		{name: "register with sign-up disabled", cfg: UsersConfig{SignupDisabled: true}, setup: func(*oidcEnv) BeginRequest {
			return BeginRequest{Method: google, Intent: IntentSignUp, OrganizationName: "FooBar"}
		}, claims: email("foo@bar.com"), code: CodeSignupDisabled},
		// source: spec/services/auth/google_service_spec.rb:206
		{name: "accept invite", setup: func(e *oidcEnv) BeginRequest {
			ci := newInvite(e.t, e.env, e.org(), "invited@bar.com", permission.BuiltInRoleManager)
			return BeginRequest{Method: google, Intent: IntentAcceptInvite, InviteToken: ci.Token}
		}, claims: email("invited@bar.com"), check: func(t *testing.T, e *oidcEnv, out *SignedIn) {
			if out.User.Email != "invited@bar.com" || !slices.Equal(out.Membership.RoleIDs, roleIDs(permission.BuiltInRoleManager)) || out.Session.Method != google || out.Token == "" {
				t.Errorf("got = %+v", out)
			}
			if len(out.User.PasswordHash) != 0 {
				t.Error("SSO acceptance set a password")
			}
		}},
		// source: spec/services/auth/google_service_spec.rb:228
		{name: "invite for another email", setup: func(e *oidcEnv) BeginRequest {
			ci := newInvite(e.t, e.env, e.org(), "invited@bar.com", permission.BuiltInRoleManager)
			return BeginRequest{Method: google, Intent: IntentAcceptInvite, InviteToken: ci.Token}
		}, claims: email("foo@bar.com"), code: CodeInviteEmailMismatch},
		{name: "unverified email", setup: func(e *oidcEnv) BeginRequest {
			e.member(e.user("foo@bar.com", "foobar"), e.org(), permission.BuiltInRoleAdmin)
			return BeginRequest{Method: google}
		}, claims: map[string]any{"email": "foo@bar.com", "email_verified": false}, code: CodeOIDCEmailNotVerified},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newOIDCEnv(t, "", tt.cfg)
			out, err := e.run(tt.setup(e), tt.claims)
			if code(err) != tt.code || fieldOf(err) != tt.field {
				t.Fatalf("got = %s on %q, want = %s on %q", code(err), fieldOf(err), tt.code, tt.field)
			}
			if tt.check != nil {
				tt.check(t, e, out)
			}
		})
	}
}

// TestOIDCOrganizationLogin ports the Okta and Entra ID login specs, which
// share their behavior.
func TestOIDCOrganizationLogin(t *testing.T) {
	type fixture struct {
		e   *oidcEnv
		org *organization.Organization
	}
	tests := []struct {
		name   string
		method organization.AuthenticationMethod
		setup  func(f fixture)
		typed  string
		claims map[string]any
		ui     map[string]any
		field  string
		code   string
		check  func(t *testing.T, f fixture, out *SignedIn)
	}{
		// source: spec/services/auth/okta/login_service_spec.rb:48
		// source: spec/services/auth/entra_id/login_service_spec.rb:48
		{name: "creates the user and the membership", method: okta, typed: "gavin@hooli.com", claims: email("gavin@hooli.com"), check: func(t *testing.T, f fixture, out *SignedIn) {
			if out.User.Email != "gavin@hooli.com" || len(out.User.PasswordHash) != 0 || out.Session.Method != okta {
				t.Errorf("user = %+v", out.User)
			}
			if m := f.e.membershipOf(out.User, f.org); !slices.Equal(m.RoleIDs, roleIDs(permission.BuiltInRoleFinance)) {
				t.Errorf("roles = %v, want the connection's default role", m.RoleIDs)
			}
		}},
		{name: "entra creates the user and the membership", method: entraM, typed: "gavin@hooli.com", claims: email("gavin@hooli.com")},
		// source: spec/services/auth/okta/login_service_spec.rb:80
		// source: spec/services/auth/entra_id/login_service_spec.rb:80
		{name: "login method not allowed", method: okta, setup: func(f fixture) {
			f.e.tx(func(ctx context.Context, r port.Repos) error {
				f.org.AuthenticationMethods = []organization.AuthenticationMethod{organization.AuthenticationMethodEmailPassword}
				return r.Organizations.Update(ctx, f.org)
			})
		}, typed: "gavin@hooli.com", claims: email("gavin@hooli.com"), field: "okta", code: CodeLoginMethodNotAuthorized, check: func(t *testing.T, f fixture, _ *SignedIn) {
			if _, ok := f.e.getUser("gavin@hooli.com"); ok || f.e.countMemberships(f.org) != 0 {
				t.Error("refused sign-in left a user or a membership")
			}
		}},
		// source: spec/services/auth/okta/login_service_spec.rb:105
		{name: "okta email differs from the typed one", method: okta, typed: "gavin@hooli.com", claims: email("other@hooli.com"), code: "okta_userinfo_error"},
		// source: spec/services/auth/entra_id/login_service_spec.rb:105
		{name: "entra email differs from the typed one", method: entraM, typed: "gavin@hooli.com", claims: email("other@hooli.com"), code: "entra_id_userinfo_error"},
		// source: spec/services/auth/entra_id/login_service_spec.rb:116
		{name: "preferred_username fallback", method: entraM, typed: "gavin@hooli.com", claims: map[string]any{"preferred_username": "gavin@hooli.com"}, ui: map[string]any{"name": "Gavin"}},
		{name: "no fallback outside entra", method: okta, typed: "gavin@hooli.com", claims: map[string]any{"preferred_username": "gavin@hooli.com"}, ui: map[string]any{}, code: "okta_userinfo_error"},
		// source: spec/services/auth/entra_id/login_service_spec.rb:127
		{name: "email casing differs", method: entraM, typed: "gavin@hooli.com", claims: email("Gavin@HOOLI.com")},
		// source: spec/services/auth/entra_id/login_service_spec.rb:138
		{name: "neither email nor preferred_username", method: entraM, typed: "gavin@hooli.com", claims: map[string]any{"name": "Gavin"}, ui: map[string]any{}, code: "entra_id_userinfo_error"},
		{name: "email from userinfo", method: okta, typed: "gavin@hooli.com", claims: map[string]any{}, ui: email("gavin@hooli.com")},
		// source: spec/services/auth/entra_id/login_service_spec.rb:149
		{name: "additional domain", method: entraM, typed: "gavin@piedpiper.com", claims: email("gavin@piedpiper.com"), check: func(t *testing.T, f fixture, out *SignedIn) {
			if f.e.membershipOf(out.User, f.org) == nil {
				t.Error("no membership in the connection's organization")
			}
		}},
		// source: spec/services/auth/entra_id/login_service_spec.rb:161
		{name: "existing user typed with another casing", method: entraM, setup: func(f fixture) {
			f.e.member(f.e.user("gavin@hooli.com", "pw"), f.org, permission.BuiltInRoleAdmin)
		}, typed: "GAVIN@hooli.com", claims: email("gavin@hooli.com")},
		// source: spec/services/auth/okta/login_service_spec.rb:116
		// source: spec/services/auth/entra_id/login_service_spec.rb:182
		{name: "user already exists", method: okta, setup: func(f fixture) { f.e.user("gavin@hooli.com", "pw") }, typed: "gavin@hooli.com", claims: email("gavin@hooli.com"), check: func(t *testing.T, f fixture, out *SignedIn) {
			if u, _ := f.e.getUser("gavin@hooli.com"); u.ID != out.User.ID || len(out.User.PasswordHash) == 0 {
				t.Error("existing user replaced or lost its password")
			}
		}},
		// source: spec/services/auth/okta/login_service_spec.rb:126
		// source: spec/services/auth/entra_id/login_service_spec.rb:192
		{name: "membership already exists", method: okta, setup: func(f fixture) {
			f.e.member(f.e.user("gavin@hooli.com", "pw"), f.org, permission.BuiltInRoleAdmin)
		}, typed: "gavin@hooli.com", claims: email("gavin@hooli.com"), check: func(t *testing.T, f fixture, out *SignedIn) {
			if n := f.e.countMemberships(f.org); n != 1 || !slices.Equal(f.e.membershipOf(out.User, f.org).RoleIDs, roleIDs(permission.BuiltInRoleAdmin)) {
				t.Errorf("memberships = %d, want the existing admin membership only", n)
			}
		}},
		{name: "revoked membership is not revived", method: okta, setup: func(f fixture) {
			f.e.revoke(f.e.member(f.e.user("gavin@hooli.com", "pw"), f.org, permission.BuiltInRoleAdmin))
		}, typed: "gavin@hooli.com", claims: email("gavin@hooli.com"), code: CodeMembershipRevoked},
		{name: "unverified email", method: okta, typed: "gavin@hooli.com", claims: map[string]any{"email": "gavin@hooli.com", "email_verified": false}, code: CodeOIDCEmailNotVerified},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix := ""
			if tt.method == entraM {
				prefix = "/3f2a-tenant/v2.0"
			}
			e := newOIDCEnv(t, prefix)
			f := fixture{e: e, org: e.org()}
			e.connect(f.org, tt.method, "hooli.com", "piedpiper.com")
			if tt.setup != nil {
				tt.setup(f)
			}
			e.idp.userinfo = tt.ui
			out, err := e.run(BeginRequest{Method: tt.method, Email: tt.typed}, tt.claims)
			if code(err) != tt.code || fieldOf(err) != tt.field {
				t.Fatalf("got = %s on %q, want = %s on %q", code(err), fieldOf(err), tt.code, tt.field)
			}
			if err == nil && (out.User.Email != access.NormalizeEmail(tt.typed) || out.Token == "") {
				t.Errorf("signed in %s, want = %s", out.User.Email, tt.typed)
			}
			if tt.check != nil {
				tt.check(t, f, out)
			}
		})
	}
}

func TestOIDCOrganizationAcceptInvite(t *testing.T) {
	for _, m := range []organization.AuthenticationMethod{okta, entraM} {
		t.Run(string(m), func(t *testing.T) {
			// source: spec/services/auth/okta/accept_invite_service_spec.rb:32
			// source: spec/services/auth/entra_id/accept_invite_service_spec.rb:32
			e := newOIDCEnv(t, "")
			o := e.org()
			e.connect(o, m, "hooli.com")
			ci := newInvite(t, e.env, o, "gavin@hooli.com", permission.BuiltInRoleManager)
			out, err := e.run(BeginRequest{Method: m, Intent: IntentAcceptInvite, Email: "gavin@hooli.com", InviteToken: ci.Token}, email("gavin@hooli.com"))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(out.Membership.RoleIDs, roleIDs(permission.BuiltInRoleManager)) || out.Session.Method != m {
				t.Errorf("got = %+v, want the invite's role", out.Membership)
			}
			if _, err := e.invites.Lookup(e.ctx, ci.Token); code(err) != "invite_not_found" {
				t.Error("invite still pending")
			}
		})
	}
	tests := []struct {
		name   string
		prep   func(e *oidcEnv, ci *CreatedInvite)
		claims map[string]any
		code   string
	}{
		// source: spec/services/auth/okta/accept_invite_service_spec.rb:90
		// source: spec/services/auth/entra_id/accept_invite_service_spec.rb:90
		{name: "invite revoked during the flow", prep: func(e *oidcEnv, ci *CreatedInvite) {
			if _, err := e.invites.Revoke(e.ctx, ci.Invite.OrganizationID, ci.Invite.ID); err != nil {
				e.t.Fatal(err)
			}
		}, claims: email("gavin@hooli.com"), code: "invite_not_found"},
		// source: spec/services/auth/okta/accept_invite_service_spec.rb:101
		{name: "provider email differs", claims: email("other@hooli.com"), code: "okta_userinfo_error"},
		{name: "organization disallows the method", prep: func(e *oidcEnv, ci *CreatedInvite) {
			e.tx(func(ctx context.Context, r port.Repos) error {
				o, err := r.Organizations.Get(ctx, ci.Invite.OrganizationID)
				if err != nil {
					return err
				}
				o.AuthenticationMethods = []organization.AuthenticationMethod{organization.AuthenticationMethodEmailPassword}
				return r.Organizations.Update(ctx, o)
			})
		}, claims: email("gavin@hooli.com"), code: CodeLoginMethodNotAuthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newOIDCEnv(t, "")
			o := e.org()
			e.connect(o, okta, "hooli.com")
			ci := newInvite(t, e.env, o, "gavin@hooli.com", permission.BuiltInRoleManager)
			u, err := e.oidc.Begin(e.ctx, BeginRequest{Method: okta, Intent: IntentAcceptInvite, Email: "gavin@hooli.com", InviteToken: ci.Token})
			if err != nil {
				t.Fatal(err)
			}
			if tt.prep != nil {
				tt.prep(e, ci)
			}
			state, c := e.idp.authorize(u, tt.claims)
			if _, err := e.oidc.Complete(e.ctx, CallbackRequest{State: state, Code: c}); code(err) != tt.code {
				t.Errorf("got = %s, want = %s", code(err), tt.code)
			}
		})
	}
}

func TestOIDCCallback(t *testing.T) {
	tests := []struct {
		name string
		// complete runs the callback after Begin returned the URL.
		complete func(e *oidcEnv, authURL string) error
		code     string
	}{
		// source: spec/services/auth/okta/login_service_spec.rb:59
		// source: spec/services/auth/okta/accept_invite_service_spec.rb:44
		{name: "code not provided", complete: func(e *oidcEnv, u string) error {
			state, _ := e.idp.authorize(u, email("gavin@hooli.com"))
			_, err := e.oidc.Complete(e.ctx, CallbackRequest{State: state})
			return err
		}, code: CodeCodeNotFound},
		// source: spec/services/auth/okta/login_service_spec.rb:70
		// source: spec/services/auth/okta/accept_invite_service_spec.rb:55
		{name: "state not provided", complete: func(e *oidcEnv, u string) error {
			_, c := e.idp.authorize(u, email("gavin@hooli.com"))
			_, err := e.oidc.Complete(e.ctx, CallbackRequest{Code: c})
			return err
		}, code: CodeStateNotFound},
		// source: spec/services/auth/okta/accept_invite_service_spec.rb:66
		{name: "state not found", complete: func(e *oidcEnv, u string) error {
			_, c := e.idp.authorize(u, email("gavin@hooli.com"))
			_, err := e.oidc.Complete(e.ctx, CallbackRequest{State: "forged", Code: c})
			return err
		}, code: CodeStateNotFound},
		{name: "state is single-use", complete: func(e *oidcEnv, u string) error {
			state, c := e.idp.authorize(u, email("gavin@hooli.com"))
			if _, err := e.oidc.Complete(e.ctx, CallbackRequest{State: state, Code: c}); err != nil {
				e.t.Fatal(err)
			}
			_, err := e.oidc.Complete(e.ctx, CallbackRequest{State: state, Code: c})
			return err
		}, code: CodeStateNotFound},
		{name: "state expired", complete: func(e *oidcEnv, u string) error {
			state, c := e.idp.authorize(u, email("gavin@hooli.com"))
			e.clock.Advance(DefaultStateTTL)
			_, err := e.oidc.Complete(e.ctx, CallbackRequest{State: state, Code: c})
			return err
		}, code: CodeStateNotFound},
		// source: spec/services/auth/okta/login_service_spec.rb:94
		// source: spec/services/auth/okta/accept_invite_service_spec.rb:79
		{name: "connection removed during the flow", complete: func(e *oidcEnv, u string) error {
			state, c := e.idp.authorize(u, email("gavin@hooli.com"))
			e.oidc.cfg.Connections = NewStaticConnections()
			_, err := e.oidc.Complete(e.ctx, CallbackRequest{State: state, Code: c})
			return err
		}, code: CodeDomainNotConfigured},
		{name: "code of another flow fails PKCE", complete: func(e *oidcEnv, u string) error {
			_, c := e.idp.authorize(u, email("gavin@hooli.com"))
			u2, err := e.oidc.Begin(e.ctx, BeginRequest{Method: okta, Email: "gavin@hooli.com"})
			if err != nil {
				e.t.Fatal(err)
			}
			state2, _ := e.idp.authorize(u2, email("gavin@hooli.com"))
			_, err = e.oidc.Complete(e.ctx, CallbackRequest{State: state2, Code: c})
			return err
		}, code: CodeOIDCRequestError},
		{name: "nonce mismatch", complete: func(e *oidcEnv, u string) error {
			e.idp.nonce = "replayed"
			return e.complete(u)
		}, code: CodeOIDCInvalidToken},
		{name: "forged signature", complete: func(e *oidcEnv, u string) error {
			e.idp.tamper = true
			return e.complete(u)
		}, code: CodeOIDCInvalidToken},
		{name: "token for another client", complete: func(e *oidcEnv, u string) error {
			e.idp.audience = "someone-else"
			return e.complete(u)
		}, code: CodeOIDCInvalidToken},
		{name: "expired ID token", complete: func(e *oidcEnv, u string) error {
			e.idp.ttl = -time.Minute
			return e.complete(u)
		}, code: CodeOIDCInvalidToken},
		{name: "wrong client secret", complete: func(e *oidcEnv, u string) error {
			e.idp.clientSecret = "rotated"
			return e.complete(u)
		}, code: CodeOIDCRequestError},
		{name: "succeeds", complete: func(e *oidcEnv, u string) error { return e.complete(u) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newOIDCEnv(t, "")
			e.connect(e.org(), okta, "hooli.com")
			u, err := e.oidc.Begin(e.ctx, BeginRequest{Method: okta, Email: "gavin@hooli.com"})
			if err != nil {
				t.Fatal(err)
			}
			if err := tt.complete(e, u); code(err) != tt.code {
				t.Errorf("got = %s, want = %s", code(err), tt.code)
			}
		})
	}
}

func (e *oidcEnv) complete(authURL string) error {
	state, c := e.idp.authorize(authURL, email("gavin@hooli.com"))
	_, err := e.oidc.Complete(e.ctx, CallbackRequest{State: state, Code: c})
	return err
}

func TestOIDCSSOUserHasNoPassword(t *testing.T) {
	e := newOIDCEnv(t, "")
	o := e.org()
	e.connect(o, okta, "hooli.com")
	if _, err := e.run(BeginRequest{Method: okta, Email: "gavin@hooli.com"}, email("gavin@hooli.com")); err != nil {
		t.Fatal(err)
	}
	for _, pw := range []string{"", "x", strings.Repeat("0", 32)} {
		if _, err := e.users.Login(e.ctx, "gavin@hooli.com", pw, SessionMeta{}); code(err) != CodeIncorrectLoginOrPassword {
			t.Errorf("password login %q = %s, want = %s", pw, code(err), CodeIncorrectLoginOrPassword)
		}
	}
}
