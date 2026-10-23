package memstore

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
)

// eventData holds the tables of package event.
type eventData struct {
	events             *table[core.EventID, event.Event]
	cachedAggregations *table[core.CachedAggregationID, event.CachedAggregation]
}

func newEventData() *eventData {
	return &eventData{
		events: newTable[core.EventID](func(e event.Event) event.Event { return *e.Clone() }),
		cachedAggregations: newTable[core.CachedAggregationID](func(a event.CachedAggregation) event.CachedAggregation {
			a.GroupedBy = event.CloneGroups(a.GroupedBy)
			a.CurrentAggregation = invoiceClonePtr(a.CurrentAggregation)
			a.MaxAggregation = invoiceClonePtr(a.MaxAggregation)
			a.MaxAggregationWithProration = invoiceClonePtr(a.MaxAggregationWithProration)
			a.PresentationBreakdowns = slices.Clone(a.PresentationBreakdowns)
			for i, b := range a.PresentationBreakdowns {
				a.PresentationBreakdowns[i].Groups = event.CloneGroups(b.Groups)
			}
			return a
		}),
	}
}

func (d *eventData) snapshot() *eventData {
	return &eventData{events: d.events.snapshot(), cachedAggregations: d.cachedAggregations.snapshot()}
}

// eventLogRepo implements event.Log.
type eventLogRepo struct {
	d *eventData
	// c holds the billable metrics post-validation checks events against.
	c *catalogData
}

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

func (r eventLogRepo) List(_ context.Context, org core.OrganizationID, f event.ListFilter, page core.PageRequest) (core.Page[*event.Event], error) {
	if page.Token != "" {
		c, err := core.DecodeCursor(page.Token)
		if err != nil {
			return core.Page[*event.Event]{}, err
		}
		if _, err := time.Parse(time.RFC3339Nano, c.Key); err != nil {
			return core.Page[*event.Event]{}, fmt.Errorf("%w: page token", core.ErrInvalidValue)
		}
	}
	rows := r.d.events.find(func(x *event.Event) bool {
		return x.OrganizationID == org &&
			(f.Code == "" || x.Code == f.Code) &&
			(f.ExternalSubscriptionID == "" || x.ExternalSubscriptionID == f.ExternalSubscriptionID) &&
			(f.TimestampFrom == nil || !x.Timestamp.Before(*f.TimestampFrom)) &&
			(f.TimestampTo == nil || !x.Timestamp.After(*f.TimestampTo))
	})
	slices.SortFunc(rows, func(a, b *event.Event) int {
		if c := b.Timestamp.Compare(a.Timestamp); c != 0 {
			return c
		}
		return b.ID.Compare(a.ID)
	})
	return paginateSorted(rows,
		func(x *event.Event) core.Cursor {
			return core.Cursor{Key: x.Timestamp.UTC().Format(time.RFC3339Nano), ID: x.ID.UUID()}
		},
		func(x *event.Event, c core.Cursor) bool {
			at, _ := time.Parse(time.RFC3339Nano, c.Key) // checked above
			if !x.Timestamp.Equal(at) {
				return x.Timestamp.Before(at)
			}
			return x.ID.UUID().String() < c.ID.String()
		}, page)
}
