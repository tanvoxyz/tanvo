package ingest_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/dockertest"
	"github.com/tanvoxyz/tanvo/libs/eventstore/postgres"
	"github.com/tanvoxyz/tanvo/libs/ingest"
)

// pg is the PostgreSQL of the integration tests, started on first use:
// TANVO_TEST_POSTGRES_DSN (a throwaway database) or a postgres:18
// container on the local Docker engine.
var pg struct {
	once    sync.Once
	pool    *pgxpool.Pool
	skip    string
	cleanup func()
}

func TestMain(m *testing.M) {
	flag.Parse()
	code := m.Run()
	if pg.cleanup != nil {
		pg.cleanup()
	}
	os.Exit(code)
}

func startPostgres() (*pgxpool.Pool, func(), error) {
	ctx := context.Background()
	cleanup := func() {}
	dsn := os.Getenv("TANVO_TEST_POSTGRES_DSN")
	if dsn == "" {
		engine, err := dockertest.Connect(ctx)
		if err != nil {
			return nil, nil, err
		}
		c, err := engine.Run(ctx, dockertest.Spec{
			Image: "postgres:18",
			Env:   []string{"POSTGRES_USER=tanvo", "POSTGRES_PASSWORD=tanvo", "POSTGRES_DB=tanvo"},
			Cmd:   []string{"postgres", "-c", "fsync=off", "-c", "synchronous_commit=off"},
			Ports: []string{"5432/tcp"},
		})
		if err != nil {
			return nil, nil, err
		}
		cleanup = func() { _ = c.Close(context.Background()) }
		dsn = fmt.Sprintf("postgres://tanvo:tanvo@%s/tanvo?sslmode=disable", c.Addr("5432/tcp"))
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if err := dockertest.Wait(ctx, time.Minute, pool.Ping); err != nil {
		pool.Close()
		cleanup()
		return nil, nil, err
	}
	err = postgres.Migrate(ctx, pool, postgres.MigrateOptions{})
	if err == nil {
		_, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS ingest_test_log (organization_id text, external_subscription_id text, transaction_id text, PRIMARY KEY (organization_id, external_subscription_id, transaction_id))`)
	}
	if err != nil {
		pool.Close()
		cleanup()
		return nil, nil, err
	}
	return pool, func() { pool.Close(); cleanup() }, nil
}

func database(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped with -short")
	}
	pg.once.Do(func() {
		pool, cleanup, err := startPostgres()
		if errors.Is(err, dockertest.ErrUnavailable) {
			pg.skip = err.Error()
			return
		}
		if err != nil {
			pg.skip = "postgres: " + err.Error()
			return
		}
		pg.pool, pg.cleanup = pool, cleanup
	})
	if pg.pool == nil {
		t.Skip(pg.skip)
	}
	return pg.pool
}

// pgTx is the TxStore of the tests: the raw event log is a table of the
// same transaction as the enriched rows, as libs/store's will be.
type pgTx struct{ pool *pgxpool.Pool }

func (p pgTx) InTx(ctx context.Context, fn func(context.Context, event.Log, ingest.EventWriter) error) error {
	return pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		return fn(ctx, pgLog{tx}, postgres.New(tx))
	})
}

type pgLog struct{ tx pgx.Tx }

func (l pgLog) Append(ctx context.Context, org core.OrganizationID, events []*event.Event) (accepted, duplicates []*event.Event, err error) {
	for _, e := range events {
		tag, err := l.tx.Exec(ctx, `INSERT INTO ingest_test_log VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, org.String(), e.ExternalSubscriptionID, e.TransactionID)
		if err != nil {
			return nil, nil, err
		}
		if tag.RowsAffected() == 1 {
			accepted = append(accepted, e)
		} else {
			duplicates = append(duplicates, e)
		}
	}
	return accepted, duplicates, nil
}

func (pgLog) Get(context.Context, core.OrganizationID, string, string) (*event.Event, error) {
	return nil, core.ErrNotFound
}

func (pgLog) List(context.Context, core.OrganizationID, event.ListFilter, core.PageRequest) (core.Page[*event.Event], error) {
	return core.Page[*event.Event]{}, nil
}

func (pgLog) ValidationErrors(context.Context, core.OrganizationID, time.Time, time.Time) (event.ValidationErrors, error) {
	return event.ValidationErrors{}, nil
}

type staticCatalog struct {
	metric *catalog.BillableMetric
	sub    *subscription.Subscription
	charge *catalog.Charge
}

func (c staticCatalog) Metric(_ context.Context, _ core.OrganizationID, code string) (*catalog.BillableMetric, error) {
	if c.metric != nil && c.metric.Code == code {
		return c.metric, nil
	}
	return nil, nil
}

func (c staticCatalog) Subscriptions(context.Context, core.OrganizationID, string) ([]*subscription.Subscription, error) {
	return []*subscription.Subscription{c.sub}, nil
}

func (c staticCatalog) Charges(context.Context, core.OrganizationID, core.PlanID, core.BillableMetricID) ([]*catalog.Charge, error) {
	return []*catalog.Charge{c.charge}, nil
}

type biller struct {
	mu     sync.Mutex
	err    error
	billed []*ingest.Enriched
}

func (b *biller) BillInAdvance(_ context.Context, events []*ingest.Enriched) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	b.billed = append(b.billed, events...)
	return nil
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, org core.OrganizationID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, org.String()).Scan(&n); err != nil {
		t.Fatalf("count error = %v", err)
	}
	return n
}

func TestPostgresMode(t *testing.T) {
	pool := database(t)
	ctx := context.Background()
	ids := core.UUIDv7Generator{}
	org := core.NewID[core.OrganizationID](ids)
	started := time.Now().Add(-time.Hour)
	m := &catalog.BillableMetric{ID: core.NewID[core.BillableMetricID](ids), OrganizationID: org, Code: "calls", AggregationType: catalog.AggregationTypeSum, FieldName: new("n")}
	sub := &subscription.Subscription{ID: core.NewID[core.SubscriptionID](ids), OrganizationID: org, ExternalID: "sub", PlanID: core.NewID[core.PlanID](ids), CustomerID: core.NewID[core.CustomerID](ids), Status: subscription.StatusActive, StartedAt: &started}
	b := &biller{}
	svc, err := ingest.NewService(ingest.Config{
		Catalog:   staticCatalog{m, sub, &catalog.Charge{PlanID: sub.PlanID, BillableMetricID: m.ID, PayInAdvance: true}},
		Postgres:  pgTx{pool},
		InAdvance: b,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	in := func(tx string) ingest.Input {
		return ingest.Input{TransactionID: tx, Code: "calls", ExternalSubscriptionID: "sub", Properties: map[string]any{"n": "2"}}
	}

	out, err := svc.CreateBatch(ctx, org, []ingest.Input{in("a"), in("b"), in("a")}, ingest.Metadata{})
	if err != nil {
		t.Fatalf("CreateBatch() error = %v", err)
	}
	if out[0].Duplicate || out[1].Duplicate || !out[2].Duplicate {
		t.Errorf("duplicates got = %v %v %v, want = false false true", out[0].Duplicate, out[1].Duplicate, out[2].Duplicate)
	}
	out, err = svc.CreateBatch(ctx, org, []ingest.Input{in("b"), in("c")}, ingest.Metadata{})
	if err != nil || !out[0].Duplicate || out[1].Duplicate {
		t.Fatalf("CreateBatch() got = %+v, %v, want = b duplicate", out, err)
	}
	if got := count(t, pool, `SELECT count(*) FROM events WHERE organization_id = $1`, org); got != 3 {
		t.Errorf("events got = %d, want = 3", got)
	}
	if got := len(b.billed); got != 3 {
		t.Errorf("billed got = %d, want = 3", got)
	}

	// A failed in-advance routing rolls the raw and enriched rows back.
	b.err = errors.New("billing down")
	if _, err := svc.Create(ctx, org, in("d"), ingest.Metadata{}); err == nil {
		t.Fatal("Create() error = nil, want = an error")
	}
	if got := count(t, pool, `SELECT count(*) FROM events WHERE organization_id = $1`, org); got != 3 {
		t.Errorf("events after rollback got = %d, want = 3", got)
	}
	if got := count(t, pool, `SELECT count(*) FROM ingest_test_log WHERE organization_id = $1`, org); got != 3 {
		t.Errorf("log after rollback got = %d, want = 3", got)
	}
	b.err = nil
	if out, err := svc.Create(ctx, org, in("d"), ingest.Metadata{}); err != nil || out.Duplicate {
		t.Errorf("retry got = %+v, %v, want = accepted", out, err)
	}
}
