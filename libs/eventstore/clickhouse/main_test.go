package clickhouse_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/eventstore/internal/dockertest"
)

// testDB is the database of the integration tests: TANVO_TEST_CLICKHOUSE_DSN
// (a throwaway database, as the tests truncate the event tables) or a
// clickhouse/clickhouse-server container on the local Docker engine.
var testDB struct {
	conn driver.Conn
	skip string
}

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	dsn := os.Getenv("TANVO_TEST_CLICKHOUSE_DSN")
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
			Image: "clickhouse/clickhouse-server:25.8",
			Env:   []string{"CLICKHOUSE_USER=tanvo", "CLICKHOUSE_PASSWORD=tanvo", "CLICKHOUSE_DB=tanvo"},
			Ports: []string{"9000/tcp"},
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer func() { _ = c.Close(context.Background()) }()
		dsn = fmt.Sprintf("clickhouse://tanvo:tanvo@127.0.0.1:%s/tanvo", c.Ports["9000/tcp"])
	}
	opts, err := ch.ParseDSN(dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	conn, err := ch.Open(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer conn.Close()
	if err := dockertest.Wait(ctx, 2*time.Minute, conn.Ping); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := clickhouse.Migrate(ctx, conn); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	testDB.conn = conn
	return m.Run()
}

// database returns the test database with empty event tables.
func database(t testing.TB) driver.Conn {
	t.Helper()
	if testDB.conn == nil {
		t.Skip(testDB.skip)
	}
	truncate(t)
	return testDB.conn
}

func truncate(t testing.TB) {
	t.Helper()
	for _, table := range []string{"events_enriched", "events_raw", "usage_buckets_15m", "usage_bucket_definitions", "bucket_repairs"} {
		if err := testDB.conn.Exec(context.Background(), "TRUNCATE TABLE "+table); err != nil {
			t.Fatal(err)
		}
	}
}
