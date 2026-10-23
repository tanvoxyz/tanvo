package postgres

import (
	"context"
	_ "embed"
	"fmt"
	"time"
)

//go:embed schema.sql
var schema string

// Schema returns the DDL of the event store.
func Schema() string { return schema }

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
}

// Migrate applies the schema of the event store, idempotently.
func Migrate(ctx context.Context, db DB, opts MigrateOptions) error {
	n := opts.HashPartitions
	if n == 0 {
		n = 4
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("eventstore: postgres: migrate: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('tanvo.event_store.migrate'))"); err != nil {
		return fmt.Errorf("eventstore: postgres: migrate: %w", err)
	}
	if _, err := tx.Exec(ctx, schema); err != nil {
		return fmt.Errorf("eventstore: postgres: migrate: %w", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO event_store_settings (hash_partitions) VALUES ($1) ON CONFLICT DO NOTHING", n); err != nil {
		return fmt.Errorf("eventstore: postgres: migrate: %w", err)
	}
	if _, err := tx.Exec(ctx, "SELECT event_transactions_create_partitions()"); err != nil {
		return fmt.Errorf("eventstore: postgres: migrate: %w", err)
	}
	if opts.Months > 0 {
		now := opts.Now.UTC()
		if _, err := tx.Exec(ctx, "SELECT events_create_partitions($1, $2)", now, now.AddDate(0, opts.Months, 0)); err != nil {
			return fmt.Errorf("eventstore: postgres: migrate: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("eventstore: postgres: migrate: %w", err)
	}
	return nil
}
