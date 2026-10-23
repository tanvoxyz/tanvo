package store_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/tanvoxyz/tanvo/libs/core/memstore"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store"
	"github.com/tanvoxyz/tanvo/libs/store/pgtest"
	"github.com/tanvoxyz/tanvo/libs/store/porttest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// schemaEnv set to "source" makes the tests load schema/ instead of
// applying migrations/, while a change to schema/ has no migration yet.
const schemaEnv = "TANVO_STORE_TEST_SCHEMA"

// template is the database every test database is cloned from: migrated
// with store.Migrate (or loaded from schema/, see schemaEnv).
var template = pgtest.Template{
	Name: "store-" + os.Getenv(schemaEnv),
	Setup: func(ctx context.Context, conn *pgx.Conn) error {
		if os.Getenv(schemaEnv) == "source" {
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

func loadSchema(ctx context.Context, conn *pgx.Conn) error {
	files, err := filepath.Glob("schema/*.sql")
	if err != nil {
		return err
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

// open returns a store on a fresh database: as the RLS-bound application
// role with row-level security on, or as the table owner with it off.
func open(t *testing.T, rls bool) *store.Store {
	t.Helper()
	db := pgtest.New(t, template)
	dsn, opts := db.AdminDSN, []store.Option(nil)
	if rls {
		dsn, opts = db.AppDSN, []store.Option{store.WithRowLevelSecurity()}
	}
	s, err := store.Open(t.Context(), dsn, opts...)
	if err != nil {
		t.Fatalf("Open() error got = %v, want = nil", err)
	}
	t.Cleanup(s.Pool().Close)
	return s
}

func storeHarness(rls bool) porttest.Harness {
	return porttest.Harness{
		Name: "postgres",
		New:  func(t *testing.T) port.Transactor { return open(t, rls) },
		Jobs: func(t *testing.T, tr port.Transactor) []port.Job {
			t.Helper()
			jobs, err := tr.(*store.Store).Jobs(t.Context())
			if err != nil {
				t.Fatalf("Jobs() error got = %v, want = nil", err)
			}
			return jobs
		},
		Concurrent: true,
	}
}

func TestConformancePostgres(t *testing.T) {
	t.Run("rls", func(t *testing.T) { t.Parallel(); porttest.Run(t, storeHarness(true)) })
	t.Run("owner", func(t *testing.T) { t.Parallel(); porttest.Run(t, storeHarness(false)) })
}

func TestConformanceMemstore(t *testing.T) {
	porttest.Run(t, porttest.Harness{
		Name: "memstore",
		New:  func(*testing.T) port.Transactor { return memstore.New() },
		Jobs: func(_ *testing.T, tr port.Transactor) []port.Job {
			return tr.(*memstore.Store).Jobs()
		},
	})
}

// TestReposBound checks that the store binds every repository of port.Repos.
func TestReposBound(t *testing.T) {
	s := open(t, false)
	err := s.InTx(t.Context(), func(_ context.Context, r port.Repos) error {
		v := reflect.ValueOf(r)
		for i := range v.NumField() {
			if v.Field(i).IsNil() {
				t.Errorf("port.Repos.%s got = nil, want = bound", v.Type().Field(i).Name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InTx() error got = %v, want = nil", err)
	}
}
