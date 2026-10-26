package clickhouse_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/metering/storetest"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestConformance(t *testing.T) {
	store := clickhouse.New(database(t))
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
	store := clickhouse.New(database(t))
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
	store := clickhouse.New(database(t))
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

func TestWriteKeepsMetadata(t *testing.T) {
	ctx := context.Background()
	conn := database(t)
	store := clickhouse.New(conn)
	e := event("a", time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC))
	e.Metadata = metering.EventMetadata{UserAgent: "curl/8", IPAddress: "192.0.2.1"}
	if _, err := store.Write(ctx, []metering.Event{e}); err != nil {
		t.Fatal(err)
	}
	var ua, ip string
	if err := conn.QueryRow(ctx, `SELECT user_agent, ip_address FROM events_raw FINAL WHERE transaction_id = 'a'`).Scan(&ua, &ip); err != nil {
		t.Fatal(err)
	}
	if got := (metering.EventMetadata{UserAgent: ua, IPAddress: ip}); got != e.Metadata {
		t.Errorf("stored metadata got = %+v, want = %+v", got, e.Metadata)
	}
}

// A value its column cannot hold is a rejection, which a writer isolates;
// the other events of the store's batch are stored once written alone.
func TestWriteRejects(t *testing.T) {
	ctx := context.Background()
	store := clickhouse.New(database(t))
	march := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	huge := money.MustParse("1" + strings.Repeat("0", 60)) // Decimal(76, 26) holds 50 integer digits
	poison := event("poison", march)
	poison.PreciseTotalAmountCents = &huge
	_, err := store.Write(ctx, []metering.Event{event("a", march), poison})
	if !errors.Is(err, core.ErrInvalidValue) {
		t.Fatalf("write error got = %v, want = %v", err, core.ErrInvalidValue)
	}
	noOrg := event("b", march)
	noOrg.OrganizationID = ""
	if _, err := store.Write(ctx, []metering.Event{noOrg}); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("write without organization error got = %v, want = %v", err, core.ErrInvalidValue)
	}
	if _, err := store.Write(ctx, []metering.Event{event("a", march)}); err != nil {
		t.Fatal(err)
	}
	n, err := store.Count(ctx, metering.Query{OrganizationID: "org", SubscriptionID: "sub", Code: "code", From: march, To: march.Add(time.Hour)})
	if err != nil || n != 1 {
		t.Errorf("count got = %d, %v, want = 1", n, err)
	}
	// A failure of the server is not a rejection.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.Write(cctx, []metering.Event{event("c", march)}); err == nil || errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("canceled write error got = %v, want = a failure that is not a rejection", err)
	}
}
