package jobs

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// migrateLockKey is the session advisory lock serializing concurrent
// Migrate calls ("tanvo.jobs" as a 64-bit key).
const migrateLockKey int64 = 0x74616e766f6a6f62

// Migrate creates schema ("" is [DefaultSchema]) when missing and applies
// River's pending migrations to it, under an advisory
// lock so that concurrent server instances apply them once.
//
// River's tables live in their own schema rather than beside the business
// tables: libs/store's Atlas migrations own the public schema (a schema
// diff or drift check of it must not see River's tables), River's
// migrations come from rivermigrate on River's own versioning, and the
// queue tables, which hold every organization's jobs and have no
// row-level security, get their own grants.
func Migrate(ctx context.Context, pool *pgxpool.Pool, schema string) error {
	schema = cmp.Or(schema, DefaultSchema)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("jobs: migrate: acquire: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrateLockKey); err != nil {
		return fmt.Errorf("jobs: migrate: lock: %w", err)
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrateLockKey); err != nil {
			slog.WarnContext(ctx, "jobs: migrate: unlock", "error", err)
		}
	}()
	m, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{Schema: schema})
	if err != nil {
		return fmt.Errorf("jobs: migrate: %w", err)
	}
	if _, err := conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{schema}.Sanitize()); err != nil {
		return fmt.Errorf("jobs: migrate: create schema %s: %w", schema, err)
	}
	// River runs each migration in its own transaction: some cannot share
	// one.
	res, err := m.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return fmt.Errorf("jobs: migrate: %w", err)
	}
	for _, v := range res.Versions {
		slog.InfoContext(ctx, "jobs: applied river migration", "schema", schema, "version", v.Version, "name", v.Name)
	}
	return nil
}
