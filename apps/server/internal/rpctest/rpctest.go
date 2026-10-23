package rpctest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanvoxyz/tanvo/apps/server/internal/app"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store"
	"github.com/tanvoxyz/tanvo/libs/store/pgtest"
	"github.com/tanvoxyz/tanvo/libs/webhooks"
)

// SchemaEnv set to "source" loads libs/store/schema into the template
// instead of applying the migrations.
const SchemaEnv = "TANVO_STORE_TEST_SCHEMA"

// Now is the initial instant of the server's clock.
var Now = time.Date(2025, 3, 15, 12, 0, 0, 0, time.UTC)

// Main runs the tests of a package and then stops the PostgreSQL server
// started for them.
func Main(m *testing.M) int { return pgtest.Main(m) }

var template = pgtest.Template{
	Name: "rpctest-" + os.Getenv(SchemaEnv),
	Setup: func(ctx context.Context, conn *pgx.Conn) error {
		if os.Getenv(SchemaEnv) == "source" {
			return loadSchema(ctx, conn)
		}
		s, err := store.Open(ctx, conn.Config().ConnString())
		if err != nil {
			return err
		}
		defer s.Pool().Close()
		return store.Migrate(ctx, s.Pool())
	},
}

// loadSchema runs the SQL files of libs/store/schema in order.
func loadSchema(ctx context.Context, conn *pgx.Conn) error {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return fmt.Errorf("rpctest: locate libs/store/schema")
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(file), "../../../../libs/store/schema/*.sql"))
	if err != nil {
		return fmt.Errorf("rpctest: schema files: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("rpctest: no schema files")
	}
	slices.Sort(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, string(b)); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
	}
	return nil
}

// Server is the API of one test.
type Server struct {
	// URL is the server's base URL.
	URL string
	// Store is the test's store, with row-level security on.
	Store *store.Store
	// Admin is a pool on the test's database as its owner, outside
	// row-level security, for direct checks.
	Admin *pgxpool.Pool
	// Clock is the clock of the services, at [Now] initially.
	Clock *billingtest.Clock
	// Billing is the billing engine of the API, over the Fixtures'
	// dependencies (its events are Fixtures.Events).
	Billing *billing.Engine
	// Fixtures creates domain objects in Org directly through the
	// repositories (billingtest: Metric, Plan, Charge, Customer, …).
	Fixtures *billingtest.Harness
	// Org is the seeded organization, with its default billing entity.
	Org *organization.Organization
	// APIKey is a full-access API key of Org.
	APIKey Credential
	// User is the admin member of Org.
	User *access.User
	// Session is User's session acting in Org.
	Session Credential
}

// Credential is what a request authenticates with: a bearer token and,
// for a user session, the organization it acts in.
type Credential struct {
	// Token is the API key or session token.
	Token string
	// Organization is sent as the organization header; empty for an API
	// key.
	Organization string
}

// Anonymous is no credential.
var Anonymous = Credential{}

// SecretKey is the installation secret of the test servers.
var SecretKey = bytes.Repeat([]byte{0x5a}, 32)

// Portal returns a customer portal token of customer in Org.
func (s *Server) Portal(t testing.TB, customer core.CustomerID) Credential {
	t.Helper()
	p, err := app.NewPortalTokens(SecretKey, "https://app.tanvo.test", s.Clock)
	if err != nil {
		t.Fatalf("rpctest: portal tokens: %v", err)
	}
	tok, _, err := p.Issue(s.Org.ID, customer)
	if err != nil {
		t.Fatalf("rpctest: portal token: %v", err)
	}
	return Credential{Token: tok}
}

// Option changes the server of [New].
type Option func(*app.APIOptions)

// New starts the API for t on a fresh database. It skips t when no
// PostgreSQL server is available.
func New(t testing.TB, opts ...Option) *Server {
	t.Helper()
	db := pgtest.New(t, template)
	st, err := store.Open(t.Context(), db.AppDSN, store.WithRowLevelSecurity())
	if err != nil {
		t.Fatalf("rpctest: open store: %v", err)
	}
	t.Cleanup(st.Pool().Close)

	h := billingtest.NewOn(t, Now, st, &webhooks.Emitter{Tx: st})
	engine, err := billing.New(*h.Deps)
	if err != nil {
		t.Fatalf("rpctest: billing engine: %v", err)
	}
	s := &Server{Store: st, Admin: pgtest.Pool(t, db.AdminDSN), Clock: h.Clock, Fixtures: h, Org: h.Org, Billing: engine}
	o := app.APIOptions{
		Tx: st, Billing: engine, MaxBytes: 8 << 20, Log: slog.New(slog.DiscardHandler), Clock: h.Clock,
		AppURL: "https://app.tanvo.test", SecretKey: SecretKey,
		// Cheap hashing: the tests do not measure password costs.
		PasswordParams: auth.Argon2Params{Memory: 64, Time: 1, Threads: 1, SaltLength: 16, KeyLength: 32},
	}
	for _, opt := range opts {
		opt(&o)
	}
	handler, err := app.NewAPIHandler(o)
	if err != nil {
		t.Fatalf("rpctest: api: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	s.seed(t)
	return s
}

// seed creates the admin user, its session and the API key of Org.
func (s *Server) seed(t testing.TB) {
	t.Helper()
	ctx := t.Context()
	d := auth.Deps{Tx: s.Store, Clock: s.Clock, IDs: core.UUIDv7Generator{}, Rand: rand.Reader}
	key, err := auth.NewAPIKeys(d, nil).Create(ctx, s.Org.ID, nil, nil)
	if err != nil {
		t.Fatalf("rpctest: create api key: %v", err)
	}
	s.APIKey = Credential{Token: key.Secret}
	now := s.Clock.Now()
	u := access.NewUser(core.NewID[core.UserID](d.IDs), "admin@tanvo.test", nil, now)
	err = s.Store.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := r.Users.Create(ctx, u); err != nil {
			return err
		}
		return r.Memberships.Create(ctx, access.NewMembership(core.NewID[core.MembershipID](d.IDs), s.Org.ID, u.ID,
			[]core.RoleID{access.BuiltInRoleID(permission.BuiltInRoleAdmin)}, now))
	})
	if err != nil {
		t.Fatalf("rpctest: create admin: %v", err)
	}
	s.User = u
	token, _, err := auth.NewSessions(d, auth.DefaultSessionConfig()).Issue(ctx, u.ID, organization.AuthenticationMethodEmailPassword, auth.SessionMeta{})
	if err != nil {
		t.Fatalf("rpctest: issue session: %v", err)
	}
	s.Session = Credential{Token: token, Organization: s.Org.ID.String()}
}

// HTTPClient returns a client sending c with every request.
func (s *Server) HTTPClient(c Credential) *http.Client {
	return &http.Client{Transport: credentialTransport{c: c, next: http.DefaultTransport}}
}

// Connect returns a Connect client of the server authenticated with c,
// for the generated service clients
// (catalogv1connect.NewBillableMetricServiceClient(s.Connect(s.APIKey))).
func (s *Server) Connect(c Credential, interceptors ...connect.ClientInterceptor) *connect.Client {
	return connect.NewClient(connecthttp.NewTransport(s.HTTPClient(c), s.URL), interceptors...)
}

// REST sends a JSON request to the REST route path ("/v1/billable_metrics")
// with body encoded as JSON (nil for none; a string or []byte is sent as
// it is) and returns the status and the response body.
func (s *Server) REST(t testing.TB, c Credential, method, path string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		r = bytes.NewReader([]byte(b))
	case []byte:
		r = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("rpctest: encode body: %v", err)
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, s.URL+path, r)
	if err != nil {
		t.Fatalf("rpctest: request: %v", err)
	}
	if r != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.HTTPClient(c).Do(req)
	if err != nil {
		t.Fatalf("rpctest: %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("rpctest: read response: %v", err)
	}
	return resp.StatusCode, out
}

// Webhooks returns the types of the domain events (webhooks) recorded for
// Org, in order.
func (s *Server) Webhooks(t testing.TB) []string {
	t.Helper()
	var out []string
	rows, err := s.Admin.Query(t.Context(), "SELECT event_type FROM domain_events WHERE organization_id = $1 ORDER BY occurred_at, id", s.Org.ID.UUID())
	if err != nil {
		t.Fatalf("rpctest: webhooks: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatalf("rpctest: webhooks: %v", err)
		}
		out = append(out, typ)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rpctest: webhooks: %v", err)
	}
	return out
}

type credentialTransport struct {
	c    Credential
	next http.RoundTripper
}

func (c credentialTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if c.c.Token != "" {
		r.Header.Set("Authorization", "Bearer "+c.c.Token)
	}
	if c.c.Organization != "" {
		r.Header.Set(rpc.OrganizationHeader, c.c.Organization)
	}
	return c.next.RoundTrip(r)
}
