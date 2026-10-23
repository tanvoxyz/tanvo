package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
)

// sessionEnv returns an env whose sessions follow cfg, and a stored user.
func sessionEnv(t *testing.T, cfg SessionConfig) (*env, *Sessions, *access.User) {
	t.Helper()
	e := newEnv(t)
	return e, NewSessions(e.deps, cfg), e.user("session@example.com", "")
}

func TestSessions(t *testing.T) {
	cfg := SessionConfig{Lifetime: 48 * time.Hour, IdleTimeout: 2 * time.Hour, TouchInterval: time.Minute}
	tests := []struct {
		name string
		step func(t *testing.T, e *env, s *Sessions, user *access.User, token string)
		ok   bool
	}{
		{name: "fresh session", ok: true},
		{name: "active within the idle timeout", step: func(t *testing.T, e *env, s *Sessions, _ *access.User, token string) {
			for range 30 {
				e.clock.Advance(time.Hour)
				if _, err := s.Authenticate(t.Context(), token); err != nil {
					t.Fatal(err)
				}
			}
		}, ok: true},
		{name: "idle timeout", step: func(_ *testing.T, e *env, _ *Sessions, _ *access.User, _ string) { e.clock.Advance(2 * time.Hour) }},
		{name: "absolute lifetime", step: func(t *testing.T, e *env, s *Sessions, _ *access.User, token string) {
			for range 48 {
				e.clock.Advance(time.Hour)
				_, _ = s.Authenticate(t.Context(), token)
			}
		}},
		{name: "revoked", step: func(t *testing.T, _ *env, s *Sessions, _ *access.User, token string) {
			if err := s.Revoke(t.Context(), token); err != nil {
				t.Fatal(err)
			}
			if err := s.Revoke(t.Context(), token); err != nil {
				t.Fatalf("Revoke() again error got = %v, want = nil", err)
			}
		}},
		{name: "all user sessions revoked", step: func(t *testing.T, _ *env, s *Sessions, user *access.User, _ string) {
			if err := s.RevokeUser(t.Context(), user.ID, SessionID{}); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, s, user := sessionEnv(t, cfg)
			token, sess, err := s.Issue(t.Context(), user.ID, organization.AuthenticationMethodEmailPassword, SessionMeta{UserAgent: "ua", IP: "127.0.0.1"})
			if err != nil {
				t.Fatal(err)
			}
			if string(sess.TokenHash) == token || len(sess.TokenHash) != 32 {
				t.Fatal("token stored in clear")
			}
			if tt.step != nil {
				tt.step(t, e, s, user, token)
			}
			got, err := s.Authenticate(t.Context(), token)
			if (err == nil) != tt.ok {
				t.Fatalf("got = %v, want ok = %v", err, tt.ok)
			}
			if err != nil && !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("got = %v, want = ErrUnauthenticated", err)
			}
			if err == nil && (got.UserID != user.ID || got.Method != organization.AuthenticationMethodEmailPassword || got.UserAgent != "ua") {
				t.Errorf("session = %+v", got)
			}
		})
	}
	t.Run("unknown user", func(t *testing.T) {
		_, s, _ := sessionEnv(t, cfg)
		other := newEnv(t).user("other@example.com", "")
		if _, _, err := s.Issue(t.Context(), other.ID, organization.AuthenticationMethodOkta, SessionMeta{}); err == nil {
			t.Error("Issue() of an unknown user error got = nil, want = not found")
		}
	})
	t.Run("touch is throttled", func(t *testing.T) {
		e, s, user := sessionEnv(t, cfg)
		token, sess, _ := s.Issue(t.Context(), user.ID, organization.AuthenticationMethodOkta, SessionMeta{})
		e.clock.Advance(30 * time.Second)
		if _, err := s.Authenticate(t.Context(), token); err != nil {
			t.Fatal(err)
		}
		got := e.session(sess.TokenHash)
		if !got.LastSeenAt.Equal(sess.CreatedAt) {
			t.Errorf("last_seen_at = %s, want unchanged within the touch interval", got.LastSeenAt)
		}
		e.clock.Advance(time.Minute)
		_, _ = s.Authenticate(t.Context(), token)
		got = e.session(sess.TokenHash)
		if !got.LastSeenAt.Equal(e.clock.Now()) {
			t.Errorf("last_seen_at = %s, want = %s", got.LastSeenAt, e.clock.Now())
		}
	})
	t.Run("revoke user keeps one session", func(t *testing.T) {
		_, s, user := sessionEnv(t, cfg)
		t1, _, _ := s.Issue(t.Context(), user.ID, organization.AuthenticationMethodOkta, SessionMeta{})
		t2, keep, _ := s.Issue(t.Context(), user.ID, organization.AuthenticationMethodOkta, SessionMeta{})
		if err := s.RevokeUser(t.Context(), user.ID, keep.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Authenticate(t.Context(), t1); err == nil {
			t.Error("revoked session still authenticates")
		}
		if _, err := s.Authenticate(t.Context(), t2); err != nil {
			t.Errorf("kept session: %v", err)
		}
	})
	t.Run("unknown and empty tokens", func(t *testing.T) {
		_, s, _ := sessionEnv(t, cfg)
		for _, tok := range []string{"", "nope"} {
			if _, err := s.Authenticate(t.Context(), tok); !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("Authenticate(%q) = %v, want = ErrUnauthenticated", tok, err)
			}
			if err := s.Revoke(t.Context(), tok); err != nil {
				t.Errorf("Revoke(%q) = %v, want = nil", tok, err)
			}
		}
	})
	t.Run("purge", func(t *testing.T) {
		e, s, user := sessionEnv(t, cfg)
		_, _, _ = s.Issue(t.Context(), user.ID, organization.AuthenticationMethodOkta, SessionMeta{})
		e.clock.Advance(49 * time.Hour)
		_, _, _ = s.Issue(t.Context(), user.ID, organization.AuthenticationMethodOkta, SessionMeta{})
		sessions, states, err := s.Purge(t.Context(), 0)
		if err != nil || sessions != 1 || states != 0 {
			t.Errorf("Purge() got = %d, %d, %v, want = 1, 0, nil", sessions, states, err)
		}
	})
}
