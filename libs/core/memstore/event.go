package memstore

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
)

// eventData holds the tables of package event.
type eventData struct {
	events *table[core.EventID, event.Event]
}

func newEventData() *eventData {
	return &eventData{
		events: newTable[core.EventID](func(e event.Event) event.Event { return *e.Clone() }),
	}
}

func (d *eventData) snapshot() *eventData {
	return &eventData{events: d.events.snapshot()}
}

// eventLogRepo implements event.Log.
type eventLogRepo struct{ d *eventData }

var _ event.Log = eventLogRepo{}

// find returns the event of the deduplication key or, with sub empty, the
// earliest ingested event with the transaction ID.
func (r eventLogRepo) find(org core.OrganizationID, sub, transactionID string) (*event.Event, bool) {
	var out *event.Event
	// In ID order: the earliest ingested first, then the earliest created.
	for _, x := range r.d.events.find(func(x *event.Event) bool {
		return x.OrganizationID == org && x.TransactionID == transactionID && (sub == "" || x.ExternalSubscriptionID == sub)
	}) {
		if out == nil || x.IngestedAt.Before(out.IngestedAt) {
			out = x
		}
	}
	return out, out != nil
}

// eventKey is the deduplication key of an event of an organization.
type eventKey struct{ sub, tx string }

func (r eventLogRepo) Append(_ context.Context, org core.OrganizationID, events []*event.Event) (accepted, duplicates []*event.Event, err error) {
	batch := map[eventKey]bool{}
	for _, e := range events {
		if e.OrganizationID != org {
			return nil, nil, notFound("event of organization", org)
		}
		k := eventKey{e.ExternalSubscriptionID, e.TransactionID}
		if _, ok := r.find(org, e.ExternalSubscriptionID, e.TransactionID); ok || batch[k] {
			duplicates = append(duplicates, e)
			continue
		}
		if r.d.events.has(e.ID) {
			return nil, nil, conflict("event", "id")
		}
		batch[k] = true
		accepted = append(accepted, e)
	}
	for _, e := range accepted {
		r.d.events.put(e.ID, *e)
	}
	return accepted, duplicates, nil
}

func (r eventLogRepo) Get(_ context.Context, org core.OrganizationID, externalSubscriptionID, transactionID string) (*event.Event, error) {
	e, ok := r.find(org, externalSubscriptionID, transactionID)
	if !ok {
		return nil, notFound("event", stringer(transactionID))
	}
	return e, nil
}

func (r eventLogRepo) Replace(_ context.Context, e *event.Event) error {
	if e.ExternalSubscriptionID == "" {
		return notFound("event", stringer(e.TransactionID))
	}
	cur, ok := r.find(e.OrganizationID, e.ExternalSubscriptionID, e.TransactionID)
	if !ok {
		return notFound("event", stringer(e.TransactionID))
	}
	n := *e.Clone()
	n.ID, n.IngestedAt, n.CreatedAt = cur.ID, cur.IngestedAt, cur.CreatedAt
	r.d.events.put(n.ID, n)
	return nil
}
