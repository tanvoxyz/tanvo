// Package ingest prepares batches of events for the event store writers:
// validation, ingestion order and the deduplication rules both backends
// share.
package ingest

import (
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/eventstore"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// Precision is the time precision of the stores: timestamps are kept to the
// microsecond.
const Precision = time.Microsecond

// Row is an event ready to be stored, with its position in the batch.
type Row struct {
	metering.Event
	Position int
}

// Prepare validates the events and returns them in ingestion order: by
// IngestedAt (zero first), then by position. Timestamps are truncated to
// [Precision] and in UTC, and a zero IngestedAt is set to now.
func Prepare(events []metering.Event, now time.Time) ([]Row, error) {
	rows := make([]Row, len(events))
	for i, e := range events {
		if e.OrganizationID == "" {
			return nil, fmt.Errorf("event %d: %w", i, eventstore.ErrMissingOrganization)
		}
		rows[i] = Row{Event: e, Position: i}
	}
	slices.SortStableFunc(rows, func(a, b Row) int { return a.IngestedAt.Compare(b.IngestedAt) })
	now = now.UTC().Truncate(Precision)
	for i := range rows {
		r := &rows[i]
		r.Timestamp = r.Timestamp.UTC().Truncate(Precision)
		if r.IngestedAt.IsZero() {
			r.IngestedAt = now
		} else {
			r.IngestedAt = r.IngestedAt.UTC().Truncate(Precision)
		}
	}
	return rows, nil
}

// TransactionKey is the deduplication key of an event: (organization,
// external subscription ID, transaction ID). The same transaction ID under
// two subscriptions is two events.
type TransactionKey struct {
	OrganizationID, SubscriptionID, TransactionID string
}

// TransactionKeyOf returns the deduplication key of an event.
func TransactionKeyOf(e metering.Event) TransactionKey {
	return TransactionKey{e.OrganizationID, e.SubscriptionID, e.TransactionID}
}

// FirstPerTransaction keeps the first row of each deduplication key and
// returns the positions of the others.
func FirstPerTransaction(rows []Row) (kept []Row, duplicates []int) {
	seen := make(map[TransactionKey]bool, len(rows))
	for _, r := range rows {
		k := TransactionKeyOf(r.Event)
		if seen[k] {
			duplicates = append(duplicates, r.Position)
			continue
		}
		seen[k] = true
		kept = append(kept, r)
	}
	return kept, duplicates
}

// LastPerKey keeps, in order, the last row of each deduplication key.
func LastPerKey(rows []Row) []Row {
	last := make(map[TransactionKey]int, len(rows))
	for i, r := range rows {
		last[TransactionKeyOf(r.Event)] = i
	}
	out := make([]Row, 0, len(last))
	for i, r := range rows {
		if last[TransactionKeyOf(r.Event)] == i {
			out = append(out, r)
		}
	}
	return out
}
