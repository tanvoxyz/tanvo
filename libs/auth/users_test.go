package auth

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
)

func TestUsersRegister(t *testing.T) {
	// source: spec/services/users_service_spec.rb:45
	t.Run("creates an organization, user and membership", func(t *testing.T) {
		e := newEnv(t)
		out, err := e.users.Register(e.ctx, "User@Email.com", "password", "organization_name", SessionMeta{UserAgent: "test"})
		if err != nil {
			t.Fatal(err)
		}
		if out.User.Email != "user@email.com" || out.Organization.Name != "organization_name" || len(out.Membership.RoleIDs) == 0 {
			t.Errorf("got = %+v, want a user, an organization and a membership", out)
		}
		if out.Token == "" || out.Session.Method != organization.AuthenticationMethodEmailPassword {
			t.Errorf("session = %+v, want an email_password session", out.Session)
		}
		if !out.User.HasPassword() || string(out.User.PasswordHash) == "password" {
			t.Error("password not hashed")
		}
		// source: spec/services/users_service_spec.rb:42 (security log user.signed_up)
		if got := e.log.count(EventUserSignedUp); got != 1 {
			t.Errorf("user.signed_up events = %d, want = 1", got)
		}
	})
	tests := []struct {
		name  string
		cfg   UsersConfig
		email string
		pass  string
		field string
		code  string
	}{
		// source: spec/services/users_service_spec.rb:61
		{name: "user already exists", email: "taken@example.com", pass: "password", field: "email", code: CodeUserAlreadyExists},
		{name: "user already exists with other casing", email: "TAKEN@example.com", pass: "password", field: "email", code: CodeUserAlreadyExists},
		// source: spec/services/users_service_spec.rb:78
		{name: "signup disabled", cfg: UsersConfig{SignupDisabled: true}, email: "new@example.com", pass: "password", code: CodeSignupDisabled},
		{name: "missing password", email: "new@example.com", field: "password", code: "value_is_mandatory"},
		{name: "invalid email", email: "not-an-email", pass: "password", field: "email", code: "invalid_email_format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, tt.cfg)
			e.user("taken@example.com", "old")
			_, err := e.users.Register(e.ctx, tt.email, tt.pass, "org", SessionMeta{})
			if code(err) != tt.code || fieldOf(err) != tt.field {
				t.Errorf("got = %s on %q, want = %s on %q", code(err), fieldOf(err), tt.code, tt.field)
			}
			if got := e.log.count(EventUserSignedUp); got != 0 {
				t.Errorf("user.signed_up events = %d, want = 0", got)
			}
		})
	}
	t.Run("two organizations with the same name get distinct slugs", func(t *testing.T) {
		e := newEnv(t)
		a, err := e.users.Register(e.ctx, "a@example.com", "pw", "Acme", SessionMeta{})
		if err != nil {
			t.Fatal(err)
		}
		b, err := e.users.Register(e.ctx, "b@example.com", "pw", "Acme", SessionMeta{})
		if err != nil {
			t.Fatal(err)
		}
		if a.Organization.Slug != "acme" || b.Organization.Slug != "acme-2" {
			t.Errorf("slugs = %s, %s, want = acme, acme-2", a.Organization.Slug, b.Organization.Slug)
		}
	})
}

func TestUsersLogin(t *testing.T) {
	tests := []struct {
		name    string
		email   string
		pass    string
		active  bool
		methods []organization.AuthenticationMethod
		sso     bool
		field   string
		code    string
	}{
		// source: spec/services/users_service_spec.rb:182
		{name: "correct password with active membership", email: "gavin@hooli.com", pass: "ILoveLago", active: true},
		{name: "email is case-insensitive", email: " GAVIN@hooli.com ", pass: "ILoveLago", active: true},
		// source: spec/services/users_service_spec.rb:206
		{name: "login method not allowed", email: "gavin@hooli.com", pass: "ILoveLago", active: true, methods: []organization.AuthenticationMethod{organization.AuthenticationMethodGoogleOAuth}, field: "email_password", code: CodeLoginMethodNotAuthorized},
		// source: spec/services/users_service_spec.rb:218
		{name: "no active membership", email: "gavin@hooli.com", pass: "ILoveLago", code: CodeIncorrectLoginOrPassword},
		// source: spec/services/users_service_spec.rb:240
		{name: "incorrect password", email: "gavin@hooli.com", pass: "wrong", active: true, code: CodeIncorrectLoginOrPassword},
		// source: spec/services/users_service_spec.rb:255
		{name: "incorrect password without membership", email: "gavin@hooli.com", pass: "wrong", code: CodeIncorrectLoginOrPassword},
		// source: spec/services/users_service_spec.rb:270
		{name: "email contains a null byte", email: "gavin@hooli.com\u0000", pass: "ILoveLago", active: true, code: CodeIncorrectLoginOrPassword},
		// source: spec/services/users_service_spec.rb:282
		{name: "password contains a null byte", email: "gavin@hooli.com", pass: "ILoveLago\u0000", active: true, code: CodeIncorrectLoginOrPassword},
		// source: spec/services/users_service_spec.rb:295
		{name: "unknown email", email: "unknown@hooli.com", pass: "ILoveLago", active: true, code: CodeIncorrectLoginOrPassword},
		{name: "SSO-only user", email: "gavin@hooli.com", pass: "", active: true, sso: true, code: CodeIncorrectLoginOrPassword},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			pw := "ILoveLago"
			if tt.sso {
				pw = ""
			}
			u := e.user("gavin@hooli.com", pw)
			o := e.org(tt.methods...)
			m := e.member(u, o, permission.BuiltInRoleAdmin)
			if !tt.active {
				e.revoke(m)
			}
			out, err := e.users.Login(e.ctx, tt.email, tt.pass, SessionMeta{})
			if code(err) != tt.code || fieldOf(err) != tt.field {
				t.Fatalf("got = %s on %q, want = %s on %q", code(err), fieldOf(err), tt.code, tt.field)
			}
			if tt.code != "" {
				return
			}
			// source: spec/services/users_service_spec.rb:198
			if out.Session.Method != organization.AuthenticationMethodEmailPassword || out.User.LastSignInAt == nil {
				t.Errorf("got = %+v, want a signed-in user with an email_password session", out)
			}
			if _, err := e.sessions.Authenticate(e.ctx, out.Token); err != nil {
				t.Errorf("session token does not authenticate: %v", err)
			}
		})
	}
}

func TestUsersLoginUpgradesHash(t *testing.T) {
	e := newEnv(t)
	u := e.user("a@example.com", "pw")
	e.member(u, e.org(), permission.BuiltInRoleAdmin)
	e.users.hasher = NewPasswordHasher(Argon2Params{Memory: 128, Time: 2, Threads: 1, SaltLength: 16, KeyLength: 32}, e.deps.Rand)
	if _, err := e.users.Login(e.ctx, "a@example.com", "pw", SessionMeta{}); err != nil {
		t.Fatal(err)
	}
	got, _ := e.getUser("a@example.com")
	if _, rehash, _ := e.users.hasher.Verify(got.PasswordHash, "pw"); rehash {
		t.Error("hash not upgraded to the current parameters")
	}
}
