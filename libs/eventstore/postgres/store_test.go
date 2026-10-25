package postgres_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/eventstore/postgres"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/metering/storetest"
)

func TestConformance(t *testing.T) {
	store := open(database(t))
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
	store := open(database(t))
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
	store := open(database(t))
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
	store, other := open(db), open(db)
	old := time.Date(2001, 1, 15, 0, 0, 0, 0, time.UTC)
	if _, err := store.Write(ctx, []metering.Event{event("old", old)}); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Write(ctx, []metering.Event{event("other", old)}); err != nil {
		t.Fatal(err)
	}
	dropped, err := store.DropPartitions(ctx, time.Date(2001, 2, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 1 {
		t.Errorf("dropped got = %d, want = 1", dropped)
	}
	// The store that dropped the partition creates it again; the other
	// one recreates the partition it believed to exist when its write
	// finds none.
	for _, s := range []*postgres.Store{store, other} {
		if _, err := s.Write(ctx, []metering.Event{event("old2", old.Add(time.Minute))}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDropPartitionsSessionSettings drops partitions from a session whose
// time zone and date style differ from UTC and ISO: the months dropped are
// still the UTC months ending at or before the given time.
func TestDropPartitionsSessionSettings(t *testing.T) {
	ctx := context.Background()
	conn, err := database(t).Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		// The session settings do not go back to the pool.
		_ = conn.Conn().Close(context.WithoutCancel(ctx))
		conn.Release()
	}()
	if _, err := conn.Exec(ctx, "SET TimeZone = 'Pacific/Kiritimati'; SET DateStyle = 'SQL, DMY'"); err != nil {
		t.Fatal(err)
	}
	store := open(conn)
	jan, feb := time.Date(2001, 1, 31, 23, 0, 0, 0, time.UTC), time.Date(2001, 2, 1, 0, 30, 0, 0, time.UTC)
	if _, err := store.Write(ctx, []metering.Event{event("jan", jan), event("feb", feb)}); err != nil {
		t.Fatal(err)
	}
	dropped, err := store.DropPartitions(ctx, time.Date(2001, 2, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 1 {
		t.Errorf("dropped got = %d, want = 1", dropped)
	}
	q := metering.Query{OrganizationID: "org", SubscriptionID: "sub", Code: "code", IncludeHistory: true, Until: feb.Add(time.Hour)}
	if n, err := store.Count(ctx, q); err != nil || n != 1 {
		t.Errorf("count got = %d %v, want = 1 <nil>", n, err)
	}
}

// TestMigrateRefusesTransactionLedger checks that the schema does not run
// over a ledger keyed by (organization, transaction ID).
func TestMigrateRefusesTransactionLedger(t *testing.T) {
	ctx := context.Background()
	db := database(t)
	t.Cleanup(func() { _, _ = db.Exec(context.WithoutCancel(ctx), "DROP SCHEMA IF EXISTS legacy CASCADE") })
	if _, err := db.Exec(ctx, `CREATE SCHEMA legacy; CREATE TABLE legacy.event_transactions (
		organization_id text NOT NULL, transaction_id text NOT NULL, "timestamp" timestamptz NOT NULL,
		ingested_at timestamptz NOT NULL, PRIMARY KEY (organization_id, transaction_id))`); err != nil {
		t.Fatal(err)
	}
	err := postgres.Migrate(ctx, db, postgres.MigrateOptions{Schema: "legacy"})
	if err == nil || !strings.Contains(err.Error(), "keyed by (organization_id, transaction_id)") {
		t.Errorf("migrate got = %v, want the ledger refused", err)
	}
}

func TestSchemas(t *testing.T) {
	ctx := context.Background()
	db := database(t)
	// A second event store, in the search_path's schema (public), beside
	// the one of the test schema.
	if err := postgres.Migrate(ctx, db, postgres.MigrateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(context.WithoutCancel(ctx), "TRUNCATE public.events, public.event_transactions")
	})
	public, scoped := postgres.New(db), open(db)
	e := event("a", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if _, err := public.Write(ctx, []metering.Event{e}); err != nil {
		t.Fatal(err)
	}
	// The stores keep their transaction IDs apart.
	dups, err := scoped.Write(ctx, []metering.Event{e, e})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(dups, []int{1}) {
		t.Errorf("duplicates got = %v, want = [1]", dups)
	}
	q := metering.Query{OrganizationID: "org", SubscriptionID: "sub", Code: "code", IncludeHistory: true, Until: e.Timestamp.Add(time.Hour)}
	for name, s := range map[string]*postgres.Store{"public": public, "scoped": scoped} {
		n, err := s.Count(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s: count got = %d, want = 1", name, n)
		}
	}
}

func TestValidateSchema(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{"eventstore", true},
		{"_tanvo_2", true},
		{"public", true},
		{"", false},
		{"Events", false},
		{"2events", false},
		{"event-store", false},
		{`a"; DROP TABLE x; --`, false},
		{"it's", false},
		{"pg_events", false},
		{strings.Repeat("a", 64), false},
	}
	for _, tt := range tests {
		err := postgres.ValidateSchema(tt.name)
		if got := err == nil; got != tt.ok {
			t.Errorf("ValidateSchema(%q) got = %v, want = ok %v", tt.name, err, tt.ok)
		}
	}
	// A store over an invalid schema fails without running a statement.
	s := postgres.New(nil, postgres.WithSchema("Bad"))
	if _, err := s.Write(context.Background(), []metering.Event{event("a", time.Now())}); err == nil {
		t.Error("write over an invalid schema got = nil error, want = an error")
	}
	if err := postgres.Migrate(context.Background(), nil, postgres.MigrateOptions{Schema: "Bad"}); err == nil {
		t.Error("migrate of an invalid schema got = nil error, want = an error")
	}
}

// A replacement keeping the ingestion instant of its event, as a
// re-enrichment does, advances the ingestion watermark.
func TestReplaceAdvancesWatermark(t *testing.T) {
	ctx := context.Background()
	store := open(database(t))
	ts := time.Date(2026, 3, 6, 0, 0, 0, 0, time.UTC)
	e := event("a", ts)
	e.IngestedAt = ts
	if _, err := store.Write(ctx, []metering.Event{e}); err != nil {
		t.Fatal(err)
	}
	q := metering.Query{OrganizationID: "org", SubscriptionID: "sub", Code: "code", From: ts, To: ts}
	before, err := store.LastIngestedAt(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if before.Before(ts) {
		t.Fatalf("watermark got = %v, want = at least %v", before, ts)
	}
	e.Properties = metering.Properties{"value": "5"}
	if err := store.Replace(ctx, []metering.Event{e}); err != nil {
		t.Fatal(err)
	}
	after, err := store.LastIngestedAt(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if !after.After(before) {
		t.Errorf("watermark after the replacement got = %v, want = after %v", after, before)
	}
}
