package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ariga.io/atlas/sql/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store"
	"github.com/tanvoxyz/tanvo/libs/store/pgtest"
)

// migrated is a database migrated with store.Migrate, whatever schemaEnv.
var migrated = pgtest.Template{
	Name: "store-migrated",
	Setup: func(ctx context.Context, conn *pgx.Conn) error {
		s, err := store.Open(ctx, conn.Config().ConnString())
		if err != nil {
			return err
		}
		defer s.Pool().Close()
		return store.Migrate(ctx, s.Pool())
	},
}

func TestCheckSchema(t *testing.T) {
	ctx := t.Context()
	empty := pgtest.Pool(t, pgtest.New(t, pgtest.Template{Name: "store-empty"}).AdminDSN)
	if err := store.CheckSchema(ctx, empty); err == nil || !strings.Contains(err.Error(), "not migrated") {
		t.Errorf("CheckSchema() of an empty database error got = %v, want = not migrated", err)
	}

	pool := pgtest.Pool(t, pgtest.New(t, migrated).AdminDSN)
	if err := store.CheckSchema(ctx, pool); err != nil {
		t.Fatalf("CheckSchema() after Migrate() error got = %v, want = nil", err)
	}
	// A newer release's migration is accepted: the previous release keeps
	// running on an expanded schema.
	if _, err := pool.Exec(ctx, `INSERT INTO tanvo_schema_revisions (version, description, checksum) VALUES ('99991231235959', 'future', 'x')`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(ctx, pool); err != nil {
		t.Errorf("CheckSchema() with a newer migration error got = %v, want = nil", err)
	}
	var first string
	if err := pool.QueryRow(ctx, `SELECT min(version) FROM tanvo_schema_revisions`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tanvo_schema_revisions SET checksum = 'changed' WHERE version = $1`, first); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(ctx, pool); err == nil || !strings.Contains(err.Error(), first) || !strings.Contains(err.Error(), "differ") {
		t.Errorf("CheckSchema() with a modified migration error got = %v, want = %s differs", err, first)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM tanvo_schema_revisions WHERE version = $1`, first); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(ctx, pool); err == nil || !strings.Contains(err.Error(), first) || !strings.Contains(err.Error(), "not applied") {
		t.Errorf("CheckSchema() with a missing migration error got = %v, want = %s not applied", err, first)
	}
}

func TestInPgxTx(t *testing.T) {
	s := open(t, false)
	ctx := t.Context()
	txid := func(ctx context.Context, tx pgx.Tx) int64 {
		var id int64
		if err := tx.QueryRow(ctx, "SELECT txid_current()").Scan(&id); err != nil {
			t.Fatalf("txid_current() error got = %v, want = nil", err)
		}
		return id
	}
	err := s.InPgxTx(ctx, func(ctx context.Context, r port.Repos, tx pgx.Tx) error {
		if r.Events == nil {
			t.Error("Repos.Events got = nil, want = bound")
		}
		outer := txid(ctx, tx)
		// A nested call joins the transaction, through either method.
		if err := s.InPgxTx(ctx, func(ctx context.Context, _ port.Repos, inner pgx.Tx) error {
			if got := txid(ctx, inner); got != outer {
				t.Errorf("nested transaction got = %d, want = %d", got, outer)
			}
			return nil
		}); err != nil {
			return err
		}
		return s.InTx(ctx, func(ctx context.Context, _ port.Repos) error {
			return s.InPgxTx(ctx, func(ctx context.Context, _ port.Repos, inner pgx.Tx) error {
				if got := txid(ctx, inner); got != outer {
					t.Errorf("transaction nested in InTx got = %d, want = %d", got, outer)
				}
				return nil
			})
		})
	})
	if err != nil {
		t.Fatalf("InPgxTx() error got = %v, want = nil", err)
	}

	// The statements on the transaction roll back with it.
	errRollback := errors.New("rollback")
	err = s.InPgxTx(ctx, func(ctx context.Context, _ port.Repos, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "CREATE TABLE in_pgx_tx (x int)"); err != nil {
			return err
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("InPgxTx() error got = %v, want = %v", err, errRollback)
	}
	var exists bool
	if err := s.Pool().QueryRow(ctx, "SELECT to_regclass('in_pgx_tx') IS NOT NULL").Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("table created in a rolled back transaction got = present, want = absent")
	}
}

// memDir returns a migration directory of the given files.
func memDir(t *testing.T, files map[string]string) migrate.Dir {
	t.Helper()
	dir := &migrate.MemDir{}
	for name, content := range files {
		if err := dir.WriteFile(name, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func revisionApplied(t *testing.T, pool *pgxpool.Pool, version string) bool {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM tanvo_schema_revisions WHERE version = $1`, version).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestMigrateLockTimeout(t *testing.T) {
	ctx := t.Context()
	pool := pgtest.Pool(t, pgtest.New(t, pgtest.Template{Name: "store-empty"}).AdminDSN)
	if _, err := pool.Exec(ctx, `CREATE TABLE locked (id bigint PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	// A long transaction of the running release reads the table.
	reader, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := reader.Exec(ctx, `SELECT * FROM locked`); err != nil {
		t.Fatal(err)
	}
	dir := memDir(t, map[string]string{"1_add_note.sql": "ALTER TABLE locked ADD COLUMN note text;\n"})

	start := time.Now()
	err = store.MigrateDir(ctx, pool, dir, store.WithLockTimeout(200*time.Millisecond))
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("MigrateDir() behind a held lock error got = %v, want = lock_not_available (55P03)", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("MigrateDir() behind a held lock took %v, want = about the 200ms lock_timeout", elapsed)
	}
	if revisionApplied(t, pool, "1") {
		t.Error("revision of the failed migration got = recorded, want = not recorded")
	}

	// Once the lock is released, the migration is retried.
	if err := reader.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateDir(ctx, pool, dir, store.WithLockTimeout(200*time.Millisecond)); err != nil {
		t.Fatalf("MigrateDir() retried error got = %v, want = nil", err)
	}
	if !revisionApplied(t, pool, "1") {
		t.Error("revision of the retried migration got = not recorded, want = recorded")
	}
}

func TestMigrateWithoutTransaction(t *testing.T) {
	ctx := t.Context()
	pool := pgtest.Pool(t, pgtest.New(t, pgtest.Template{Name: "store-empty"}).AdminDSN)
	if _, err := pool.Exec(ctx, `CREATE TABLE items (id bigint PRIMARY KEY, a text, b text);
		INSERT INTO items VALUES (1, 'a', 'dup'), (2, 'a', 'dup')`); err != nil {
		t.Fatal(err)
	}
	dir := memDir(t, map[string]string{
		"1_items_indexes.sql": `-- atlas:txmode none

-- CONCURRENTLY cannot run in a transaction.
CREATE INDEX CONCURRENTLY IF NOT EXISTS items_a ON items (a);
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS "items_b_key" ON items (b);
`,
	})
	valid := func(index string) (exists, valid bool) {
		t.Helper()
		err := pool.QueryRow(ctx, `SELECT indisvalid FROM pg_index WHERE indexrelid = to_regclass($1)`, index).Scan(&valid)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, false
		}
		if err != nil {
			t.Fatal(err)
		}
		return true, valid
	}

	// The unique index fails halfway through the file, on duplicates,
	// leaving an invalid index.
	if err := store.MigrateDir(ctx, pool, dir); err == nil {
		t.Fatal("MigrateDir() on duplicates error got = nil, want = unique violation")
	}
	if revisionApplied(t, pool, "1") {
		t.Error("revision of the failed migration got = recorded, want = not recorded")
	}
	if exists, ok := valid("items_a"); !exists || !ok {
		t.Errorf("index items_a got = exists %t, valid %t, want = a valid index", exists, ok)
	}
	if exists, ok := valid("items_b_key"); !exists || ok {
		t.Errorf("index items_b_key got = exists %t, valid %t, want = an invalid index", exists, ok)
	}

	// The retry runs the file again: IF NOT EXISTS skips the built index,
	// and the invalid one is dropped and built again.
	if _, err := pool.Exec(ctx, `UPDATE items SET b = id::text`); err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateDir(ctx, pool, dir); err != nil {
		t.Fatalf("MigrateDir() retried error got = %v, want = nil", err)
	}
	if exists, ok := valid("items_b_key"); !exists || !ok {
		t.Errorf("index items_b_key after the retry got = exists %t, valid %t, want = a valid index", exists, ok)
	}
	if !revisionApplied(t, pool, "1") {
		t.Error("revision got = not recorded, want = recorded")
	}
	if err := store.MigrateDir(ctx, pool, dir); err != nil {
		t.Errorf("MigrateDir() applied again error got = %v, want = nil", err)
	}

	// A transactional file cannot hold CONCURRENTLY.
	bad := memDir(t, map[string]string{"2_in_tx.sql": "CREATE INDEX CONCURRENTLY items_id_a ON items (id, a);\n"})
	if err := store.MigrateDir(ctx, pool, bad); err == nil || !strings.Contains(err.Error(), "transaction block") {
		t.Errorf("MigrateDir() of CONCURRENTLY in a transaction error got = %v, want = cannot run inside a transaction block", err)
	}
}
