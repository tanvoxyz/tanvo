package pgtest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Template describes the template database the test databases are cloned
// from. Templates are built once per process and keyed by Name.
type Template struct {
	// Name identifies the template; templates with the same name are
	// assumed identical.
	Name string
	// Setup prepares the template database, connected as the superuser
	// (applying migrations, loading fixtures). A nil Setup leaves it empty.
	Setup func(ctx context.Context, conn *pgx.Conn) error
}

// Database is one test's database.
type Database struct {
	// Name is the database name.
	Name string
	// AdminDSN connects as the superuser, which owns every object.
	AdminDSN string
	// AppDSN connects as [AppRole], subject to row-level security.
	AppDSN string
}

// New creates a database for t cloned from tmpl and drops it when t ends.
// It skips t when no server is available or in -short mode.
func New(t testing.TB, tmpl Template) *Database {
	t.Helper()
	s := getServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	tname, err := s.template(ctx, tmpl)
	if err != nil {
		t.Fatalf("pgtest: template %q: %v", tmpl.Name, err)
	}
	name := randomName("test_")
	conn, err := s.admin(ctx, "")
	if err != nil {
		t.Fatalf("pgtest: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, tname)); err != nil {
		t.Fatalf("pgtest: create database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		conn, err := s.admin(ctx, "")
		if err != nil {
			t.Errorf("pgtest: drop database %s: %v", name, err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		if _, err := conn.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name)); err != nil {
			t.Errorf("pgtest: drop database %s: %v", name, err)
		}
	})
	admin, err := withDatabase(s.adminDSN, name, "", "")
	if err != nil {
		t.Fatalf("pgtest: %v", err)
	}
	app, err := withDatabase(s.adminDSN, name, AppRole, AppRole)
	if err != nil {
		t.Fatalf("pgtest: %v", err)
	}
	return &Database{Name: name, AdminDSN: admin, AppDSN: app}
}

// Pool opens a pool on dsn (AdminDSN or AppDSN) closed when t ends.
func Pool(t testing.TB, dsn string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgtest: parse dsn: %v", err)
	}
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatalf("pgtest: open pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// template returns the name of the template database of tmpl, building it
// at the first call.
func (s *server) template(ctx context.Context, tmpl Template) (string, error) {
	s.mu.Lock()
	st, ok := s.templates[tmpl.Name]
	if !ok {
		st = &templateState{}
		s.templates[tmpl.Name] = st
	}
	s.mu.Unlock()
	st.once.Do(func() { st.name, st.err = s.buildTemplate(ctx, tmpl) })
	return st.name, st.err
}

func (s *server) buildTemplate(ctx context.Context, tmpl Template) (string, error) {
	name := randomName("tmpl_")
	conn, err := s.admin(ctx, "")
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		return "", fmt.Errorf("create template: %w", err)
	}
	tconn, err := s.admin(ctx, name)
	if err != nil {
		return "", err
	}
	defer func() { _ = tconn.Close(ctx) }()
	if tmpl.Setup != nil {
		if err := tmpl.Setup(ctx, tconn); err != nil {
			return "", fmt.Errorf("setup: %w", err)
		}
	}
	grants := fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %[1]s;
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %[1]s;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %[1]s;`, AppRole)
	if _, err := tconn.Exec(ctx, grants); err != nil {
		return "", fmt.Errorf("grant %s: %w", AppRole, err)
	}
	if err := tconn.Close(ctx); err != nil {
		return "", fmt.Errorf("close template connection: %w", err)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf("ALTER DATABASE %s WITH IS_TEMPLATE true", name)); err != nil {
		return "", fmt.Errorf("mark template: %w", err)
	}
	return name, nil
}
