package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
)

type sessionKind struct{}

// SessionID identifies a session.
type SessionID = core.ID[sessionKind]

// Session is a signed-in user's opaque session (architecture §4.4). The
// token is shown once; only its SHA-256 is stored. Sessions belong to
// users, not organizations: the organization is chosen per request and
// checked by [Authorizer.UserPrincipal].
type Session struct {
	// ID identifies the session.
	ID SessionID
	// UserID is the signed-in user.
	UserID core.UserID
	// TokenHash is the SHA-256 of the token ([HashSecret]).
	TokenHash []byte
	// Method is the sign-in method; organizations that do not allow it are
	// not accessible with this session (lago login_method).
	Method organization.AuthenticationMethod
	// UserAgent is the client's user agent at sign-in.
	UserAgent string
	// IP is the client's address at sign-in.
	IP string
	// CreatedAt is the sign-in instant.
	CreatedAt time.Time
	// LastSeenAt is the instant of the last authenticated request, updated
	// at most every [SessionConfig.TouchInterval].
	LastSeenAt time.Time
	// ExpiresAt is the absolute end of validity.
	ExpiresAt time.Time
	// RevokedAt is set on sign-out or revocation.
	RevokedAt *time.Time
}

// Valid reports whether the session authenticates requests at now under
// the idle timeout idle (0 disables it).
func (s *Session) Valid(now time.Time, idle time.Duration) bool {
	if s.RevokedAt != nil || !now.Before(s.ExpiresAt) {
		return false
	}
	return idle <= 0 || now.Sub(s.LastSeenAt) < idle
}

// SessionStore stores sessions. libs/store implements it in PostgreSQL.
type SessionStore interface {
	// Create inserts s.
	Create(ctx context.Context, s *Session) error
	// GetByTokenHash returns the session with that token hash, or
	// core.ErrNotFound.
	GetByTokenHash(ctx context.Context, tokenHash []byte) (*Session, error)
	// Touch sets the session's last_seen_at to at.
	Touch(ctx context.Context, id SessionID, at time.Time) error
	// Revoke sets the session's revoked_at to at.
	Revoke(ctx context.Context, id SessionID, at time.Time) error
	// RevokeByUser revokes every unrevoked session of user except keep
	// (zero keeps none) and returns how many.
	RevokeByUser(ctx context.Context, user core.UserID, keep SessionID, at time.Time) (int, error)
	// DeleteExpired removes sessions that expired or were revoked before
	// before, and returns how many.
	DeleteExpired(ctx context.Context, before time.Time) (int, error)
}

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

// SessionMeta describes the client opening a session.
type SessionMeta struct {
	// UserAgent is the client's user agent.
	UserAgent string
	// IP is the client's address.
	IP string
}

// Sessions issues and authenticates sessions.
type Sessions struct {
	store SessionStore
	clock core.Clock
	ids   core.IDGenerator
	rand  io.Reader
	cfg   SessionConfig
}

// NewSessions returns the session service.
func NewSessions(store SessionStore, clock core.Clock, ids core.IDGenerator, rand io.Reader, cfg SessionConfig) *Sessions {
	return &Sessions{store: store, clock: clock, ids: ids, rand: rand, cfg: cfg}
}

// Issue opens a session of user signed in with method and returns its
// token, which is never stored.
func (s *Sessions) Issue(ctx context.Context, user core.UserID, method organization.AuthenticationMethod, meta SessionMeta) (string, *Session, error) {
	token, err := NewSecret(s.rand, secretBytes)
	if err != nil {
		return "", nil, fmt.Errorf("issue session: %w", err)
	}
	now := s.clock.Now().UTC()
	sess := &Session{
		ID: core.NewID[SessionID](s.ids), UserID: user, TokenHash: HashSecret(token), Method: method,
		UserAgent: meta.UserAgent, IP: meta.IP, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(s.cfg.Lifetime),
	}
	if err := s.store.Create(ctx, sess); err != nil {
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
	sess, err := s.store.GetByTokenHash(ctx, HashSecret(token))
	if errors.Is(err, core.ErrNotFound) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, fmt.Errorf("authenticate session: %w", err)
	}
	now := s.clock.Now().UTC()
	if !sess.Valid(now, s.cfg.IdleTimeout) {
		return nil, ErrUnauthenticated
	}
	if now.Sub(sess.LastSeenAt) >= s.cfg.TouchInterval {
		if err := s.store.Touch(ctx, sess.ID, now); err != nil {
			return nil, fmt.Errorf("touch session: %w", err)
		}
		sess.LastSeenAt = now
	}
	return sess, nil
}

// Revoke ends the session of token (sign-out); an unknown token succeeds.
func (s *Sessions) Revoke(ctx context.Context, token string) error {
	sess, err := s.store.GetByTokenHash(ctx, HashSecret(token))
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	if sess.RevokedAt != nil {
		return nil
	}
	if err := s.store.Revoke(ctx, sess.ID, s.clock.Now().UTC()); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// RevokeUser ends every session of user except keep (zero ends all).
func (s *Sessions) RevokeUser(ctx context.Context, user core.UserID, keep SessionID) error {
	if _, err := s.store.RevokeByUser(ctx, user, keep, s.clock.Now().UTC()); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	return nil
}

// MemorySessionStore is an in-memory [SessionStore] for tests and
// single-process development.
type MemorySessionStore struct {
	mu       sync.Mutex
	sessions []*Session
}

var _ SessionStore = (*MemorySessionStore)(nil)

// NewMemorySessionStore returns an empty store.
func NewMemorySessionStore() *MemorySessionStore { return &MemorySessionStore{} }

func cloneSession(s *Session) *Session {
	c := *s
	c.TokenHash = slices.Clone(s.TokenHash)
	if s.RevokedAt != nil {
		t := *s.RevokedAt
		c.RevokedAt = &t
	}
	return &c
}

// Create implements [SessionStore].
func (m *MemorySessionStore) Create(_ context.Context, s *Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.sessions {
		if bytes.Equal(x.TokenHash, s.TokenHash) {
			return fmt.Errorf("session token: %w", core.ErrConflict)
		}
	}
	m.sessions = append(m.sessions, cloneSession(s))
	return nil
}

// GetByTokenHash implements [SessionStore].
func (m *MemorySessionStore) GetByTokenHash(_ context.Context, h []byte) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.sessions {
		if bytes.Equal(x.TokenHash, h) {
			return cloneSession(x), nil
		}
	}
	return nil, fmt.Errorf("session: %w", core.ErrNotFound)
}

func (m *MemorySessionStore) find(id SessionID) (*Session, error) {
	for _, x := range m.sessions {
		if x.ID == id {
			return x, nil
		}
	}
	return nil, fmt.Errorf("session %s: %w", id, core.ErrNotFound)
}

// Touch implements [SessionStore].
func (m *MemorySessionStore) Touch(_ context.Context, id SessionID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	x, err := m.find(id)
	if err != nil {
		return err
	}
	x.LastSeenAt = at
	return nil
}

// Revoke implements [SessionStore].
func (m *MemorySessionStore) Revoke(_ context.Context, id SessionID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	x, err := m.find(id)
	if err != nil {
		return err
	}
	x.RevokedAt = &at
	return nil
}

// RevokeByUser implements [SessionStore].
func (m *MemorySessionStore) RevokeByUser(_ context.Context, user core.UserID, keep SessionID, at time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, x := range m.sessions {
		if x.UserID == user && x.ID != keep && x.RevokedAt == nil {
			x.RevokedAt = &at
			n++
		}
	}
	return n, nil
}

// DeleteExpired implements [SessionStore].
func (m *MemorySessionStore) DeleteExpired(_ context.Context, before time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.sessions)
	m.sessions = slices.DeleteFunc(m.sessions, func(x *Session) bool {
		return x.ExpiresAt.Before(before) || (x.RevokedAt != nil && x.RevokedAt.Before(before))
	})
	return n - len(m.sessions), nil
}
