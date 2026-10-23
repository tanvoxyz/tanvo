package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/eventstore/postgres"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// The PostgreSQL event store lives in its own schema (TANVO_EVENTSTORE_SCHEMA):
// its tables are unqualified and its events table would collide with the
// store's raw event log (public.events). Reads go through a pool whose
// search_path is that schema; writes inside the ingestion transaction
// switch the transaction's search_path around the write ([schemaWriter]).

// eventStoreMonthsAhead is how many months of event partitions the
// migration creates ahead of writes.
const eventStoreMonthsAhead = 3

// migrateEventStore creates the event store's schema and applies its DDL
// on a dedicated connection whose search_path is the schema; the
// connection is closed afterwards, so that its search_path does not leak
// into the pool.
func migrateEventStore(ctx context.Context, pool *pgxpool.Pool, schema string, hashPartitions int, now time.Time) error {
	c, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("eventstore: acquire: %w", err)
	}
	conn := c.Hijack()
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+ident); err != nil {
		return fmt.Errorf("eventstore: create schema %s: %w", schema, err)
	}
	if _, err := conn.Exec(ctx, "SELECT set_config('search_path', $1, false)", ident); err != nil {
		return fmt.Errorf("eventstore: search_path: %w", err)
	}
	if err := postgres.Migrate(ctx, conn, postgres.MigrateOptions{HashPartitions: hashPartitions, Now: now, Months: eventStoreMonthsAhead}); err != nil {
		return err
	}
	return nil
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

// newEventStores builds the stores: reads over readPool, whose
// search_path is the event store's schema, and ClickHouse over conn when
// set.
func newEventStores(readPool *pgxpool.Pool, conn driver.Conn, modeOf func(core.OrganizationID) ingest.Mode) *EventStores {
	s := &EventStores{pg: postgres.New(readPool), modeOf: modeOf}
	if conn != nil {
		s.ch = clickhouse.New(conn)
	}
	return s
}

// schemaWriter writes enriched events to the PostgreSQL event store inside
// the ingestion transaction tx: it sets the transaction's search_path to
// the event store's schema for the write and restores it afterwards.
// PostgreSQL re-plans cached statements when the search_path changes.
type schemaWriter struct {
	tx     pgx.Tx
	schema string
}

var _ ingest.EventWriter = schemaWriter{}

// Write implements [ingest.EventWriter].
func (w schemaWriter) Write(ctx context.Context, events []metering.Event) (dups []int, err error) {
	var prev string
	if err := w.tx.QueryRow(ctx, "SELECT current_setting('search_path')").Scan(&prev); err != nil {
		return nil, fmt.Errorf("eventstore: search_path: %w", err)
	}
	if _, err := w.tx.Exec(ctx, "SELECT set_config('search_path', $1, true)", pgx.Identifier{w.schema}.Sanitize()); err != nil {
		return nil, fmt.Errorf("eventstore: search_path: %w", err)
	}
	defer func() {
		if _, rerr := w.tx.Exec(ctx, "SELECT set_config('search_path', $1, true)", prev); rerr != nil {
			err = errors.Join(err, fmt.Errorf("eventstore: restore search_path: %w", rerr))
		}
	}()
	return postgres.New(w.tx).Write(ctx, events)
}
