package clickhouse_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/tanvoxyz/tanvo/libs/dockertest"
	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
)

// testDB is the database of the integration tests: TANVO_TEST_CLICKHOUSE_DSN
// (a throwaway database, as the tests truncate the event tables) or a
// clickhouse/clickhouse-server container on the local Docker engine. With
// TANVO_TEST_CLICKHOUSE_REPLICATED=1 the container runs two replicas of a
// Replicated database (see replicas_test.go): conn opens its connections
// on either, in turn, and replicas holds one connection to each.
var testDB struct {
	conn     driver.Conn
	replicas []driver.Conn
	skip     string
}

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	dsn := os.Getenv("TANVO_TEST_CLICKHOUSE_DSN")
	replicated := os.Getenv("TANVO_TEST_CLICKHOUSE_REPLICATED") == "1"
	var nodes []string
	switch {
	case testing.Short():
		testDB.skip = "integration test skipped with -short"
		return m.Run()
	case replicated:
		engine, err := dockertest.Connect(ctx)
		if err != nil {
			testDB.skip = err.Error()
			return m.Run()
		}
		c, addrs, err := startReplicas(ctx, engine)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer func() { _ = c.Close(context.Background()) }()
		nodes = addrs
		dsn = fmt.Sprintf("clickhouse://%s/tanvo?connection_open_strategy=round_robin", strings.Join(addrs, ","))
	case dsn == "":
		engine, err := dockertest.Connect(ctx)
		if err != nil {
			testDB.skip = err.Error()
			return m.Run()
		}
		c, err := engine.Run(ctx, dockertest.Spec{
			Image: clickhouseImage,
			Env:   []string{"CLICKHOUSE_USER=tanvo", "CLICKHOUSE_PASSWORD=tanvo", "CLICKHOUSE_DB=tanvo"},
			Ports: []string{"9000/tcp"},
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer func() { _ = c.Close(context.Background()) }()
		dsn = fmt.Sprintf("clickhouse://tanvo:tanvo@%s/tanvo", c.Addr("9000/tcp"))
	}
	for _, addr := range nodes {
		conn, err := open(ctx, "clickhouse://"+addr+"/tanvo")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer conn.Close()
		testDB.replicas = append(testDB.replicas, conn)
	}
	conn, err := open(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer conn.Close()
	if err := clickhouse.Migrate(ctx, conn); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	testDB.conn = conn
	return m.Run()
}

const clickhouseImage = "clickhouse/clickhouse-server:25.8"

func open(ctx context.Context, dsn string) (driver.Conn, error) {
	opts, err := ch.ParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	conn, err := ch.Open(opts)
	if err != nil {
		return nil, err
	}
	if err := dockertest.Wait(ctx, 2*time.Minute, conn.Ping); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// nodes returns a connection to each node of the test database.
func nodes() []driver.Conn {
	if testDB.replicas != nil {
		return testDB.replicas
	}
	return []driver.Conn{testDB.conn}
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
	tables := []string{"events_enriched", "events_raw", "usage_buckets_15m", "usage_bucket_definitions", "usage_bucket_blocks", "usage_bucket_reads", "bucket_repairs", "events_dead_letter", "events_dead_letter_replays", "security_logs", "api_logs", "activity_logs"}
	if testDB.replicas != nil {
		// Each truncation in a Replicated database waits for both replicas:
		// truncate the tables that hold rows on either.
		tables = tables[:0]
		for _, conn := range testDB.replicas {
			rows, err := conn.Query(context.Background(), "SELECT name FROM system.tables WHERE database = currentDatabase() AND total_rows > 0")
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					t.Fatal(err)
				}
				if !slices.Contains(tables, name) {
					tables = append(tables, name)
				}
			}
			rows.Close()
		}
	}
	for _, table := range tables {
		if err := testDB.conn.Exec(context.Background(), "TRUNCATE TABLE "+table); err != nil {
			t.Fatal(err)
		}
	}
}
