package event

import (
	"context"
	"time"

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
	// List returns the events of org matching f, newest first: by
	// timestamp descending, then by ID descending (the event API's list,
	// lago EventsQuery).
	List(ctx context.Context, org core.OrganizationID, f ListFilter, page core.PageRequest) (core.Page[*Event], error)
	// ValidationErrors returns the post-validation errors of the events of
	// org created in [from, to) (domain §10.2, [ValidationErrors]).
	ValidationErrors(ctx context.Context, org core.OrganizationID, from, to time.Time) (ValidationErrors, error)
}

// ListFilter selects the events of [Log.List]; zero fields do not filter.
type ListFilter struct {
	// Code keeps the events of a billable metric code.
	Code string
	// ExternalSubscriptionID keeps the events of an external subscription.
	ExternalSubscriptionID string
	// TimestampFrom keeps the events at or after the instant.
	TimestampFrom *time.Time
	// TimestampTo keeps the events at or before the instant.
	TimestampTo *time.Time
}
