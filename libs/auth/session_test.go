package auth

import (
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
)

func TestSessions(t *testing.T) {
	cfg := SessionConfig{Lifetime: 48 * time.Hour, IdleTimeout: 2 * time.Hour, TouchInterval: time.Minute}
	user := core.NewID[core.UserID](core.UUIDv7Generator{})
	tests := []struct {
		name string
		step func(t *testing.T, s *Sessions, c *testClock, token string)
		ok   bool
	}{
		{name: "fresh session", ok: true},
		{name: "active within the idle timeout", step: func(t *testing.T, s *Sessions, c *testClock, token string) {
			for range 30 {
				c.Advance(time.Hour)
				if _, err := s.Authenticate(t.Context(), token); err != nil {
					t.Fatal(err)
				}
			}
		}, ok: true},
		{name: "idle timeout", step: func(_ *testing.T, _ *Sessions, c *testClock, _ string) { c.Advance(2 * time.Hour) }},
		{name: "absolute lifetime", step: func(t *testing.T, s *Sessions, c *testClock, token string) {
			for range 48 {
				c.Advance(time.Hour)
				_, _ = s.Authenticate(t.Context(), token)
			}
		}},
		{name: "revoked", step: func(t *testing.T, s *Sessions, _ *testClock, token string) {
			if err := s.Revoke(t.Context(), token); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "all user sessions revoked", step: func(t *testing.T, s *Sessions, _ *testClock, _ string) {
			if err := s.RevokeUser(t.Context(), user, SessionID{}); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &testClock{t: time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)}
			s := NewSessions(NewMemorySessionStore(), c, core.UUIDv7Generator{}, rand.Reader, cfg)
			token, sess, err := s.Issue(t.Context(), user, organization.AuthenticationMethodEmailPassword, SessionMeta{UserAgent: "ua", IP: "127.0.0.1"})
			if err != nil {
				t.Fatal(err)
			}
			if string(sess.TokenHash) == token || len(sess.TokenHash) != 32 {
				t.Fatal("token stored in clear")
			}
			if tt.step != nil {
				tt.step(t, s, c, token)
			}
			got, err := s.Authenticate(t.Context(), token)
			if (err == nil) != tt.ok {
				t.Fatalf("got = %v, want ok = %v", err, tt.ok)
			}
			if err != nil && !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("got = %v, want = ErrUnauthenticated", err)
			}
			if err == nil && (got.UserID != user || got.Method != organization.AuthenticationMethodEmailPassword || got.UserAgent != "ua") {
				t.Errorf("session = %+v", got)
			}
		})
	}
	t.Run("touch is throttled", func(t *testing.T) {
		c := &testClock{t: time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)}
		store := NewMemorySessionStore()
		s := NewSessions(store, c, core.UUIDv7Generator{}, rand.Reader, cfg)
		token, sess, _ := s.Issue(t.Context(), user, organization.AuthenticationMethodOkta, SessionMeta{})
		c.Advance(30 * time.Second)
		if _, err := s.Authenticate(t.Context(), token); err != nil {
			t.Fatal(err)
		}
		got, _ := store.GetByTokenHash(t.Context(), sess.TokenHash)
		if !got.LastSeenAt.Equal(sess.CreatedAt) {
			t.Errorf("last_seen_at = %s, want unchanged within the touch interval", got.LastSeenAt)
		}
		c.Advance(time.Minute)
		_, _ = s.Authenticate(t.Context(), token)
		got, _ = store.GetByTokenHash(t.Context(), sess.TokenHash)
		if !got.LastSeenAt.Equal(c.Now()) {
			t.Errorf("last_seen_at = %s, want = %s", got.LastSeenAt, c.Now())
		}
	})
	t.Run("revoke user keeps one session", func(t *testing.T) {
		c := &testClock{t: time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)}
		s := NewSessions(NewMemorySessionStore(), c, core.UUIDv7Generator{}, rand.Reader, cfg)
		t1, _, _ := s.Issue(t.Context(), user, organization.AuthenticationMethodOkta, SessionMeta{})
		t2, keep, _ := s.Issue(t.Context(), user, organization.AuthenticationMethodOkta, SessionMeta{})
		if err := s.RevokeUser(t.Context(), user, keep.ID); err != nil {
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
		s := NewSessions(NewMemorySessionStore(), &testClock{}, core.UUIDv7Generator{}, rand.Reader, cfg)
		for _, tok := range []string{"", "nope"} {
			if _, err := s.Authenticate(t.Context(), tok); !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("Authenticate(%q) = %v, want = ErrUnauthenticated", tok, err)
			}
		}
	})
	t.Run("delete expired", func(t *testing.T) {
		c := &testClock{t: time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)}
		store := NewMemorySessionStore()
		s := NewSessions(store, c, core.UUIDv7Generator{}, rand.Reader, cfg)
		_, _, _ = s.Issue(t.Context(), user, organization.AuthenticationMethodOkta, SessionMeta{})
		c.Advance(49 * time.Hour)
		_, _, _ = s.Issue(t.Context(), user, organization.AuthenticationMethodOkta, SessionMeta{})
		n, err := store.DeleteExpired(t.Context(), c.Now())
		if err != nil || n != 1 {
			t.Errorf("deleted = %d, %v, want = 1", n, err)
		}
	})
}
