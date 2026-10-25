package postgres_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/eventstore/internal/buckettest"
	"github.com/tanvoxyz/tanvo/libs/eventstore/internal/latency"
	"github.com/tanvoxyz/tanvo/libs/eventstore/postgres"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/metering/storetest"
)

// bucketStore returns a NewBucketStore over the test database, defining
// the buckets before loading the events (maintained on write) or after
// (backfilled).
func bucketStore(t *testing.T, backfill bool) storetest.NewBucketStore {
	// Small chunks make the backfill run many transactions.
	store := open(database(t), postgres.WithBackfillChunk(3))
	return func(t *testing.T, defs []metering.BucketDefinition, events []metering.Event) (metering.Store, metering.BucketReader) {
		truncate(t)
		define := func() {
			for _, d := range defs {
				if err := store.DefineBuckets(t.Context(), d); err != nil {
					t.Fatal(err)
				}
			}
		}
		if !backfill {
			define()
		}
		if err := store.Replace(t.Context(), events); err != nil {
			t.Fatal(err)
		}
		if backfill {
			define()
		}
		return store, store
	}
}

func TestBucketConformance(t *testing.T) {
	storetest.RunBucketConformance(t, bucketStore(t, false))
}

func TestBucketBackfillConformance(t *testing.T) {
	storetest.RunBucketConformance(t, bucketStore(t, true))
}

func TestBucketLifecycle(t *testing.T) {
	store := open(database(t), postgres.WithBackfillChunk(3))
	storetest.RunBucketLifecycle(t, func(t *testing.T, events []metering.Event) (metering.Store, storetest.LifecycleBuckets) {
		truncate(t)
		if err := store.Replace(t.Context(), events); err != nil {
			t.Fatal(err)
		}
		return store, store
	})
}

func TestBucketSpecs(t *testing.T) {
	storetest.RunBucketSpecs(t, bucketStore(t, false))
}

func TestBucketMaintenance(t *testing.T) {
	buckettest.Run(t, func(t *testing.T) buckettest.Backend { return open(database(t), postgres.WithBackfillChunk(3)) },
		func(_ buckettest.Backend, n int) []buckettest.Backend {
			out := make([]buckettest.Backend, n)
			for i := range out {
				out[i] = open(testDB.pool, postgres.WithBackfillChunk(3))
			}
			return out
		})
}

func TestIngestLatency(t *testing.T) {
	latency.Run(t, "postgres", func(t *testing.T) latency.Backend { return open(database(t)) },
		latency.Config{Writers: []int{1, 4}})
}
