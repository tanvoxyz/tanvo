package event

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Log is the append-only log of raw events. Every method is scoped to an
// organization. Aggregation does not read it: it goes through
// metering.Store.
type Log interface {
	// Append stores the events of org that are new and returns them as
	// accepted; an event whose (organization, transaction_id) is already
	// stored, or repeated earlier in the same call, is returned as a
	// duplicate and dropped (the first one wins, domain §24 item 11).
	Append(ctx context.Context, org core.OrganizationID, events []*Event) (accepted, duplicates []*Event, err error)
	// Get returns the event with that transaction ID, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, transactionID string) (*Event, error)
	// Replace replaces the stored event with e's transaction ID by e (the
	// event correction API), keeping its ID and ingestion time; a missing
	// event fails with core.ErrNotFound.
	Replace(ctx context.Context, e *Event) error
}
