package kafka

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/ingest"
)

// ReplayFilter selects the dead letters of an organization to replay.
type ReplayFilter struct {
	// OrganizationID is the organization. Required.
	OrganizationID core.OrganizationID
	// From and To bound the failure instant: From included, To excluded;
	// zero for no bound.
	From, To time.Time
	// ErrorCode keeps the dead letters with that code ([ingest.ErrorReject],
	// …); empty for all.
	ErrorCode string
}

// DeadLetterCount counts the pending dead letters of one error code and
// source topic.
type DeadLetterCount struct {
	ErrorCode, Topic string
	Count            uint64
}

// DeadLetterStore holds the dead letters the dead letter consumer stored
// ([DeadLetters]) and records their replays.
type DeadLetterStore interface {
	// PendingDeadLetters returns up to limit dead letters matching f whose
	// original message has not been replayed, ordered by failure instant
	// then original message (topic, partition, offset), after after (nil
	// for the first page). Each original message is returned once.
	PendingDeadLetters(ctx context.Context, f ReplayFilter, after *ingest.DeadLetterRecord, limit int) ([]ingest.DeadLetterRecord, error)
	// CountPendingDeadLetters counts what PendingDeadLetters returns for
	// f, by error code and topic.
	CountPendingDeadLetters(ctx context.Context, f ReplayFilter) ([]DeadLetterCount, error)
	// MarkReplayed records that the original messages of the dead letters
	// were produced again at at.
	MarkReplayed(ctx context.Context, dead []ingest.DeadLetterRecord, at time.Time) error
}

// DefaultReplayBatchSize bounds the dead letters replayed at once.
const DefaultReplayBatchSize = 500

// Replayer produces the original messages of dead letters again to their
// source topics, keyed as their producers key them, once the cause of
// their failure is fixed. Replays are idempotent: the event stores drop
// the copies of a deduplication key (organization, external subscription
// ID, transaction ID) and in-advance billing is unique on it, so an event
// replayed twice, or replayed although a retry already stored it, is
// stored and billed once. A replayed message that fails again is dead
// letter again, with its new offset, and can be replayed again.
type Replayer struct {
	// Client produces the messages. Required.
	Client *kgo.Client
	// Topics are the ingestion topics: dead letters of the raw, enriched
	// and usage refresh topics are replayed, others skipped.
	Topics Topics
	// Store holds the dead letters. Required.
	Store DeadLetterStore
	// BatchSize overrides [DefaultReplayBatchSize] when positive.
	BatchSize int
	// Clock tells the replay instants; the system clock when nil.
	Clock core.Clock
	// Logger logs the skipped dead letters; slog.Default() when nil.
	Logger *slog.Logger
}

// ReplayResult counts the dead letters of a replay.
type ReplayResult struct {
	// Replayed counts the messages produced again.
	Replayed int
	// Skipped counts the dead letters that cannot be replayed (another
	// topic, a message without its keys); they stay pending.
	Skipped int
}

// Count counts the dead letters f selects that are not replayed yet: the
// dry run of [Replayer.Replay].
func (r Replayer) Count(ctx context.Context, f ReplayFilter) ([]DeadLetterCount, error) {
	if f.OrganizationID.IsZero() {
		return nil, errors.New("kafka: replay: missing organization")
	}
	return r.Store.CountPendingDeadLetters(ctx, f)
}

// Replay produces the original message of every dead letter f selects
// that is not replayed yet and failed before the replay started, page by
// page, and records each page's replay once its messages are
// acknowledged. A replay that stops between the two
// produces the page again on the next run, which the stores deduplicate.
func (r Replayer) Replay(ctx context.Context, f ReplayFilter) (ReplayResult, error) {
	var res ReplayResult
	if f.OrganizationID.IsZero() {
		return res, errors.New("kafka: replay: missing organization")
	}
	if r.Client == nil || r.Store == nil {
		return res, errors.New("kafka: replay needs a client and a store")
	}
	size := r.BatchSize
	if size <= 0 {
		size = DefaultReplayBatchSize
	}
	clock, logger := r.Clock, r.Logger
	if clock == nil {
		clock = core.SystemClock{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	// A replayed message that fails again is dead-lettered again while the
	// replay runs: the replay stops at the dead letters that failed before
	// it started, so that it never replays its own.
	if start := clock.Now(); f.To.IsZero() || f.To.After(start) {
		f.To = start
	}
	var after *ingest.DeadLetterRecord
	for {
		page, err := r.Store.PendingDeadLetters(ctx, f, after, size)
		if err != nil {
			return res, fmt.Errorf("kafka: replay: %w", err)
		}
		if len(page) == 0 {
			return res, nil
		}
		last := page[len(page)-1]
		after = &last
		var recs []*kgo.Record
		var replayed []ingest.DeadLetterRecord
		for _, d := range page {
			rec, err := r.Topics.replayRecord(d)
			if err != nil {
				res.Skipped++
				logger.WarnContext(ctx, "kafka: replay: skip dead letter", "topic", d.Topic, "partition", d.Partition, "offset", d.Offset, "error", err)
				continue
			}
			recs = append(recs, rec)
			replayed = append(replayed, d)
		}
		if len(recs) == 0 {
			continue
		}
		if err := r.Client.ProduceSync(ctx, recs...).FirstErr(); err != nil {
			return res, fmt.Errorf("kafka: replay: produce: %w", err)
		}
		if err := r.Store.MarkReplayed(ctx, replayed, clock.Now()); err != nil {
			return res, fmt.Errorf("kafka: replay: %w", err)
		}
		res.Replayed += len(recs)
	}
}

// errNotReplayable is returned for a dead letter that cannot be replayed.
var errNotReplayable = errors.New("kafka: dead letter cannot be replayed")

// replayRecord returns the record that produces d's original message
// again: to its source topic, keyed as that topic's producer keys it.
func (t Topics) replayRecord(d ingest.DeadLetterRecord) (*kgo.Record, error) {
	value, err := originalMessage(d.Event)
	if err != nil {
		return nil, err
	}
	org, err := core.ParseID[core.OrganizationID](d.OrganizationID)
	if err != nil {
		return nil, fmt.Errorf("%w: organization: %w", errNotReplayable, err)
	}
	var key []byte
	switch d.Topic {
	case t.Raw:
		key = ingest.PartitionKey(org, d.ExternalSubscriptionID, d.TransactionID)
	case t.Enriched:
		key = ingest.SubscriptionPartitionKey(org, d.ExternalSubscriptionID)
	case t.Signals:
		var s ingest.UsageSignal
		if err := json.Unmarshal(value, &s); err != nil {
			return nil, fmt.Errorf("%w: usage signal: %w", errNotReplayable, err)
		}
		key = signalKey(s)
	default:
		return nil, fmt.Errorf("%w: topic %q is not an ingestion topic", errNotReplayable, d.Topic)
	}
	return &kgo.Record{Topic: d.Topic, Key: key, Value: value}, nil
}

// originalMessage returns the message a dead letter's event holds: the
// JSON as is, or the bytes of a JSON string, the form of a message that
// is not JSON ([ingest.MarshalDeadLetter]).
func originalMessage(event json.RawMessage) ([]byte, error) {
	b := bytes.TrimSpace(event)
	if len(b) == 0 {
		return nil, fmt.Errorf("%w: empty message", errNotReplayable)
	}
	if b[0] != '"' {
		return b, nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%w: message: %w", errNotReplayable, err)
	}
	return []byte(s), nil
}
