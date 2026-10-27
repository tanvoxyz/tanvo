package clickhouse

import (
	"context"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/eventstore"
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
	b, err := s.conn.PrepareBatch(consistent(ctx), `INSERT INTO events_dead_letter (organization_id, subscription_id, code, transaction_id,
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

// DeadLetterFilter selects the dead letters of an organization.
type DeadLetterFilter struct {
	// OrganizationID is the organization. Required.
	OrganizationID string
	// FailedFrom and FailedTo bound the failure instant: FailedFrom
	// included, FailedTo excluded; zero for no bound.
	FailedFrom, FailedTo time.Time
	// ErrorCode keeps the dead letters with that code; empty for all.
	ErrorCode string
}

// DeadLetterPosition is the position of a dead letter in the order of
// [Store.PendingDeadLetters]: its failure instant, then its original
// message.
type DeadLetterPosition struct {
	FailedAt  time.Time
	Topic     string
	Partition int32
	Offset    int64
}

// Position returns the position of d.
func (d DeadLetter) Position() DeadLetterPosition {
	return DeadLetterPosition{FailedAt: d.FailedAt, Topic: d.Topic, Partition: d.Partition, Offset: d.Offset}
}

// DeadLetterCount counts the pending dead letters of one error code and
// topic.
type DeadLetterCount struct {
	ErrorCode, Topic string
	Count            uint64
}

// pendingWhere returns the conditions of the pending dead letters of f:
// those matching f whose original message has no replay.
func pendingWhere(f DeadLetterFilter, a *args) ([]string, error) {
	if f.OrganizationID == "" {
		return nil, eventstore.ErrMissingOrganization
	}
	org := a.add(f.OrganizationID)
	where := []string{"organization_id = " + org}
	if !f.FailedFrom.IsZero() {
		where = append(where, "failed_at >= "+a.time(f.FailedFrom))
	}
	if !f.FailedTo.IsZero() {
		where = append(where, "failed_at < "+a.time(f.FailedTo))
	}
	if f.ErrorCode != "" {
		where = append(where, "error_code = "+a.add(f.ErrorCode))
	}
	where = append(where, "(topic, partition, offset) NOT IN (SELECT topic, partition, offset FROM events_dead_letter_replays WHERE organization_id = "+org+")")
	return where, nil
}

// PendingDeadLetters returns up to limit dead letters matching f that have
// not been replayed ([Store.MarkDeadLettersReplayed]), after the position
// after (nil for the first page), ordered by failure instant then original
// message. A message dead-lettered or stored more than once is returned
// once, with its first failure.
func (s *Store) PendingDeadLetters(ctx context.Context, f DeadLetterFilter, after *DeadLetterPosition, limit int) ([]DeadLetter, error) {
	var a args
	where, err := pendingWhere(f, &a)
	if err != nil {
		return nil, wrap("pending dead letters", err)
	}
	if after != nil {
		where = append(where, "(failed_at, topic, partition, offset) > ("+a.time(after.FailedAt)+", "+a.add(after.Topic)+", "+
			a.add(after.Partition)+", "+a.add(after.Offset)+")")
	}
	sql := `SELECT organization_id, subscription_id, code, transaction_id, timestamp, ingested_at, failed_at,
		event, error_code, error_message, topic, partition, offset
		FROM events_dead_letter WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY failed_at, topic, partition, offset
		LIMIT 1 BY topic, partition, offset
		LIMIT ` + a.add(limit)
	var (
		d   DeadLetter
		out []DeadLetter
	)
	dest := []any{
		&d.OrganizationID, &d.SubscriptionID, &d.Code, &d.TransactionID, &d.Timestamp, &d.IngestedAt, &d.FailedAt,
		&d.Event, &d.ErrorCode, &d.ErrorMessage, &d.Topic, &d.Partition, &d.Offset,
	}
	err = s.each(ctx, sql, a, dest, func() error {
		r := d
		r.Timestamp, r.IngestedAt, r.FailedAt = r.Timestamp.UTC(), r.IngestedAt.UTC(), r.FailedAt.UTC()
		out = append(out, r)
		return nil
	})
	return out, wrap("pending dead letters", err)
}

// CountPendingDeadLetters counts the dead letters [Store.PendingDeadLetters]
// returns for f, by error code and topic.
func (s *Store) CountPendingDeadLetters(ctx context.Context, f DeadLetterFilter) ([]DeadLetterCount, error) {
	var a args
	where, err := pendingWhere(f, &a)
	if err != nil {
		return nil, wrap("count dead letters", err)
	}
	sql := `SELECT error_code, topic, count() FROM (
		SELECT error_code, topic FROM events_dead_letter WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY failed_at, topic, partition, offset
		LIMIT 1 BY topic, partition, offset)
		GROUP BY error_code, topic ORDER BY error_code, topic`
	var (
		c   DeadLetterCount
		out []DeadLetterCount
	)
	err = s.each(ctx, sql, a, []any{&c.ErrorCode, &c.Topic, &c.Count}, func() error {
		out = append(out, c)
		return nil
	})
	return out, wrap("count dead letters", err)
}

// MarkDeadLettersReplayed records that the original messages of the dead
// letters were produced again at at, so that [Store.PendingDeadLetters]
// no longer returns them. Marking a message again keeps one row.
func (s *Store) MarkDeadLettersReplayed(ctx context.Context, rows []DeadLetter, at time.Time) error {
	if len(rows) == 0 {
		return nil
	}
	b, err := s.conn.PrepareBatch(consistent(ctx), `INSERT INTO events_dead_letter_replays (organization_id, topic, partition, offset, replayed_at)`)
	if err != nil {
		return wrap("mark dead letters replayed", err)
	}
	for _, r := range rows {
		if err := b.Append(r.OrganizationID, r.Topic, r.Partition, r.Offset, at.UTC()); err != nil {
			_ = b.Abort()
			return wrap("mark dead letters replayed", err)
		}
	}
	return wrap("mark dead letters replayed", b.Send())
}
