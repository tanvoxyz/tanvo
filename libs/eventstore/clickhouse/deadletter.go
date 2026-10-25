package clickhouse

import (
	"context"
	"time"
)

// DeadLetter is a row of events_dead_letter: a message the ingest
// consumers could not process.
type DeadLetter struct {
	// OrganizationID, SubscriptionID (the external subscription ID), Code
	// and TransactionID are the event's keys; empty when its message does
	// not have them.
	OrganizationID, SubscriptionID, Code, TransactionID string
	// Timestamp is the event's timestamp, else its ingestion time.
	Timestamp time.Time
	// IngestedAt is the event's ingestion time, else the failure's.
	IngestedAt time.Time
	// FailedAt is the instant of the failure.
	FailedAt time.Time
	// Event is the original message.
	Event string
	// ErrorCode classifies the failure; ErrorMessage is its text.
	ErrorCode, ErrorMessage string
	// Topic, Partition and Offset locate the original message.
	Topic     string
	Partition int32
	Offset    int64
}

// WriteDeadLetters inserts dead letters into events_dead_letter. The table
// is append-only: a dead letter written again is kept twice.
func (s *Store) WriteDeadLetters(ctx context.Context, rows []DeadLetter) error {
	if len(rows) == 0 {
		return nil
	}
	b, err := s.conn.PrepareBatch(ctx, `INSERT INTO events_dead_letter (organization_id, subscription_id, code, transaction_id,
		timestamp, ingested_at, failed_at, event, error_code, error_message, topic, partition, offset)`)
	if err != nil {
		return wrap("write dead letters", err)
	}
	for _, r := range rows {
		if err := b.Append(r.OrganizationID, r.SubscriptionID, r.Code, r.TransactionID, r.Timestamp.UTC(), r.IngestedAt.UTC(), r.FailedAt.UTC(),
			r.Event, r.ErrorCode, r.ErrorMessage, r.Topic, r.Partition, r.Offset); err != nil {
			_ = b.Abort()
			return wrap("write dead letters", err)
		}
	}
	return wrap("write dead letters", b.Send())
}
