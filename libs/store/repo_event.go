package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindEvent binds the repositories of the event aggregates.
func (t *txState) bindEvent(r *port.Repos) {
	r.Events = eventLogRepo{t}
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

func eventParams(e *event.Event) db.AppendEventParams {
	p := db.AppendEventParams{
		OrganizationID:         e.OrganizationID.UUID(),
		ID:                     e.ID.UUID(),
		TransactionID:          e.TransactionID,
		Code:                   e.Code,
		ExternalSubscriptionID: e.ExternalSubscriptionID,
		Timestamp:              utc(e.Timestamp),
		Properties:             eventStrings(e.Properties),
		AttributionLabels:      eventStrings(e.AttributionLabels),
		IngestedAt:             utc(e.IngestedAt),
		CreatedAt:              utc(e.CreatedAt),
		UpdatedAt:              utc(e.UpdatedAt),
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
	labels, err := eventStringsOf(r.AttributionLabels)
	if err != nil {
		return nil, fmt.Errorf("event %s: attribution labels: %w", r.ID, err)
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
		AttributionLabels:      labels,
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
	stored, err := q.ListEventTransactionIDs(ctx, db.ListEventTransactionIDsParams{OrganizationID: org.UUID(), TransactionIds: txIDs})
	if err != nil {
		return nil, nil, fmt.Errorf("append events: %w", mapErr(err))
	}
	seen := make(map[string]bool, len(events)+len(stored))
	for _, id := range stored {
		seen[id] = true
	}
	for _, e := range events {
		if seen[e.TransactionID] {
			duplicates = append(duplicates, e)
			continue
		}
		seen[e.TransactionID] = true
		n, err := q.AppendEvent(ctx, eventParams(e))
		if err != nil {
			return nil, nil, fmt.Errorf("append event %q: %w", e.TransactionID, mapErr(err))
		}
		if n == 0 {
			// A concurrent transaction stored the transaction ID first, or
			// the event's ID is taken.
			if _, err := r.Get(ctx, org, e.TransactionID); err == nil {
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

func (r eventLogRepo) Get(ctx context.Context, org core.OrganizationID, transactionID string) (*event.Event, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetEvent(ctx, db.GetEventParams{OrganizationID: org.UUID(), TransactionID: transactionID})
	if err != nil {
		return nil, getErr("event", transactionID, err)
	}
	return eventOf(row)
}

func (r eventLogRepo) Replace(ctx context.Context, e *event.Event) error {
	q, err := r.t.queries(ctx, e.OrganizationID)
	if err != nil {
		return err
	}
	p := eventParams(e)
	n, err := q.ReplaceEvent(ctx, db.ReplaceEventParams{
		Code:                   p.Code,
		ExternalSubscriptionID: p.ExternalSubscriptionID,
		Timestamp:              p.Timestamp,
		Properties:             p.Properties,
		PreciseTotalAmount:     p.PreciseTotalAmount,
		AttributionLabels:      p.AttributionLabels,
		UpdatedAt:              p.UpdatedAt,
		OrganizationID:         p.OrganizationID,
		TransactionID:          p.TransactionID,
	})
	return updateErr("event", e.TransactionID, n, err)
}
