package jobs_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanvoxyz/tanvo/libs/dockertest"
	"github.com/tanvoxyz/tanvo/libs/jobs"
)

// testDB is the server of the integration tests: TANVO_TEST_POSTGRES_DSN
// or a postgres:18 container on the local Docker engine. Each test
// migrates River into a schema of its own.
var testDB struct {
	pool *pgxpool.Pool
	skip string
	seq  atomic.Int64
}

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	dsn := os.Getenv("TANVO_TEST_POSTGRES_DSN")
	switch {
	case testing.Short():
		testDB.skip = "integration test skipped with -short"
		return m.Run()
	case dsn == "":
		engine, err := dockertest.Connect(ctx)
		if err != nil {
			testDB.skip = err.Error()
			return m.Run()
		}
		c, err := engine.Run(ctx, dockertest.Spec{
			Image: "postgres:18",
			Env:   []string{"POSTGRES_USER=tanvo", "POSTGRES_PASSWORD=tanvo", "POSTGRES_DB=tanvo"},
			Cmd:   []string{"postgres", "-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off", "-c", "max_connections=300"},
			Ports: []string{"5432/tcp"},
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer func() { _ = c.Close(context.Background()) }()
		dsn = fmt.Sprintf("postgres://tanvo:tanvo@%s/tanvo?sslmode=disable", c.Addr("5432/tcp"))
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer pool.Close()
	if err := dockertest.Wait(ctx, time.Minute, pool.Ping); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	testDB.pool = pool
	return m.Run()
}

var nonIdent = regexp.MustCompile(`[^a-z0-9_]+`)

// schema returns a fresh schema with River's tables, dropped when the test
// ends.
func schema(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	if testDB.pool == nil {
		t.Skip(testDB.skip)
	}
	name := fmt.Sprintf("river_%d_%s", testDB.seq.Add(1), nonIdent.ReplaceAllString(strings.ToLower(t.Name()), "_"))
	name = name[:min(len(name), 46)] // River's bound
	ctx := context.Background()
	if err := jobs.Migrate(ctx, testDB.pool, name); err != nil {
		t.Fatalf("Migrate() = %v", err)
	}
	// A second run applies nothing.
	if err := jobs.Migrate(ctx, testDB.pool, name); err != nil {
		t.Fatalf("Migrate() again = %v", err)
	}
	t.Cleanup(func() {
		if _, err := testDB.pool.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{name}.Sanitize()+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})
	return testDB.pool, name
}

// row is one River job row.
type row struct {
	Kind, Queue, State string
	ScheduledAt        time.Time
	Attempt, Max       int
	Args               string
}

func rows(t *testing.T, s string) []row {
	t.Helper()
	r, err := testDB.pool.Query(context.Background(),
		"SELECT kind, queue, state::text, scheduled_at, attempt, max_attempts, args::text FROM "+pgx.Identifier{s, "river_job"}.Sanitize()+" ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(r, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.Kind, &x.Queue, &x.State, &x.ScheduledAt, &x.Attempt, &x.Max, &x.Args)
		return x, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// eventually polls cond until it holds or the timeout passes.
func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
