package auth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/memstore"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// testClock is a settable clock.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// recorder is a SecurityLogger keeping events.
type recorder struct {
	mu     sync.Mutex
	events []SecurityEvent
}

func (r *recorder) Record(_ context.Context, e SecurityEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	for i, e := range r.events {
		out[i] = e.Event
	}
	return out
}

func (r *recorder) count(event string) int {
	n := 0
	for _, x := range r.names() {
		if x == event {
			n++
		}
	}
	return n
}

// env wires every service over an in-memory store.
type env struct {
	t         *testing.T
	ctx       context.Context
	store     *memstore.Store
	clock     *testClock
	log       *recorder
	deps      Deps
	sessions  *Sessions
	sessStore *MemorySessionStore
	hasher    *PasswordHasher
	users     *Users
	invites   *Invites
	resets    *PasswordResets
	keys      *APIKeys
	usage     *UsageTracker
	authz     *Authorizer
}

func testHasher() *PasswordHasher {
	return NewPasswordHasher(Argon2Params{Memory: 64, Time: 1, Threads: 1, SaltLength: 16, KeyLength: 32}, rand.Reader)
}

func newEnv(t *testing.T, cfg ...UsersConfig) *env {
	t.Helper()
	e := &env{t: t, ctx: t.Context(), store: memstore.New(), clock: &testClock{t: time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)}, log: &recorder{}}
	e.deps = Deps{Tx: e.store, Clock: e.clock, IDs: core.UUIDv7Generator{}, Rand: rand.Reader, SecurityLog: e.log, Logger: slog.New(slog.DiscardHandler)}
	e.sessStore = NewMemorySessionStore()
	e.sessions = NewSessions(e.sessStore, e.clock, e.deps.IDs, rand.Reader, DefaultSessionConfig())
	e.hasher = testHasher()
	var uc UsersConfig
	if len(cfg) > 0 {
		uc = cfg[0]
	}
	e.users = NewUsers(e.deps, e.sessions, e.hasher, uc)
	e.invites = NewInvites(e.deps, e.users, InvitesConfig{FrontURL: "https://app.tanvo.test/"})
	e.resets = NewPasswordResets(e.deps, e.users, e.sessions, "https://app.tanvo.test")
	e.usage = NewUsageTracker()
	e.keys = NewAPIKeys(e.deps, e.usage)
	e.authz = NewAuthorizer(e.store)
	return e
}

func (e *env) tx(fn func(ctx context.Context, r port.Repos) error) {
	e.t.Helper()
	if err := e.store.InTx(e.ctx, fn); err != nil {
		e.t.Fatalf("tx: %v", err)
	}
}

// org creates an organization allowing methods (all when none).
func (e *env) org(methods ...organization.AuthenticationMethod) *organization.Organization {
	e.t.Helper()
	id := core.NewID[core.OrganizationID](e.deps.IDs)
	s := id.String()
	o := organization.New(id, "Org "+s[len(s)-12:], e.clock.Now())
	if len(methods) > 0 {
		o.AuthenticationMethods = methods
	}
	e.tx(func(ctx context.Context, r port.Repos) error { return r.Organizations.Create(ctx, o) })
	return o
}

// user creates a user; an empty password makes an SSO-only user.
func (e *env) user(email, password string) *access.User {
	e.t.Helper()
	var hash []byte
	if password != "" {
		var err error
		if hash, err = e.hasher.Hash(password); err != nil {
			e.t.Fatal(err)
		}
	}
	u := access.NewUser(core.NewID[core.UserID](e.deps.IDs), email, hash, e.clock.Now())
	e.tx(func(ctx context.Context, r port.Repos) error { return r.Users.Create(ctx, u) })
	return u
}

func (e *env) member(u *access.User, o *organization.Organization, role permission.BuiltInRole) *access.Membership {
	e.t.Helper()
	m := access.NewMembership(core.NewID[core.MembershipID](e.deps.IDs), o.ID, u.ID, access.BuiltInRoleID(role), e.clock.Now())
	e.tx(func(ctx context.Context, r port.Repos) error { return r.Memberships.Create(ctx, m) })
	return m
}

func (e *env) revoke(m *access.Membership) {
	e.t.Helper()
	e.tx(func(ctx context.Context, r port.Repos) error {
		if err := m.Revoke(e.clock.Now()); err != nil {
			return err
		}
		return r.Memberships.Update(ctx, m)
	})
}

// countMemberships returns the memberships (any status) of org.
func (e *env) countMemberships(o *organization.Organization) int {
	n := 0
	e.tx(func(ctx context.Context, r port.Repos) error {
		p, err := r.Memberships.List(ctx, o.ID, access.MembershipFilter{}, core.PageRequest{Size: 1000})
		n = len(p.Items)
		return err
	})
	return n
}

func (e *env) getUser(email string) (*access.User, bool) {
	var u *access.User
	err := e.store.InTx(e.ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		u, err = r.Users.GetByEmail(ctx, access.NormalizeEmail(email))
		return err
	})
	return u, err == nil
}

// mailJobs decodes the enqueued mail jobs and clears them.
func (e *env) mailJobs() []MailJob {
	var out []MailJob
	for _, j := range e.store.TakeJobs() {
		if j.Kind != JobKindMail {
			continue
		}
		var m MailJob
		if err := json.Unmarshal(j.Args, &m); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// code returns ErrorCode(err), or "" for nil.
func code(err error) string {
	if err == nil {
		return ""
	}
	if c := ErrorCode(err); c != "" {
		return c
	}
	return err.Error()
}

func fieldOf(err error) string {
	if fe := core.FieldErrors(err); len(fe) > 0 {
		return fe[0].Field
	}
	return ""
}

func ptr[T any](v T) *T { return &v }
