package clickhouse_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/eventstore/internal/buckettest"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/metering/storetest"
)

// bucketStore returns a NewBucketStore over the test database, defining
// the buckets before loading the events (maintained on write) or after
// (backfilled).
func bucketStore(t *testing.T, backfill bool) storetest.NewBucketStore {
	store := clickhouse.New(database(t))
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

func TestBucketSpecs(t *testing.T) {
	storetest.RunBucketSpecs(t, bucketStore(t, false))
}

func TestBucketMaintenance(t *testing.T) {
	buckettest.Run(t, func(t *testing.T) buckettest.Backend { return clickhouse.New(database(t)) })
}

// TestBucketRepair inserts events past the store, as a write failing
// between its insert and its bucket maintenance leaves them, and checks
// that a retry of the write or a repair sweep heals their buckets.
func TestBucketRepair(t *testing.T) {
	day := time.Date(2026, 3, 10, 10, 0, 0, 0, time.UTC)
	def := metering.BucketDefinition{OrganizationID: "org_r", Code: "calls", Property: "value"}
	ev := func(tx string, minute int, value string) metering.Event {
		return metering.Event{
			OrganizationID: "org_r", SubscriptionID: "sub_r", Code: "calls", TransactionID: tx,
			Timestamp: day.Add(time.Duration(minute) * time.Minute), IngestedAt: day, Properties: metering.Properties{"value": value},
		}
	}
	// orphan stores e as a write whose maintenance failed.
	orphan := func(t *testing.T, conn driver.Conn, e metering.Event, sequence uint64) {
		t.Helper()
		for _, sql := range []string{
			`INSERT INTO events_enriched (organization_id, code, subscription_id, timestamp, transaction_id, properties, precise_total_amount_cents, ingested_at, sequence)
				VALUES ($1, $2, $3, $4, $5, $6, NULL, $7, $8)`,
			`INSERT INTO events_raw (organization_id, code, subscription_id, timestamp, transaction_id, properties, precise_total_amount_cents, ingested_at, sequence)
				VALUES ($1, $2, $3, $4, $5, $6, NULL, $7, $8)`,
		} {
			if err := conn.Exec(t.Context(), sql, e.OrganizationID, e.Code, e.SubscriptionID, e.Timestamp, e.TransactionID,
				map[string]string(e.Properties), e.IngestedAt, sequence); err != nil {
				t.Fatal(err)
			}
		}
	}
	sum := func(t *testing.T, s *clickhouse.Store) string {
		t.Helper()
		q := metering.Query{OrganizationID: "org_r", SubscriptionID: "sub_r", Code: "calls", Numeric: true, Property: "value"}
		rows, ok, err := s.ReadBuckets(t.Context(), metering.BucketRead{Query: q, From: day, To: day.Add(time.Hour)})
		if err != nil || !ok || len(rows) != 1 {
			t.Fatalf("read got = %v %v %v, want one row", rows, ok, err)
		}
		return fmt.Sprintf("%s/%d", rows[0].Sum.Reduce(), rows[0].Count)
	}
	tests := []struct {
		name string
		heal func(t *testing.T, s *clickhouse.Store, orphaned metering.Event, since time.Time)
	}{
		{"retry of the write", func(t *testing.T, s *clickhouse.Store, e metering.Event, _ time.Time) {
			dups, err := s.Write(t.Context(), []metering.Event{e})
			if err != nil || len(dups) != 1 {
				t.Fatalf("retry got = %v %v, want = [0] <nil>", dups, err)
			}
		}},
		{"repair sweep", func(t *testing.T, s *clickhouse.Store, _ metering.Event, since time.Time) {
			if err := s.RepairBuckets(t.Context(), since); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := database(t)
			s := clickhouse.New(conn)
			if err := s.DefineBuckets(t.Context(), def); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Write(t.Context(), []metering.Event{ev("a", 1, "2"), ev("b", 2, "3")}); err != nil {
				t.Fatal(err)
			}
			since := time.Now()
			orphaned := ev("c", 3, "10")
			orphan(t, conn, orphaned, uint64(time.Now().UnixNano()))
			if got := sum(t, s); got != "5/2" {
				t.Fatalf("stale sum got = %s, want = 5/2", got)
			}
			tt.heal(t, s, orphaned, since)
			if got, want := sum(t, s), "15/3"; got != want {
				t.Errorf("healed sum got = %s, want = %s", got, want)
			}
		})
	}
}
