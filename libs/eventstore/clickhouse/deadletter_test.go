package clickhouse_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/eventstore"
	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
)

func TestWriteDeadLetters(t *testing.T) {
	ctx := context.Background()
	db := database(t)
	store := clickhouse.New(db)
	failed := time.Date(2026, 3, 6, 1, 2, 3, 456000, time.UTC)
	rows := []clickhouse.DeadLetter{
		{
			OrganizationID: "org", SubscriptionID: "sub", Code: "api_calls", TransactionID: "tx",
			Timestamp: failed.Add(-time.Hour), IngestedAt: failed.Add(-time.Minute), FailedAt: failed,
			Event: `{"transaction_id":"tx"}`, ErrorCode: "evaluate_expression", ErrorMessage: "boom", Topic: "events.raw", Partition: 2, Offset: 41,
		},
		{Timestamp: failed, IngestedAt: failed, FailedAt: failed, Event: `"not json"`, ErrorCode: "decode", Topic: "events.raw", Offset: 42},
	}
	if err := store.WriteDeadLetters(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteDeadLetters(ctx, nil); err != nil {
		t.Errorf("empty write error got = %v, want = nil", err)
	}
	var (
		n       uint64
		code    string
		offset  int64
		failure time.Time
	)
	if err := db.QueryRow(ctx, `SELECT count() FROM events_dead_letter`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("rows got = %d, want = 2", n)
	}
	if err := db.QueryRow(ctx, `SELECT error_code, offset, failed_at FROM events_dead_letter WHERE transaction_id = 'tx'`).Scan(&code, &offset, &failure); err != nil {
		t.Fatal(err)
	}
	if code != "evaluate_expression" || offset != 41 || !failure.Equal(failed) {
		t.Errorf("row got = %s %d %v, want = evaluate_expression 41 %v", code, offset, failure, failed)
	}
}

func TestPendingDeadLetters(t *testing.T) {
	ctx := context.Background()
	db := database(t)
	store := clickhouse.New(db)
	at := time.Date(2026, 3, 6, 0, 0, 0, 0, time.UTC)
	dl := func(org, code string, offset int64, failed time.Time) clickhouse.DeadLetter {
		return clickhouse.DeadLetter{
			OrganizationID: org, SubscriptionID: "sub", Code: "calls", TransactionID: "tx", Timestamp: failed, IngestedAt: failed, FailedAt: failed,
			Event: `{}`, ErrorCode: code, Topic: "events.enriched", Offset: offset,
		}
	}
	rows := []clickhouse.DeadLetter{
		dl("org", "reject_event", 1, at),
		dl("org", "reject_event", 1, at), // stored twice
		dl("org", "reject_event", 2, at.Add(time.Minute)),
		dl("org", "write_events", 3, at.Add(2*time.Minute)),
		dl("org", "reject_event", 4, at.Add(time.Hour)), // out of range
		dl("other", "reject_event", 5, at),
	}
	if err := store.WriteDeadLetters(ctx, rows); err != nil {
		t.Fatal(err)
	}
	f := clickhouse.DeadLetterFilter{OrganizationID: "org", FailedFrom: at, FailedTo: at.Add(time.Hour)}
	offsets := func(ds []clickhouse.DeadLetter) []int64 {
		var out []int64
		for _, d := range ds {
			out = append(out, d.Offset)
		}
		return out
	}

	page, err := store.PendingDeadLetters(ctx, f, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := offsets(page), []int64{1, 2}; !slices.Equal(got, want) {
		t.Errorf("first page got = %v, want = %v", got, want)
	}
	pos := page[len(page)-1].Position()
	page, err = store.PendingDeadLetters(ctx, f, &pos, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := offsets(page), []int64{3}; !slices.Equal(got, want) {
		t.Errorf("second page got = %v, want = %v", got, want)
	}
	coded := f
	coded.ErrorCode = "reject_event"
	counts, err := store.CountPendingDeadLetters(ctx, coded)
	if err != nil {
		t.Fatal(err)
	}
	if want := []clickhouse.DeadLetterCount{{ErrorCode: "reject_event", Topic: "events.enriched", Count: 2}}; !slices.Equal(counts, want) {
		t.Errorf("counts got = %v, want = %v", counts, want)
	}

	if err := store.MarkDeadLettersReplayed(ctx, []clickhouse.DeadLetter{rows[0]}, at); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDeadLettersReplayed(ctx, []clickhouse.DeadLetter{rows[0]}, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	page, err = store.PendingDeadLetters(ctx, coded, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := offsets(page), []int64{2}; !slices.Equal(got, want) {
		t.Errorf("after replay got = %v, want = %v", got, want)
	}
	// A replay of another organization's message hides nothing here.
	other := rows[5]
	other.OrganizationID, other.Offset = "other", 2
	if err := store.MarkDeadLettersReplayed(ctx, []clickhouse.DeadLetter{other}, at); err != nil {
		t.Fatal(err)
	}
	if page, err = store.PendingDeadLetters(ctx, coded, nil, 10); err != nil || len(page) != 1 {
		t.Errorf("after another organization's replay got = %v, %v, want = 1 dead letter", offsets(page), err)
	}
	if _, err := store.PendingDeadLetters(ctx, clickhouse.DeadLetterFilter{}, nil, 10); !errors.Is(err, eventstore.ErrMissingOrganization) {
		t.Errorf("no organization error got = %v, want = %v", err, eventstore.ErrMissingOrganization)
	}
}
