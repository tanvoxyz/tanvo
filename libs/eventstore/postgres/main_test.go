package postgres_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanvoxyz/tanvo/libs/eventstore/internal/dockertest"
	"github.com/tanvoxyz/tanvo/libs/eventstore/postgres"
)

// testDB is the database of the integration tests: TANVO_TEST_POSTGRES_DSN
// (a throwaway database, as the tests truncate the event tables) or a
// postgres:18 container on the local Docker engine.
var testDB struct {
	pool *pgxpool.Pool
	skip string
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
			Cmd:   []string{"postgres", "-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off", "-c", "shared_buffers=512MB", "-c", "work_mem=64MB"},
			Ports: []string{"5432/tcp"},
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer func() { _ = c.Close(context.Background()) }()
		dsn = fmt.Sprintf("postgres://tanvo:tanvo@127.0.0.1:%s/tanvo?sslmode=disable", c.Ports["5432/tcp"])
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
	if err := postgres.Migrate(ctx, pool, postgres.MigrateOptions{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	testDB.pool = pool
	return m.Run()
}

// database returns the test database with empty event tables.
func database(t testing.TB) *pgxpool.Pool {
	t.Helper()
	if testDB.pool == nil {
		t.Skip(testDB.skip)
	}
	truncate(t)
	return testDB.pool
}

func truncate(t testing.TB) {
	t.Helper()
	if _, err := testDB.pool.Exec(context.Background(), "TRUNCATE events, event_transactions"); err != nil {
		t.Fatal(err)
	}
}
