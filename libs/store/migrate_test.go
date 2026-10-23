package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

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
