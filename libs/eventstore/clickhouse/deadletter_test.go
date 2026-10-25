package clickhouse_test

import (
	"context"
	"testing"
	"time"

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
