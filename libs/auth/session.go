package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// SessionID identifies a session.
type SessionID = core.SessionID

// Session is a signed-in user's opaque session (domain §5.7), stored in
// the access repositories ([port.Repos.Sessions]). The organization is
// chosen per request and checked by [Authorizer.UserPrincipal].
type Session = access.Session

// SessionConfig bounds session validity.
type SessionConfig struct {
	// Lifetime is the absolute validity from sign-in.
	Lifetime time.Duration
	// IdleTimeout ends a session unused for that long; 0 disables it.
	IdleTimeout time.Duration
	// TouchInterval is the minimum delay between two last_seen_at writes.
	TouchInterval time.Duration
}

// DefaultSessionConfig returns 14 days of lifetime, a 24-hour idle timeout
// and a one-minute touch interval.
func DefaultSessionConfig() SessionConfig {
	return SessionConfig{Lifetime: 14 * 24 * time.Hour, IdleTimeout: 24 * time.Hour, TouchInterval: time.Minute}
}

// SessionMeta describes the client opening a session. apps/server fills it
// from the request, parsing the user agent into Browser, OS and
// DeviceType (lago Utils::DeviceInfo).
type SessionMeta struct {
	// UserAgent is the client's user agent; empty skips device tracking.
	UserAgent string
	// IP is the client's address.
	IP string
	// Browser is the browser name and version, if known.
	Browser string
	// OS is the operating system, if known.
	OS string
	// DeviceType is the kind of device (desktop, smartphone, …), if known.
	DeviceType string
}

// Sessions issues and authenticates sessions.
type Sessions struct {
	d   Deps
	cfg SessionConfig
}

// NewSessions returns the session service over d's repositories.
func NewSessions(d Deps, cfg SessionConfig) *Sessions {
	return &Sessions{d: d, cfg: cfg}
}

// Issue opens a session of user signed in with method and returns its
// token, which is never stored.
func (s *Sessions) Issue(ctx context.Context, user core.UserID, method organization.AuthenticationMethod, meta SessionMeta) (string, *Session, error) {
	token, err := NewSecret(s.d.Rand, secretBytes)
	if err != nil {
		return "", nil, fmt.Errorf("issue session: %w", err)
	}
	now := s.d.now()
	sess := &Session{
		ID: core.NewID[SessionID](s.d.IDs), UserID: user, TokenHash: HashSecret(token), Method: method,
		UserAgent: meta.UserAgent, IP: meta.IP, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(s.cfg.Lifetime),
	}
	if err := sess.Validate(); err != nil {
		return "", nil, fmt.Errorf("issue session: %w", err)
	}
	if err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error { return r.Sessions.Create(ctx, sess) }); err != nil {
		return "", nil, fmt.Errorf("issue session: %w", err)
	}
	return token, sess, nil
}

// Authenticate returns the session of token, refreshing its last use; an
// unknown, expired, idle or revoked session fails with
// [ErrUnauthenticated].
func (s *Sessions) Authenticate(ctx context.Context, token string) (*Session, error) {
	if token == "" {
		return nil, ErrUnauthenticated
	}
	var sess *Session
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		sess, err = r.Sessions.GetByTokenHash(ctx, HashSecret(token))
		if errors.Is(err, core.ErrNotFound) {
			return ErrUnauthenticated
		}
		if err != nil {
			return fmt.Errorf("authenticate session: %w", err)
		}
		now := s.d.now()
		if !sess.Valid(now, s.cfg.IdleTimeout) {
			return ErrUnauthenticated
		}
		if now.Sub(sess.LastSeenAt) >= s.cfg.TouchInterval {
			if err := r.Sessions.Touch(ctx, sess.ID, now); err != nil {
				return fmt.Errorf("touch session: %w", err)
			}
			sess.LastSeenAt = now
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// Revoke ends the session of token (sign-out); an unknown or already
// revoked token succeeds.
func (s *Sessions) Revoke(ctx context.Context, token string) error {
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		sess, err := r.Sessions.GetByTokenHash(ctx, HashSecret(token))
		if errors.Is(err, core.ErrNotFound) || (err == nil && sess.RevokedAt != nil) {
			return nil
		}
		if err != nil {
			return err
		}
		return r.Sessions.Revoke(ctx, sess.ID, s.d.now())
	})
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// RevokeUser ends every session of user except keep (zero ends all).
func (s *Sessions) RevokeUser(ctx context.Context, user core.UserID, keep SessionID) error {
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		_, err := r.Sessions.RevokeByUser(ctx, user, keep, s.d.now())
		return err
	})
	if err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	return nil
}

// Purge deletes the sessions that expired or were revoked before now
// minus retain, and the expired sign-in states; it returns how many
// sessions and states it removed. apps/server runs it periodically. Lago
// has no purge because its JWT sessions are not stored; Tanvo's opaque
// sessions are, so they must be removed.
func (s *Sessions) Purge(ctx context.Context, retain time.Duration) (sessions, states int, err error) {
	now := s.d.now()
	err = s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if sessions, err = r.Sessions.DeleteExpired(ctx, now.Add(-retain)); err != nil {
			return fmt.Errorf("delete expired sessions: %w", err)
		}
		if states, err = r.OIDCStates.DeleteExpired(ctx, now); err != nil {
			return fmt.Errorf("delete expired oidc states: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("purge sessions: %w", err)
	}
	return sessions, states, nil
}
