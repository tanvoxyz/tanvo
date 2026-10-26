// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

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

// migrationFiles holds the versioned migrations of the analytics schema
// and their atlas.sum.
//
//go:embed migrations
var migrationFiles embed.FS

// migrateLockKey is the advisory lock serializing concurrent Migrate calls
// ("analymig" as a 64-bit key).
const migrateLockKey int64 = 0x616e616c796d6967

// Migrate creates the analytics schema when it does not exist and applies
// the migrations the database has not applied, each in its own
// transaction, under an advisory lock. It fails when atlas.sum does not
// match the files, when an applied migration was modified, or when the
// database applied a migration this binary does not know.
func Migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	files, err := Migrations()
	if err != nil {
		return err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrateLockKey); err != nil {
		return fmt.Errorf("store: migrate: lock: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrateLockKey) }()
	for _, stmt := range []string{
		"CREATE SCHEMA IF NOT EXISTS analytics",
		`CREATE TABLE IF NOT EXISTS analytics.schema_revisions (
			version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`,
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("store: migrate: %w", err)
		}
	}
	applied := map[string]string{}
	rows, err := conn.Query(ctx, "SELECT version, checksum FROM analytics.schema_revisions")
	if err != nil {
		return fmt.Errorf("store: migrate: revisions: %w", err)
	}
	var version, checksum string
	if _, err := pgx.ForEachRow(rows, []any{&version, &checksum}, func() error {
		applied[version] = checksum
		return nil
	}); err != nil {
		return fmt.Errorf("store: migrate: revisions: %w", err)
	}
	known := map[string]bool{}
	for _, f := range files {
		known[f.Version()] = true
		sum := checksumOf(f)
		if got, ok := applied[f.Version()]; ok {
			if got != sum {
				return fmt.Errorf("store: migrate: migration %s was modified after it was applied", f.Name())
			}
			continue
		}
		if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(f.Bytes())); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "INSERT INTO analytics.schema_revisions (version, checksum) VALUES ($1, $2)", f.Version(), sum)
			return err
		}); err != nil {
			return fmt.Errorf("store: migrate: %s: %w", f.Name(), err)
		}
		log.InfoContext(ctx, "analytics migration applied", "version", f.Version())
	}
	for v := range applied {
		if !known[v] {
			return fmt.Errorf("store: migrate: the database applied migration %s, which this binary does not know", v)
		}
	}
	return nil
}

// Migrations returns the embedded migrations, oldest first, once their
// atlas.sum is checked.
func Migrations() ([]migrate.File, error) {
	dir, err := migrationDir()
	if err != nil {
		return nil, err
	}
	if err := migrate.Validate(dir); err != nil {
		return nil, fmt.Errorf("store: migrations: %w", err)
	}
	files, err := dir.Files()
	if err != nil {
		return nil, fmt.Errorf("store: migrations: %w", err)
	}
	return files, nil
}

// migrationDir copies the embedded migrations into an atlas directory.
func migrationDir() (*migrate.MemDir, error) {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: migrations: %w", err)
	}
	entries, err := fs.ReadDir(sub, ".")
	if err != nil {
		return nil, fmt.Errorf("store: migrations: %w", err)
	}
	dir := &migrate.MemDir{}
	for _, e := range entries {
		b, err := fs.ReadFile(sub, e.Name())
		if err != nil {
			return nil, fmt.Errorf("store: migrations: %w", err)
		}
		if err := dir.WriteFile(e.Name(), b); err != nil {
			return nil, fmt.Errorf("store: migrations: %w", err)
		}
	}
	return dir, nil
}

// Checksum returns the atlas.sum of the migrations in dir, for the
// migrate-hash command.
func Checksum(dir migrate.Dir) ([]byte, error) {
	sum, err := dir.Checksum()
	if err != nil {
		return nil, fmt.Errorf("store: checksum: %w", err)
	}
	return sum.MarshalText()
}

func checksumOf(f migrate.File) string {
	h, err := migrate.NewHashFile([]migrate.File{f})
	if err != nil || len(h) == 0 {
		return ""
	}
	return h[0].H
}
