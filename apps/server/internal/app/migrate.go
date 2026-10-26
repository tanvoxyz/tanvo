package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/jobs"
	"github.com/tanvoxyz/tanvo/libs/store"
)

// Migrate applies the schemas of cfg (architecture §5.2) and returns: the
// store's versioned migrations, River's, the PostgreSQL event store's
// and, when TANVO_CLICKHOUSE_URL is set, ClickHouse's. It is `tanvo
// migrate`. It connects to TANVO_DATABASE_DIRECT_URL when set, since the
// migrations hold session advisory locks.
func Migrate(ctx context.Context, cfg config.Config, tp trace.TracerProvider, mp metric.MeterProvider) error {
	dsn, setting := cfg.DatabaseURL, ""
	if cfg.DatabaseDirectURL != "" {
		dsn, setting = cfg.DatabaseDirectURL, "TANVO_DATABASE_DIRECT_URL"
	}
	pool, err := openPool(ctx, dsn, poolOptions{MaxConns: 4, Name: "migrate", Setting: setting}, tp, mp)
	if err != nil {
		return err
	}
	defer pool.Close()
	var conn driver.Conn
	if cfg.ClickHouseURL != "" {
		if conn, err = openClickHouse(ctx, cfg.ClickHouseURL); err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
	}
	return migrateAll(ctx, cfg, pool, conn)
}

// migrateAll applies the schemas over pool and conn (nil skips
// ClickHouse). Each step is idempotent and serialized by an advisory lock,
// so concurrent instances may run it; the store's and River's locks are
// session locks, so pool must not go through a pooler in transaction
// mode.
func migrateAll(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, conn driver.Conn) error {
	start := time.Now()
	if err := store.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := jobs.Migrate(ctx, pool, cfg.JobsSchema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := migrateEventStore(ctx, pool, cfg.EventStoreSchema, cfg.EventStoreHashPartitions, time.Now()); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if conn != nil {
		if err := clickhouse.Migrate(ctx, conn); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	slog.InfoContext(ctx, "migrate: schemas are up to date", "clickhouse", conn != nil, "duration", time.Since(start))
	return nil
}

// checkSchemas verifies, without changing anything, that the PostgreSQL
// schemas were migrated: the store's and River's by their applied
// versions (the store accepts versions of a newer release, for rolling
// deploys of expand-only migrations), the event store's by its
// bookkeeping table. ClickHouse is not checked: its DDL is idempotent and
// applied by `tanvo migrate`.
func checkSchemas(ctx context.Context, cfg config.Config, pool *pgxpool.Pool) error {
	var errs []error
	if err := store.CheckSchema(ctx, pool); err != nil {
		errs = append(errs, err)
	}
	if err := jobs.CheckSchema(ctx, pool, cfg.JobsSchema); err != nil {
		errs = append(errs, err)
	}
	if err := checkEventStore(ctx, pool, cfg.EventStoreSchema); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
