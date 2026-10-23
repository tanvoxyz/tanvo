package auth

import (
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
)

func (e *env) resetToken(email string) string {
	e.t.Helper()
	if _, err := e.resets.Request(e.ctx, email); err != nil {
		e.t.Fatal(err)
	}
	m := e.mailJobs()
	if len(m) != 1 {
		e.t.Fatalf("mails = %d, want = 1", len(m))
	}
	u := m[0].Data["reset_url"]
	tok, ok := strings.CutPrefix(u, "https://app.tanvo.test/reset-password/")
	if !ok {
		e.t.Fatalf("reset_url = %s", u)
	}
	return tok
}

func TestPasswordResetsRequest(t *testing.T) {
	// source: spec/services/password_resets/create_service_spec.rb:20
	// source: spec/services/password_resets/create_service_spec.rb:29
	// source: spec/services/password_resets/create_service_spec.rb:50
	t.Run("creates a reset and enqueues the mail", func(t *testing.T) {
		e := newEnv(t)
		u := e.user("gavin@hooli.com", "pw")
		e.member(u, e.org(), permission.BuiltInRoleAdmin)
		e.member(u, e.org(), permission.BuiltInRoleFinance)
		pr, err := e.resets.Request(e.ctx, "Gavin@Hooli.com")
		if err != nil {
			t.Fatal(err)
		}
		if pr.UserID != u.ID || !pr.ExpiresAt.Equal(e.clock.Now().Add(access.PasswordResetValidity)) {
			t.Errorf("reset = %+v, want one for the user valid 30 minutes", pr)
		}
		m := e.mailJobs()
		if len(m) != 1 || m[0].Template != MailPasswordReset || m[0].To != "gavin@hooli.com" {
			t.Errorf("mails = %+v", m)
		}
		if e.log.count(EventPasswordResetRequested) != 2 {
			t.Errorf("events = %v", e.log.names())
		}
	})
	// source: spec/services/password_resets/create_service_spec.rb:37
	t.Run("unknown user", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.resets.Request(e.ctx, "nobody@hooli.com"); code(err) != "user_not_found" {
			t.Errorf("got = %s, want = user_not_found", code(err))
		}
		if len(e.mailJobs()) != 0 {
			t.Error("mail enqueued for an unknown user")
		}
	})
}

func TestPasswordResetsReset(t *testing.T) {
	// source: spec/services/password_resets/reset_service_spec.rb:24
	// source: spec/services/password_resets/reset_service_spec.rb:30
	// source: spec/services/password_resets/reset_service_spec.rb:39
	t.Run("resets the password and signs in", func(t *testing.T) {
		e := newEnv(t)
		u := e.user("gavin@hooli.com", "old")
		e.member(u, e.org(), permission.BuiltInRoleAdmin)
		old, _, _ := e.sessions.Issue(e.ctx, u.ID, organization.AuthenticationMethodEmailPassword, SessionMeta{})
		tok := e.resetToken(u.Email)
		out, err := e.resets.Reset(e.ctx, tok, "new", SessionMeta{})
		if err != nil {
			t.Fatal(err)
		}
		if out.User.ID != u.ID || out.Token == "" {
			t.Errorf("got = %+v", out)
		}
		if _, err := e.users.Login(e.ctx, u.Email, "new", SessionMeta{}); err != nil {
			t.Errorf("login with the new password: %v", err)
		}
		if _, err := e.users.Login(e.ctx, u.Email, "old", SessionMeta{}); code(err) != CodeIncorrectLoginOrPassword {
			t.Errorf("login with the old password = %v", err)
		}
		if _, err := e.sessions.Authenticate(e.ctx, old); err == nil {
			t.Error("session from before the reset still valid")
		}
		if _, err := e.sessions.Authenticate(e.ctx, out.Token); err != nil {
			t.Errorf("new session: %v", err)
		}
		if _, err := e.resets.Reset(e.ctx, tok, "again", SessionMeta{}); code(err) != "password_reset_not_found" {
			t.Errorf("reuse = %s, want = password_reset_not_found", code(err))
		}
		if e.log.count(EventPasswordEdited) != 1 {
			t.Errorf("events = %v", e.log.names())
		}
	})
	tests := []struct {
		name  string
		token func(tok string) string
		pass  string
		prep  func(e *env)
		field string
		code  string
	}{
		// source: spec/services/password_resets/reset_service_spec.rb:64
		{name: "missing password", field: "new_password", code: CodeMissingPassword},
		// source: spec/services/password_resets/reset_service_spec.rb:52
		{name: "missing token", token: func(string) string { return "" }, pass: "new", field: "token", code: CodeMissingToken},
		{name: "unknown token", token: func(string) string { return "nope" }, pass: "new", code: "password_reset_not_found"},
		// source: spec/services/password_resets/reset_service_spec.rb:77
		{name: "expired token", pass: "new", prep: func(e *env) { e.clock.Advance(access.PasswordResetValidity) }, code: "password_reset_not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			u := e.user("gavin@hooli.com", "old")
			e.member(u, e.org(), permission.BuiltInRoleAdmin)
			tok := e.resetToken(u.Email)
			if tt.token != nil {
				tok = tt.token(tok)
			}
			if tt.prep != nil {
				tt.prep(e)
			}
			_, err := e.resets.Reset(e.ctx, tok, tt.pass, SessionMeta{})
			if code(err) != tt.code || fieldOf(err) != tt.field {
				t.Errorf("got = %s on %q, want = %s on %q", code(err), fieldOf(err), tt.code, tt.field)
			}
		})
	}
}

func TestPasswordResetsRequestWithoutMemberships(t *testing.T) {
	e := newEnv(t)
	e.revoke(e.member(e.user("gone@hooli.com", "pw"), e.org(), permission.BuiltInRoleAdmin))
	if _, err := e.resets.Request(e.ctx, "gone@hooli.com"); code(err) != "user_not_found" {
		t.Errorf("got = %s, want = user_not_found", code(err))
	}
}
