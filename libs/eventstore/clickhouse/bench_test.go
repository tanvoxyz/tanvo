package clickhouse_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/eventstore/internal/benchdata"
	"github.com/tanvoxyz/tanvo/libs/metering"
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

// BenchmarkHotBucketWrite writes one event per batch into a bucket already
// holding n events, with a definition in place: the cost of the bucket
// recompute a write runs, which reads every event of the touched buckets.
func BenchmarkHotBucketWrite(b *testing.B) {
	day := time.Date(2026, 3, 10, 10, 0, 0, 0, time.UTC)
	def := metering.BucketDefinition{OrganizationID: "org_h", Code: "calls", Property: "value", Dimensions: []string{"region"}}
	ev := func(tx string, i int) metering.Event {
		return metering.Event{
			OrganizationID: "org_h", SubscriptionID: "sub_h", Code: "calls", TransactionID: tx,
			Timestamp: day.Add(time.Duration(i%900) * time.Second), Properties: metering.Properties{"value": "1", "region": []string{"eu", "us"}[i%2]},
		}
	}
	for _, n := range []int{0, 1_000, 10_000, 100_000} {
		b.Run(fmt.Sprintf("bucket=%d", n), func(b *testing.B) {
			ctx := context.Background()
			store := clickhouse.New(database(b))
			if err := store.DefineBuckets(ctx, def); err != nil {
				b.Fatal(err)
			}
			for chunk := 0; chunk < n; chunk += 10_000 {
				batch := make([]metering.Event, 0, 10_000)
				for i := chunk; i < min(n, chunk+10_000); i++ {
					batch = append(batch, ev(fmt.Sprintf("p%d", i), i))
				}
				if _, err := store.Write(ctx, batch); err != nil {
					b.Fatal(err)
				}
			}
			i := 0
			for b.Loop() {
				if _, err := store.Write(ctx, []metering.Event{ev(fmt.Sprintf("w%d", i), i)}); err != nil {
					b.Fatal(err)
				}
				i++
			}
		})
	}
}
