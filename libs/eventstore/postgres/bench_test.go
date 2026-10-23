package postgres_test

import (
	"context"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/eventstore/internal/benchdata"
)

// BenchmarkQueries runs the hot aggregation queries over
// TANVO_BENCH_EVENTS events of one subscription.
func BenchmarkQueries(b *testing.B) {
	n := benchdata.Size(b)
	ctx := context.Background()
	db := database(b)
	store := open(db)
	benchdata.Load(ctx, b, store, n, 10_000)
	if _, err := db.Exec(ctx, "VACUUM ANALYZE eventstore.events"); err != nil {
		b.Fatal(err)
	}
	benchdata.Run(b, store)
}
