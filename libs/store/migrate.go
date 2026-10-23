package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"

	"ariga.io/atlas/sql/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationFiles holds the versioned migrations written by
// internal/cmd/migrate-diff and their atlas.sum.
//
//go:embed migrations
var migrationFiles embed.FS

// migrateLockKey is the session advisory lock serializing concurrent
// Migrate calls ("tanvo.migrate" as a 64-bit key).
const migrateLockKey int64 = 0x74616e766f6d6967

// revisionsTable records the applied migrations.
const revisionsTable = "tanvo_schema_revisions"

// Migrate applies the embedded migrations that the database has not applied
// yet, in version order, each in its own transaction. It holds an advisory
// lock meanwhile, so concurrent server instances apply them once. It fails
// when atlas.sum does not match the files, when an applied migration was
// modified (merged migrations are never modified), or when the database has
// applied a migration this binary does not know (it is older than the
// database).
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	files, sum, err := migrations()
	if err != nil {
		return err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("store: migrate: acquire: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrateLockKey); err != nil {
		return fmt.Errorf("store: migrate: lock: %w", err)
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrateLockKey); err != nil {
			slog.WarnContext(ctx, "store: migrate: unlock", "error", err)
		}
	}()
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+revisionsTable+` (
		version text PRIMARY KEY,
		description text NOT NULL,
		checksum text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("store: migrate: create %s: %w", revisionsTable, err)
	}
	applied, err := appliedRevisions(ctx, conn.Conn())
	if err != nil {
		return err
	}
	known := make(map[string]bool, len(files))
	for _, f := range files {
		known[f.Version()] = true
		checksum, err := sum.SumByName(f.Name())
		if err != nil {
			return fmt.Errorf("store: migrate: checksum of %s: %w", f.Name(), err)
		}
		if prev, ok := applied[f.Version()]; ok {
			if prev != checksum {
				return fmt.Errorf("store: migrate: applied migration %s was modified", f.Name())
			}
			continue
		}
		if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(f.Bytes())); err != nil {
				return mapErr(err)
			}
			_, err := tx.Exec(ctx, "INSERT INTO "+revisionsTable+" (version, description, checksum) VALUES ($1, $2, $3)",
				f.Version(), f.Desc(), checksum)
			return err
		}); err != nil {
			return fmt.Errorf("store: migrate: apply %s: %w", f.Name(), err)
		}
		slog.InfoContext(ctx, "store: applied migration", "version", f.Version(), "description", f.Desc())
	}
	for v := range applied {
		if !known[v] {
			return fmt.Errorf("store: migrate: the database has applied migration %s, unknown to this version", v)
		}
	}
	return nil
}

// migrations returns the embedded migration files in version order and
// their validated sum file.
func migrations() ([]migrate.File, migrate.HashFile, error) {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return nil, nil, fmt.Errorf("store: migrations: %w", err)
	}
	dir := &migrate.MemDir{}
	entries, err := fs.ReadDir(sub, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("store: migrations: %w", err)
	}
	for _, e := range entries {
		b, err := fs.ReadFile(sub, e.Name())
		if err != nil {
			return nil, nil, fmt.Errorf("store: migrations: %w", err)
		}
		if err := dir.WriteFile(e.Name(), b); err != nil {
			return nil, nil, fmt.Errorf("store: migrations: %w", err)
		}
	}
	if err := migrate.Validate(dir); err != nil {
		return nil, nil, fmt.Errorf("store: migrations: %w", err)
	}
	files, err := dir.Files()
	if err != nil {
		return nil, nil, fmt.Errorf("store: migrations: %w", err)
	}
	sum, err := dir.Checksum()
	if err != nil {
		return nil, nil, fmt.Errorf("store: migrations: %w", err)
	}
	return files, sum, nil
}

func appliedRevisions(ctx context.Context, conn *pgx.Conn) (map[string]string, error) {
	rows, err := conn.Query(ctx, "SELECT version, checksum FROM "+revisionsTable)
	if err != nil {
		return nil, fmt.Errorf("store: migrate: read %s: %w", revisionsTable, err)
	}
	out := map[string]string{}
	var version, checksum string
	if _, err := pgx.ForEachRow(rows, []any{&version, &checksum}, func() error {
		out[version] = checksum
		return nil
	}); err != nil {
		return nil, fmt.Errorf("store: migrate: read %s: %w", revisionsTable, err)
	}
	return out, nil
}
