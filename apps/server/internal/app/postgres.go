package app

import (
	"context"
	"fmt"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/libs/store"
)

// poolOptions configure [openPool].
type poolOptions struct {
	// MaxConns bounds the pool; 0 keeps pgx's default (or the URL's
	// pool_max_conns).
	MaxConns int32
	// SearchPath, when set, is the search_path of every connection.
	SearchPath string
	// Name tells the pools apart in telemetry ("main", "eventstore").
	Name string
}

// openPool opens a PostgreSQL pool on dsn whose connections decode
// timestamps in UTC (store.ConfigurePool) and are traced and measured by
// otelpgx. It pings the database, so that a wrong URL fails at startup.
func openPool(ctx context.Context, dsn string, o poolOptions, tp trace.TracerProvider, mp metric.MeterProvider) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse TANVO_DATABASE_URL: %w", err)
	}
	store.ConfigurePool(cfg)
	if o.MaxConns > 0 {
		cfg.MaxConns = o.MaxConns
	}
	if o.SearchPath != "" {
		cfg.ConnConfig.RuntimeParams["search_path"] = o.SearchPath
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
