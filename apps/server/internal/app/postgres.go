package app

import (
	"cmp"
	"context"
	"fmt"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/libs/store"
)

// poolOptions configure [openPool].
type poolOptions struct {
	// MaxConns bounds the pool; 0 keeps pgx's default (or the URL's
	// pool_max_conns).
	MaxConns int32
	// Name tells the pools apart in telemetry ("main").
	Name string
	// Setting names the variable of the URL in errors; "" is
	// TANVO_DATABASE_URL.
	Setting string
}

// openPool opens a PostgreSQL pool on dsn whose connections decode
// timestamps in UTC (store.ConfigurePool) and are traced and measured by
// otelpgx. It pings the database, so that a wrong URL fails at startup.
func openPool(ctx context.Context, dsn string, o poolOptions, tp trace.TracerProvider, mp metric.MeterProvider) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse %s: %w", cmp.Or(o.Setting, "TANVO_DATABASE_URL"), err)
	}
	store.ConfigurePool(cfg)
	if o.MaxConns > 0 {
		cfg.MaxConns = o.MaxConns
	}
	cfg.ConnConfig.Tracer = otelpgx.NewTracer(
		otelpgx.WithTracerProvider(tp),
		otelpgx.WithMeterProvider(mp),
	)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: open %s pool: %w", o.Name, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: connect %s pool: %w", o.Name, err)
	}
	if err := otelpgx.RecordStats(pool, otelpgx.WithStatsMeterProvider(mp),
		otelpgx.WithStatsAttributes(attribute.String("db.client.connection.pool.name", o.Name))); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: %s pool stats: %w", o.Name, err)
	}
	return pool, nil
}

// openDirectPool opens the pool of TANVO_DATABASE_DIRECT_URL, named
// "direct", bounded by maxConns; it returns nil when the variable is
// unset.
func openDirectPool(ctx context.Context, cfg config.Config, maxConns int32, tp trace.TracerProvider, mp metric.MeterProvider) (*pgxpool.Pool, error) {
	if cfg.DatabaseDirectURL == "" {
		return nil, nil
	}
	return openPool(ctx, cfg.DatabaseDirectURL, poolOptions{MaxConns: maxConns, Name: "direct", Setting: "TANVO_DATABASE_DIRECT_URL"}, tp, mp)
}

// directMaxConns sizes the direct pool of a serving process: the LISTEN
// connections of the catalog changes and of River, and two for the
// migrations at startup. The concurrency slots are leases written through
// the main pool, so they hold no connection.
const directMaxConns int32 = 2 + 2
