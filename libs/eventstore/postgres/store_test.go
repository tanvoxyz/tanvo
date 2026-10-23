package postgres_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/eventstore/postgres"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/metering/storetest"
)

func TestConformance(t *testing.T) {
	store := postgres.New(database(t))
	storetest.Run(t, func(t *testing.T, events []metering.Event) metering.Store {
		truncate(t)
		if err := store.Replace(context.Background(), events); err != nil {
			t.Fatal(err)
		}
		return store
	})
}

func event(tx string, ts time.Time) metering.Event {
	return metering.Event{
		OrganizationID: "org", SubscriptionID: "sub", Code: "code", TransactionID: tx, Timestamp: ts,
		Properties: metering.Properties{"value": "1"},
	}
}

func TestWriteDeduplicates(t *testing.T) {
	ctx := context.Background()
	store := postgres.New(database(t))
	march := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	dups, err := store.Write(ctx, []metering.Event{
		event("a", march),
		event("b", march.AddDate(0, 1, 0)), // another month: another partition
		event("a", march.Add(time.Hour)),   // same transaction, another timestamp
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{2}; !slices.Equal(dups, want) {
		t.Errorf("duplicates got = %v, want = %v", dups, want)
	}

	other := event("a", march)
	other.OrganizationID = "other"
	dups, err = store.Write(ctx, []metering.Event{event("b", march), event("c", march), other})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{0}; !slices.Equal(dups, want) {
		t.Errorf("duplicates got = %v, want = %v", dups, want)
	}

	q := metering.Query{OrganizationID: "org", SubscriptionID: "sub", Code: "code", From: march, To: march.AddDate(0, 2, 0), Property: "value"}
	n, err := store.Count(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("count got = %d, want = 3", n)
	}
	// The first accepted copy of "a" is the one stored.
	last, err := store.LastEvent(ctx, metering.Query{OrganizationID: "org", SubscriptionID: "sub", Code: "code", From: march, To: march.Add(2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if last == nil || last.TransactionID != "c" {
		t.Errorf("last event got = %+v, want = c", last)
	}
}

func TestReplaceKeepsLatestCopy(t *testing.T) {
	ctx := context.Background()
	store := postgres.New(database(t))
	ts := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	e := event("a", ts)
	if _, err := store.Write(ctx, []metering.Event{e}); err != nil {
		t.Fatal(err)
	}
	e.Properties = metering.Properties{"value": "5"}
	if err := store.Replace(ctx, []metering.Event{e}); err != nil {
		t.Fatal(err)
	}
	q := metering.Query{OrganizationID: "org", SubscriptionID: "sub", Code: "code", From: ts, To: ts, Property: "value"}
	sum, err := store.Sum(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Value.String() != "5" || sum.Count != 1 {
		t.Errorf("sum got = %v, want = 5 over 1 event", sum)
	}
	// Ingestion still knows the transaction.
	dups, err := store.Write(ctx, []metering.Event{e})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(dups, []int{0}) {
		t.Errorf("duplicates got = %v, want = [0]", dups)
	}
}

func TestDropPartitions(t *testing.T) {
	ctx := context.Background()
	db := database(t)
	store := postgres.New(db)
	old := time.Date(2001, 1, 15, 0, 0, 0, 0, time.UTC)
	if _, err := store.Write(ctx, []metering.Event{event("old", old)}); err != nil {
		t.Fatal(err)
	}
	var dropped int
	if err := db.QueryRow(ctx, "SELECT events_drop_partitions($1)", time.Date(2001, 2, 1, 0, 0, 0, 0, time.UTC)).Scan(&dropped); err != nil {
		t.Fatal(err)
	}
	if dropped != 1 {
		t.Errorf("dropped got = %d, want = 1", dropped)
	}
	// The store recreates the partition it believed to exist.
	if _, err := store.Write(ctx, []metering.Event{event("old2", old)}); err != nil {
		t.Fatal(err)
	}
}
