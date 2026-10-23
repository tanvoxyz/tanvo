package clickhouse_test

import (
	"context"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/eventstore/internal/benchdata"
)

// BenchmarkQueries runs the hot aggregation queries over
// TANVO_BENCH_EVENTS events of one subscription.
func BenchmarkQueries(b *testing.B) {
	n := benchdata.Size(b)
	ctx := context.Background()
	conn := database(b)
	store := clickhouse.New(conn)
	benchdata.Load(ctx, b, store, n, 100_000)
	if err := conn.Exec(ctx, "OPTIMIZE TABLE events_enriched FINAL"); err != nil {
		b.Fatal(err)
	}
	benchdata.Run(b, store)
}
