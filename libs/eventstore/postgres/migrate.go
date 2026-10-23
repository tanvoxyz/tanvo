package postgres

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed schema.sql
var schema string

// Schema returns the DDL of the event store in the named PostgreSQL schema
// (see [WithSchema]), or unqualified when name is empty. It does not create
// the schema.
func Schema(name string) (string, error) {
	q, err := newQualifier(name)
	if err != nil {
		return "", err
	}
	return q.sql(schema), nil
}

// MigrateOptions configure [Migrate].
type MigrateOptions struct {
	// HashPartitions is the number of hash partitions by organization of
	// each month and of the ledger; 4 when zero. It is fixed by the first
	// migration.
	HashPartitions int
	// Now and Months, when Months is positive, create the partitions of the
	// months from the month of Now through Months months later ahead of
	// writes.
	Now    time.Time
	Months int
	// Schema is the PostgreSQL schema to hold the event store, created if
	// missing; the stores over it take it as [WithSchema]. When empty, the
	// event store goes to the first schema of the search_path.
	Schema string
}

// Migrate applies the schema of the event store, idempotently.
func Migrate(ctx context.Context, db DB, opts MigrateOptions) error {
	n := opts.HashPartitions
	if n == 0 {
		n = 4
	}
	q, err := newQualifier(opts.Schema)
	if err != nil {
		return fmt.Errorf("eventstore: postgres: migrate: %w", err)
	}
	tx, err := schemaDB{db: db, q: q}.Begin(ctx)
	if err != nil {
		return fmt.Errorf("eventstore: postgres: migrate: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('tanvo.event_store.__schema__.migrate'))"); err != nil {
		return fmt.Errorf("eventstore: postgres: migrate: %w", err)
	}
	if opts.Schema != "" {
		// CREATE SCHEMA checks the privilege to create schemas even when
		// the schema exists: it runs only when the schema is missing.
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)", opts.Schema).Scan(&exists); err != nil {
			return fmt.Errorf("eventstore: postgres: migrate: %w", err)
		}
		if !exists {
			if _, err := tx.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{opts.Schema}.Sanitize()); err != nil {
				return fmt.Errorf("eventstore: postgres: migrate: create schema: %w", err)
			}
		}
	}
	if _, err := tx.Exec(ctx, schema); err != nil {
		return fmt.Errorf("eventstore: postgres: migrate: %w", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO __schema__.event_store_settings (hash_partitions) VALUES ($1) ON CONFLICT DO NOTHING", n); err != nil {
		return fmt.Errorf("eventstore: postgres: migrate: %w", err)
	}
	if _, err := tx.Exec(ctx, "SELECT __schema__.event_transactions_create_partitions()"); err != nil {
		return fmt.Errorf("eventstore: postgres: migrate: %w", err)
	}
	if opts.Months > 0 {
		now := opts.Now.UTC()
		if _, err := tx.Exec(ctx, "SELECT __schema__.events_create_partitions($1, $2)", now, now.AddDate(0, opts.Months, 0)); err != nil {
			return fmt.Errorf("eventstore: postgres: migrate: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("eventstore: postgres: migrate: %w", err)
	}
	return nil
}
