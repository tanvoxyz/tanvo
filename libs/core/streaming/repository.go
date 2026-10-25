package streaming

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Repository stores destinations. Destinations are configured by the
// operator; Create and Update validate them ([Destination.Validate]).
type Repository interface {
	// Create stores a new destination; an event type already claimed by
	// another destination of the organization, active or not, is
	// core.ErrConflict.
	Create(ctx context.Context, d *Destination) error
	// Update stores d over its row, with the claim rule of Create (a
	// destination never conflicts with itself).
	Update(ctx context.Context, d *Destination) error
	// Get returns the destination id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.StreamingDestinationID) (*Destination, error)
	// ForEvent returns the active destinations of org that claim t, in
	// creation order; at most one, given the claim rule.
	ForEvent(ctx context.Context, org core.OrganizationID, t EventType) ([]*Destination, error)
	// ListActive returns the active destinations of org in creation order.
	ListActive(ctx context.Context, org core.OrganizationID) ([]*Destination, error)
}

// LockTTL bounds both delivery locks, so a lost worker cannot block a
// customer's deliveries for longer.
//
// source: app/jobs/deliver_event_job.rb:15
const LockTTL = 10 * time.Minute

// DeliveryLockRepository holds the two locks of the delivery of one event
// type for one customer: the enqueue lock, held from the enqueue of a
// delivery job until it starts, and the runtime lock, held while it runs.
// A refresh landing while a delivery waits is dropped; one landing while a
// delivery runs is queued, and dropped when it starts before the running
// one ends.
//
// source: app/jobs/deliver_event_job.rb:15 (until_and_while_executing)
type DeliveryLockRepository interface {
	// ClaimEnqueue takes the enqueue lock until the instant until, unless
	// it is held past now; it reports whether it was taken.
	ClaimEnqueue(ctx context.Context, org core.OrganizationID, customer core.CustomerID, t EventType, now, until time.Time) (bool, error)
	// ClaimRun releases the enqueue lock and takes the runtime lock until
	// the instant until, unless it is held past now; it reports whether
	// the runtime lock was taken.
	ClaimRun(ctx context.Context, org core.OrganizationID, customer core.CustomerID, t EventType, now, until time.Time) (bool, error)
	// ReleaseRun releases the runtime lock.
	ReleaseRun(ctx context.Context, org core.OrganizationID, customer core.CustomerID, t EventType) error
}

// LockKey names the delivery locks of t for a customer in logs.
func LockKey(t EventType, customer core.CustomerID) string {
	return "streaming:" + string(t) + ":" + customer.String()
}
