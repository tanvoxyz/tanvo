package app

import (
	"context"
	"fmt"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/eventstore/postgres"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// The PostgreSQL event store lives in its own schema (TANVO_EVENTSTORE_SCHEMA),
// apart from the store's raw event log (public.events): the stores
// qualify its tables with the schema ([postgres.WithSchema]), on the main
// pool and inside the ingestion transaction alike.

// eventStoreMonthsAhead is how many months of event partitions the
// migration creates ahead of writes.
const eventStoreMonthsAhead = 3

// migrateEventStore creates the event store's schema if missing and
// applies its DDL there.
func migrateEventStore(ctx context.Context, pool *pgxpool.Pool, schema string, hashPartitions int, now time.Time) error {
	return postgres.Migrate(ctx, pool, postgres.MigrateOptions{
		Schema: schema, HashPartitions: hashPartitions, Now: now, Months: eventStoreMonthsAhead,
	})
}

// checkEventStore reports whether the event store's schema was migrated.
func checkEventStore(ctx context.Context, pool *pgxpool.Pool, schema string) error {
	var ok bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", pgx.Identifier{schema, "event_store_settings"}.Sanitize()).Scan(&ok); err != nil {
		return fmt.Errorf("eventstore: check schema: %w", err)
	}
	if !ok {
		return fmt.Errorf("eventstore: schema %s is not migrated (run tanvo migrate)", schema)
	}
	return nil
}

// openClickHouse connects to TANVO_CLICKHOUSE_URL.
func openClickHouse(ctx context.Context, url string) (driver.Conn, error) {
	opts, err := ch.ParseDSN(url)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: parse TANVO_CLICKHOUSE_URL: %w", err)
	}
	conn, err := ch.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: open: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("clickhouse: connect: %w", err)
	}
	return conn, nil
}

// EventStores selects the event store of an organization: the
// PostgreSQL store for organizations ingesting in postgres mode, ClickHouse
// for those ingesting through Kafka (architecture §6).
type EventStores struct {
	pg     *postgres.Store
	ch     *clickhouse.Store
	modeOf func(core.OrganizationID) ingest.Mode
}

// For returns the event store holding org's events. It is nil when org
// ingests through Kafka and ClickHouse is not configured, which the
// configuration's validation rules out.
func (s *EventStores) For(org core.OrganizationID) metering.Store {
	if s.modeOf(org) == ingest.ModeKafka {
		if s.ch == nil {
			return nil
		}
		return s.ch
	}
	return s.pg
}

// ClickHouse returns the ClickHouse store, nil when not configured.
func (s *EventStores) ClickHouse() *clickhouse.Store { return s.ch }

// newEventStores builds the stores: PostgreSQL over pool in schema, and
// ClickHouse over conn when set, its reads bounded by limits and its
// statements traced with tp.
func newEventStores(pool *pgxpool.Pool, schema string, conn driver.Conn, limits clickhouse.ReadLimits, modeOf func(core.OrganizationID) ingest.Mode, tp trace.TracerProvider) *EventStores {
	s := &EventStores{pg: postgres.New(pool, postgres.WithSchema(schema)), modeOf: modeOf}
	if conn != nil {
		s.ch = clickhouse.New(conn, clickhouse.WithReadLimits(limits), clickhouse.WithTracerProvider(tp))
	}
	return s
}
