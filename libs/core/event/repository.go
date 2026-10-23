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
	// accepted; an event whose deduplication key (organization,
	// external_subscription_id, transaction_id) is already stored, or
	// repeated earlier in the same call, is returned as a duplicate and
	// dropped, whatever its code and timestamp (the first one wins, domain
	// §24 item 11). One transaction ID under two subscriptions is two
	// events.
	Append(ctx context.Context, org core.OrganizationID, events []*Event) (accepted, duplicates []*Event, err error)
	// Get returns the event of the subscription with that transaction ID,
	// or core.ErrNotFound. An empty externalSubscriptionID matches any
	// subscription and returns the earliest ingested event with the
	// transaction ID, as the event API looks events up by transaction ID
	// alone.
	Get(ctx context.Context, org core.OrganizationID, externalSubscriptionID, transactionID string) (*Event, error)
	// Replace replaces the stored event of e's deduplication key
	// (external_subscription_id, transaction_id) by e (the event
	// correction API), keeping its ID and ingestion time; a missing event
	// fails with core.ErrNotFound. A replacement cannot move an event to
	// another subscription: that is another event.
	Replace(ctx context.Context, e *Event) error
}
