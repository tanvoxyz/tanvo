package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindEvent binds the repositories of the event aggregates.
func (t *txState) bindEvent(r *port.Repos) {
	r.Events = eventLogRepo{t}
	r.CachedAggregations = cachedAggregationRepo{t}
}

// eventLogRepo implements event.Log.
type eventLogRepo struct{ t *txState }

var _ event.Log = eventLogRepo{}

// eventStrings encodes a string map as a JSON object (never null).
func eventStrings(m map[string]string) []byte {
	if m == nil {
		m = map[string]string{}
	}
	b, _ := encodeJSON(m) // a map of strings always encodes
	return b
}

// eventStringsOf decodes a JSON object of strings (an empty one to nil).
func eventStringsOf(b []byte) (map[string]string, error) {
	var m map[string]string
	if err := decodeJSON(b, &m); err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, nil
	}
	return m, nil
}

// eventMetadata encodes the request metadata as a JSON object (never
// null).
func eventMetadata(m event.Metadata) []byte {
	b, _ := encodeJSON(m) // a struct of strings always encodes
	return b
}

func eventParams(e *event.Event) db.AppendEventParams {
	p := db.AppendEventParams{
		OrganizationID:         e.OrganizationID.UUID(),
		ID:                     e.ID.UUID(),
		TransactionID:          e.TransactionID,
		Code:                   e.Code,
		ExternalSubscriptionID: e.ExternalSubscriptionID,
		Timestamp:              utc(e.Timestamp),
		Properties:             eventStrings(e.Properties),
		IngestedAt:             utc(e.IngestedAt),
		CreatedAt:              utc(e.CreatedAt),
		UpdatedAt:              utc(e.UpdatedAt),
		Metadata:               eventMetadata(e.Metadata),
	}
	if e.PreciseTotalAmount != nil {
		v := *e.PreciseTotalAmount
		p.PreciseTotalAmount = &v
	}
	return p
}

func eventOf(r db.Event) (*event.Event, error) {
	props, err := eventStringsOf(r.Properties)
	if err != nil {
		return nil, fmt.Errorf("event %s: properties: %w", r.ID, err)
	}
	var md event.Metadata
	if err := decodeJSON(r.Metadata, &md); err != nil {
		return nil, fmt.Errorf("event %s: metadata: %w", r.ID, err)
	}
	amount := r.PreciseTotalAmount
	if amount != nil {
		v := *amount
		amount = &v
	}
	return &event.Event{
		ID:                     idOf[core.EventID](r.ID),
		OrganizationID:         idOf[core.OrganizationID](r.OrganizationID),
		TransactionID:          r.TransactionID,
		Code:                   r.Code,
		ExternalSubscriptionID: r.ExternalSubscriptionID,
		Timestamp:              utc(r.Timestamp),
		Properties:             props,
		PreciseTotalAmount:     amount,
		Metadata:               md,
		IngestedAt:             utc(r.IngestedAt),
		CreatedAt:              utc(r.CreatedAt),
		UpdatedAt:              utc(r.UpdatedAt),
	}, nil
}

func (r eventLogRepo) Append(ctx context.Context, org core.OrganizationID, events []*event.Event) (accepted, duplicates []*event.Event, err error) {
	for _, e := range events {
		if e.OrganizationID != org {
			return nil, nil, notFound("event of organization", org)
		}
	}
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, nil, err
	}
	txIDs := make([]string, len(events))
	for i, e := range events {
		txIDs[i] = e.TransactionID
	}
	stored, err := q.ListEventKeys(ctx, db.ListEventKeysParams{OrganizationID: org.UUID(), TransactionIds: txIDs})
	if err != nil {
		return nil, nil, fmt.Errorf("append events: %w", mapErr(err))
	}
	// The deduplication keys, (external subscription, transaction ID).
	type key struct{ sub, tx string }
	seen := make(map[key]bool, len(events)+len(stored))
	for _, k := range stored {
		seen[key{k.ExternalSubscriptionID, k.TransactionID}] = true
	}
	for _, e := range events {
		k := key{e.ExternalSubscriptionID, e.TransactionID}
		if seen[k] {
			duplicates = append(duplicates, e)
			continue
		}
		seen[k] = true
		n, err := q.AppendEvent(ctx, eventParams(e))
		if err != nil {
			return nil, nil, fmt.Errorf("append event %q: %w", e.TransactionID, mapErr(err))
		}
		if n == 0 {
			// A concurrent transaction stored the key first, or the event's
			// ID is taken.
			if _, err := r.Get(ctx, org, e.ExternalSubscriptionID, e.TransactionID); err == nil {
				duplicates = append(duplicates, e)
				continue
			} else if !errors.Is(err, core.ErrNotFound) {
				return nil, nil, err
			}
			return nil, nil, conflict("event", "id")
		}
		accepted = append(accepted, e)
	}
	return accepted, duplicates, nil
}

func (r eventLogRepo) Get(ctx context.Context, org core.OrganizationID, externalSubscriptionID, transactionID string) (*event.Event, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	var row db.Event
	if externalSubscriptionID == "" {
		row, err = q.GetFirstEvent(ctx, db.GetFirstEventParams{OrganizationID: org.UUID(), TransactionID: transactionID})
	} else {
		row, err = q.GetEvent(ctx, db.GetEventParams{
			OrganizationID: org.UUID(), ExternalSubscriptionID: externalSubscriptionID, TransactionID: transactionID,
		})
	}
	if err != nil {
		return nil, getErr("event", transactionID, err)
	}
	return eventOf(row)
}

func (r eventLogRepo) List(ctx context.Context, org core.OrganizationID, f event.ListFilter, page core.PageRequest) (core.Page[*event.Event], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*event.Event]{}, err
	}
	after, afterAt, lim, err := timeCursorArgs(page)
	if err != nil {
		return core.Page[*event.Event]{}, err
	}
	p := db.ListEventsParams{OrganizationID: org.UUID(), After: after, AfterAt: afterAt, Lim: lim}
	if f.Code != "" {
		p.Code = &f.Code
	}
	if f.ExternalSubscriptionID != "" {
		p.ExternalSubscriptionID = &f.ExternalSubscriptionID
	}
	if f.TimestampFrom != nil {
		t := utc(*f.TimestampFrom)
		p.TimestampFrom = &t
	}
	if f.TimestampTo != nil {
		t := utc(*f.TimestampTo)
		p.TimestampTo = &t
	}
	rows, err := q.ListEvents(ctx, p)
	if err != nil {
		return core.Page[*event.Event]{}, fmt.Errorf("list events: %w", mapErr(err))
	}
	return pageOf(rows, page, eventOf, func(e *event.Event) string {
		return core.Cursor{Key: e.Timestamp.UTC().Format(time.RFC3339Nano), ID: e.ID.UUID()}.Encode()
	})
}

func (r eventLogRepo) ValidationErrors(ctx context.Context, org core.OrganizationID, from, to time.Time) (event.ValidationErrors, error) {
	var out event.ValidationErrors
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return out, err
	}
	rows, err := q.ListEventValidationErrors(ctx, db.ListEventValidationErrorsParams{OrganizationID: org.UUID(), FromAt: from.UTC(), ToAt: to.UTC()})
	if err != nil {
		return out, fmt.Errorf("list event validation errors: %w", mapErr(err))
	}
	for _, x := range rows {
		switch x.Kind {
		case "invalid_code":
			out.InvalidCode = append(out.InvalidCode, x.TransactionID)
		case "missing_aggregation_property":
			out.MissingAggregationProperty = append(out.MissingAggregationProperty, x.TransactionID)
		case "invalid_filter_values":
			out.InvalidFilterValues = append(out.InvalidFilterValues, x.TransactionID)
		}
	}
	out.Normalize()
	return out, nil
}
