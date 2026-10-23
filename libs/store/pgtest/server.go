package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// DSNEnv names the environment variable holding the DSN of an existing
// server to use instead of a container; the user must be a superuser (it
// creates roles and databases).
const DSNEnv = "TANVO_TEST_POSTGRES_DSN"

// Image is the container image started when [DSNEnv] is unset.
const Image = "postgres:18"

// AppRole is the non-superuser login role created on every server; it is
// subject to row-level security. Its password is the role name.
const AppRole = "tanvo_app"

// server is the PostgreSQL server shared by the tests of a process.
type server struct {
	adminDSN  string
	container *container

	mu        sync.Mutex
	templates map[string]*templateState
}

type templateState struct {
	once sync.Once
	name string
	err  error
}

// shared holds the process-wide server. A test binary runs the tests of one
// package, so this is "one server per package"; it is the only state of
// this package, guarded by its sync.Once.
var shared struct {
	once sync.Once
	srv  *server
	err  error
}

// Main runs the tests of a package and then removes the container started
// for them, if any. Call it from TestMain: os.Exit(pgtest.Main(m)).
func Main(m *testing.M) int {
	code := m.Run()
	if shared.srv != nil && shared.srv.container != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := shared.srv.container.remove(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "pgtest: removing container: %v\n", err)
		}
	}
	return code
}

// getServer returns the shared server, starting it at the first call.
func getServer(t testing.TB) *server {
	t.Helper()
	if testing.Short() {
		t.Skip("pgtest: skipped in -short mode")
	}
	shared.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		shared.srv, shared.err = startServer(ctx)
	})
	if shared.err != nil {
		if errors.Is(shared.err, errNoDocker) {
			t.Skipf("pgtest: no PostgreSQL available: %v (set %s or start a Docker engine)", shared.err, DSNEnv)
		}
		t.Fatalf("pgtest: %v", shared.err)
	}
	return shared.srv
}

func startServer(ctx context.Context) (*server, error) {
	s := &server{templates: map[string]*templateState{}}
	if dsn := os.Getenv(DSNEnv); dsn != "" {
		s.adminDSN = dsn
	} else {
		c, err := startContainer(ctx)
		if err != nil {
			return nil, err
		}
		s.container = c
		s.adminDSN = c.dsn
	}
	if err := s.waitReady(ctx); err != nil {
		if s.container != nil {
			_ = s.container.remove(context.WithoutCancel(ctx))
		}
		return nil, err
	}
	if err := s.ensureAppRole(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *server) waitReady(ctx context.Context) error {
	var last error
	for {
		conn, err := pgx.Connect(ctx, s.adminDSN)
		if err == nil {
			err = conn.Ping(ctx)
			_ = conn.Close(ctx)
			if err == nil {
				return nil
			}
		}
		last = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for postgres: %w (last error: %w)", ctx.Err(), last)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (s *server) admin(ctx context.Context, db string) (*pgx.Conn, error) {
	dsn, err := withDatabase(s.adminDSN, db, "", "")
	if err != nil {
		return nil, err
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", db, err)
	}
	return conn, nil
}

func (s *server) ensureAppRole(ctx context.Context) error {
	conn, err := s.admin(ctx, "")
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, fmt.Sprintf(`DO $$ BEGIN
		IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%[1]s') THEN
			CREATE ROLE %[1]s LOGIN PASSWORD '%[1]s' NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
		END IF;
	END $$`, AppRole))
	if err != nil {
		return fmt.Errorf("create role %s: %w", AppRole, err)
	}
	return nil
}

// withDatabase returns dsn pointing at database db (unchanged when empty)
// and, when user is set, authenticating as user with password.
func withDatabase(dsn, db, user, password string) (string, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", fmt.Errorf("parse dsn: %w", err)
		}
		if db != "" {
			u.Path = "/" + db
		}
		if user != "" {
			u.User = url.UserPassword(user, password)
		}
		return u.String(), nil
	}
	// Keyword/value form: later keywords win.
	if db != "" {
		dsn += " dbname=" + db
	}
	if user != "" {
		dsn += " user=" + user + " password=" + password
	}
	return dsn, nil
}

func randomName(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}
