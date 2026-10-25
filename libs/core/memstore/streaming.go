package memstore

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/streaming"
)

// streamingData holds streaming destinations and their delivery locks.
type streamingData struct {
	destinations *table[core.StreamingDestinationID, streaming.Destination]
	locks        map[streamingLockKey]streamingLock
}

type streamingLockKey struct {
	org      core.OrganizationID
	customer core.CustomerID
	event    streaming.EventType
}

type streamingLock struct{ enqueuedUntil, runningUntil time.Time }

func newStreamingData() *streamingData {
	return &streamingData{
		destinations: newTable[core.StreamingDestinationID](func(d streaming.Destination) streaming.Destination {
			d.EventTypes = slices.Clone(d.EventTypes)
			d.CustomerFullUsageExcludedPlanCodes = slices.Clone(d.CustomerFullUsageExcludedPlanCodes)
			return d
		}),
		locks: map[streamingLockKey]streamingLock{},
	}
}

func (d *streamingData) snapshot() *streamingData {
	return &streamingData{destinations: d.destinations.snapshot(), locks: maps.Clone(d.locks)}
}

// streamingDestinationRepo implements streaming.Repository.
type streamingDestinationRepo struct{ d *streamingData }

var _ streaming.Repository = streamingDestinationRepo{}

func (r streamingDestinationRepo) check(d *streaming.Destination) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if r.d.destinations.any(func(x *streaming.Destination) bool {
		return x.ID != d.ID && x.OrganizationID == d.OrganizationID &&
			slices.ContainsFunc(d.EventTypes, x.Streams)
	}) {
		return conflict("streaming_destination", "event_types")
	}
	return nil
}

func (r streamingDestinationRepo) Create(_ context.Context, d *streaming.Destination) error {
	if err := r.check(d); err != nil {
		return err
	}
	if r.d.destinations.has(d.ID) {
		return conflict("streaming_destination", "id")
	}
	c := *d
	c.Normalize()
	r.d.destinations.put(d.ID, c)
	return nil
}

func (r streamingDestinationRepo) Update(_ context.Context, d *streaming.Destination) error {
	if err := r.check(d); err != nil {
		return err
	}
	cur, ok := r.d.destinations.get(d.ID)
	if !ok || cur.OrganizationID != d.OrganizationID {
		return notFound("streaming_destination", d.ID)
	}
	c := *d
	c.Normalize()
	r.d.destinations.put(d.ID, c)
	return nil
}

func (r streamingDestinationRepo) Get(_ context.Context, org core.OrganizationID, id core.StreamingDestinationID) (*streaming.Destination, error) {
	d, ok := r.d.destinations.get(id)
	if !ok || d.OrganizationID != org {
		return nil, notFound("streaming_destination", id)
	}
	return d, nil
}

func (r streamingDestinationRepo) ForEvent(_ context.Context, org core.OrganizationID, t streaming.EventType) ([]*streaming.Destination, error) {
	return r.d.destinations.find(func(d *streaming.Destination) bool {
		return d.OrganizationID == org && d.Active && d.Streams(t)
	}), nil
}

func (r streamingDestinationRepo) ListActive(_ context.Context, org core.OrganizationID) ([]*streaming.Destination, error) {
	return r.d.destinations.find(func(d *streaming.Destination) bool {
		return d.OrganizationID == org && d.Active
	}), nil
}

// streamingLockRepo implements streaming.DeliveryLockRepository.
type streamingLockRepo struct{ d *streamingData }

var _ streaming.DeliveryLockRepository = streamingLockRepo{}

func (r streamingLockRepo) ClaimEnqueue(_ context.Context, org core.OrganizationID, customer core.CustomerID, t streaming.EventType, now, until time.Time) (bool, error) {
	k := streamingLockKey{org, customer, t}
	l := r.d.locks[k]
	if l.enqueuedUntil.After(now) {
		return false, nil
	}
	l.enqueuedUntil = until
	r.d.locks[k] = l
	return true, nil
}

func (r streamingLockRepo) ClaimRun(_ context.Context, org core.OrganizationID, customer core.CustomerID, t streaming.EventType, now, until time.Time) (bool, error) {
	k := streamingLockKey{org, customer, t}
	l := r.d.locks[k]
	l.enqueuedUntil = time.Time{}
	taken := !l.runningUntil.After(now)
	if taken {
		l.runningUntil = until
	}
	r.d.locks[k] = l
	return taken, nil
}

func (r streamingLockRepo) ReleaseRun(_ context.Context, org core.OrganizationID, customer core.CustomerID, t streaming.EventType) error {
	k := streamingLockKey{org, customer, t}
	if l, ok := r.d.locks[k]; ok {
		l.runningUntil = time.Time{}
		r.d.locks[k] = l
	}
	return nil
}
