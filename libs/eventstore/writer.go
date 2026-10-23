package eventstore

import (
	"context"
	"errors"

	"github.com/tanvoxyz/tanvo/libs/metering"
)

// Writer stores batches of ingested events.
type Writer interface {
	// Write stores the events of a batch that are not duplicates and
	// returns the positions, in events, of the duplicates: the events whose
	// (organization, transaction ID) is already stored or appears earlier
	// in the batch. The first accepted event of a transaction ID wins.
	//
	// Events are ingested in the order of their IngestedAt, then of the
	// batch; a zero IngestedAt is set to the time of the write. That order
	// ranks events sharing a timestamp.
	Write(ctx context.Context, events []metering.Event) (duplicates []int, err error)
}

// Replacer stores events regardless of ingestion deduplication.
type Replacer interface {
	// Replace stores each event as the latest copy of its (organization,
	// subscription, code, transaction ID, timestamp) key, replacing an
	// earlier copy. Re-enrichment and the event replacement API use it;
	// the conformance cases load their events with it, as they hold
	// re-sent copies the queries must collapse. Within the batch, a later
	// copy of a key replaces an earlier one.
	Replace(ctx context.Context, events []metering.Event) error
}

// ErrMissingOrganization is returned for a query or an event without an
// organization ID: every read and write is scoped to one organization.
var ErrMissingOrganization = errors.New("eventstore: missing organization ID")
