package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindWebhook binds the repositories of the webhook aggregates.
func (t *txState) bindWebhook(r *port.Repos) {
	r.WebhookEndpoints = webhookEndpointRepo{t}
	r.WebhookDeliveries = webhookDeliveryRepo{t}
	r.DomainEvents = domainEventRepo{t}
}

// webhookEventTypes converts the event types of an endpoint to a text
// array, keeping nil (every type, a NULL column) apart from an empty list
// (no type).
func webhookEventTypes(ts []webhook.EventType) []string {
	if ts == nil {
		return nil
	}
	return enumStrings(ts)
}

// webhookEventTypesOf is the inverse of webhookEventTypes.
func webhookEventTypesOf(ss []string) []webhook.EventType {
	if ss == nil {
		return nil
	}
	out := make([]webhook.EventType, len(ss))
	for i, s := range ss {
		out[i] = webhook.EventType(s)
	}
	return out
}

// webhookEndpointRepo implements webhook.EndpointRepository.
type webhookEndpointRepo struct{ t *txState }

var _ webhook.EndpointRepository = webhookEndpointRepo{}

func webhookEndpointParams(e *webhook.Endpoint) db.CreateWebhookEndpointParams {
	return db.CreateWebhookEndpointParams{
		OrganizationID: e.OrganizationID.UUID(),
		ID:             e.ID.UUID(),
		Url:            e.URL,
		Name:           e.Name,
		EventTypes:     webhookEventTypes(e.EventTypes),
		Secret:         e.Secret,
		CreatedAt:      utc(e.CreatedAt),
		UpdatedAt:      utc(e.UpdatedAt),
	}
}

func webhookEndpointOf(r db.WebhookEndpoint) (*webhook.Endpoint, error) {
	return &webhook.Endpoint{
		ID:             idOf[core.WebhookEndpointID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		URL:            r.Url,
		Name:           r.Name,
		EventTypes:     webhookEventTypesOf(r.EventTypes),
		Secret:         r.Secret,
		CreatedAt:      utc(r.CreatedAt),
		UpdatedAt:      utc(r.UpdatedAt),
	}, nil
}

func (r webhookEndpointRepo) Create(ctx context.Context, e *webhook.Endpoint) error {
	q, err := r.t.queries(ctx, e.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateWebhookEndpoint(ctx, webhookEndpointParams(e))
	return insertErr("webhook_endpoint", n, err)
}

func (r webhookEndpointRepo) Update(ctx context.Context, e *webhook.Endpoint) error {
	q, err := r.t.queries(ctx, e.OrganizationID)
	if err != nil {
		return err
	}
	// The URL is unique per organization.
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateWebhookEndpoint(ctx, db.UpdateWebhookEndpointParams(webhookEndpointParams(e)))
		return err
	})
	return updateErr("webhook_endpoint", e.ID, n, err)
}

func (r webhookEndpointRepo) Get(ctx context.Context, org core.OrganizationID, id core.WebhookEndpointID) (*webhook.Endpoint, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetWebhookEndpoint(ctx, db.GetWebhookEndpointParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("webhook_endpoint", id, err)
	}
	return webhookEndpointOf(row)
}

func (r webhookEndpointRepo) List(ctx context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*webhook.Endpoint], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*webhook.Endpoint]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*webhook.Endpoint]{}, err
	}
	rows, err := q.ListWebhookEndpoints(ctx, db.ListWebhookEndpointsParams{OrganizationID: org.UUID(), After: after, Lim: lim})
	if err != nil {
		return core.Page[*webhook.Endpoint]{}, fmt.Errorf("list webhook endpoints: %w", mapErr(err))
	}
	return pageOf(rows, page, webhookEndpointOf, func(e *webhook.Endpoint) string { return e.ID.String() })
}

func (r webhookEndpointRepo) ListSubscribed(ctx context.Context, org core.OrganizationID, t webhook.EventType) ([]*webhook.Endpoint, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListSubscribedWebhookEndpoints(ctx, db.ListSubscribedWebhookEndpointsParams{OrganizationID: org.UUID(), EventType: string(t)})
	if err != nil {
		return nil, fmt.Errorf("list webhook endpoints: %w", mapErr(err))
	}
	return convAll(rows, webhookEndpointOf)
}

func (r webhookEndpointRepo) Count(ctx context.Context, org core.OrganizationID) (int, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	n, err := q.CountWebhookEndpoints(ctx, org.UUID())
	if err != nil {
		return 0, fmt.Errorf("count webhook endpoints: %w", mapErr(err))
	}
	return int(n), nil
}

// Delete removes the endpoint; its deliveries go with it (ON DELETE
// CASCADE).
func (r webhookEndpointRepo) Delete(ctx context.Context, org core.OrganizationID, id core.WebhookEndpointID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	n, err := q.DeleteWebhookEndpoint(ctx, db.DeleteWebhookEndpointParams{OrganizationID: org.UUID(), ID: id.UUID()})
	return updateErr("webhook_endpoint", id, n, err)
}

// webhookDeliveryRepo implements webhook.DeliveryRepository.
type webhookDeliveryRepo struct{ t *txState }

var _ webhook.DeliveryRepository = webhookDeliveryRepo{}

func webhookDeliveryParams(d *webhook.Delivery) db.CreateWebhookDeliveryParams {
	return db.CreateWebhookDeliveryParams{
		OrganizationID:    d.OrganizationID.UUID(),
		ID:                d.ID.UUID(),
		WebhookEndpointID: d.EndpointID.UUID(),
		EventID:           d.EventID.UUID(),
		EventType:         string(d.EventType),
		ObjectType:        d.ObjectType,
		ObjectID:          d.ObjectID,
		Payload:           d.Payload,
		Status:            string(d.Status),
		Attempts:          int32(d.Attempts), // at most a few attempts
		LastAttemptAt:     optUTC(d.LastAttemptAt),
		NextAttemptAt:     optUTC(d.NextAttemptAt),
		HttpStatus:        optInt32(d.HTTPStatus),
		Response:          d.Response,
		CreatedAt:         utc(d.CreatedAt),
		UpdatedAt:         utc(d.UpdatedAt),
	}
}

func webhookDeliveryOf(r db.WebhookDelivery) (*webhook.Delivery, error) {
	return &webhook.Delivery{
		ID:             idOf[core.WebhookID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		EndpointID:     idOf[core.WebhookEndpointID](r.WebhookEndpointID),
		EventID:        idOf[core.AuditEventID](r.EventID),
		EventType:      webhook.EventType(r.EventType),
		ObjectType:     r.ObjectType,
		ObjectID:       r.ObjectID,
		Payload:        r.Payload,
		Status:         webhook.Status(r.Status),
		Attempts:       int(r.Attempts),
		LastAttemptAt:  optUTC(r.LastAttemptAt),
		NextAttemptAt:  optUTC(r.NextAttemptAt),
		HTTPStatus:     optInt(r.HttpStatus),
		Response:       r.Response,
		CreatedAt:      utc(r.CreatedAt),
		UpdatedAt:      utc(r.UpdatedAt),
	}, nil
}

func (r webhookDeliveryRepo) Create(ctx context.Context, d *webhook.Delivery) error {
	q, err := r.t.queries(ctx, d.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateWebhookDelivery(ctx, webhookDeliveryParams(d))
	return insertErr("webhook", n, err)
}

func (r webhookDeliveryRepo) Update(ctx context.Context, d *webhook.Delivery) error {
	q, err := r.t.queries(ctx, d.OrganizationID)
	if err != nil {
		return err
	}
	// Guarded: moving a delivery to an endpoint that does not exist is a
	// foreign key violation.
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateWebhookDelivery(ctx, db.UpdateWebhookDeliveryParams(webhookDeliveryParams(d)))
		return err
	})
	return updateErr("webhook", d.ID, n, err)
}

func (r webhookDeliveryRepo) Get(ctx context.Context, org core.OrganizationID, id core.WebhookID) (*webhook.Delivery, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetWebhookDelivery(ctx, db.GetWebhookDeliveryParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("webhook", id, err)
	}
	return webhookDeliveryOf(row)
}

func (r webhookDeliveryRepo) ListByEndpoint(ctx context.Context, org core.OrganizationID, endpoint core.WebhookEndpointID, f webhook.DeliveryFilter, page core.PageRequest) (core.Page[*webhook.Delivery], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*webhook.Delivery]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*webhook.Delivery]{}, err
	}
	rows, err := q.ListWebhookDeliveriesByEndpoint(ctx, db.ListWebhookDeliveriesByEndpointParams{
		OrganizationID:    org.UUID(),
		WebhookEndpointID: endpoint.UUID(),
		Statuses:          enumStrings(f.Statuses),
		EventTypes:        enumStrings(f.EventTypes),
		After:             after,
		Lim:               lim,
	})
	if err != nil {
		return core.Page[*webhook.Delivery]{}, fmt.Errorf("list webhook deliveries: %w", mapErr(err))
	}
	return pageOf(rows, page, webhookDeliveryOf, func(d *webhook.Delivery) string { return d.ID.String() })
}

// ListDue is not organization-scoped: it feeds the dispatcher sweep.
func (r webhookDeliveryRepo) ListDue(ctx context.Context, now time.Time, limit int) ([]*webhook.Delivery, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return nil, err
	}
	t := utc(now)
	var lim *int32
	if limit > 0 {
		lim = optInt32(&limit)
	}
	rows, err := q.ListDueWebhookDeliveries(ctx, db.ListDueWebhookDeliveriesParams{Now: &t, Lim: lim})
	if err != nil {
		return nil, fmt.Errorf("list due webhook deliveries: %w", mapErr(err))
	}
	return convAll(rows, webhookDeliveryOf)
}

// PurgeBefore is not organization-scoped: it enforces the retention of
// every organization.
func (r webhookDeliveryRepo) PurgeBefore(ctx context.Context, cutoff time.Time) (int, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return 0, err
	}
	n, err := q.PurgeWebhookDeliveries(ctx, utc(cutoff))
	if err != nil {
		return 0, fmt.Errorf("purge webhook deliveries: %w", mapErr(err))
	}
	return int(n), nil
}

// domainEventRepo implements webhook.EventRepository.
type domainEventRepo struct{ t *txState }

var _ webhook.EventRepository = domainEventRepo{}

func domainEventOf(r db.DomainEvent) (*webhook.Event, error) {
	return &webhook.Event{
		ID:             idOf[core.AuditEventID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		Type:           webhook.EventType(r.EventType),
		ObjectType:     r.ObjectType,
		ObjectID:       r.ObjectID,
		Payload:        json.RawMessage(r.Payload),
		OccurredAt:     utc(r.OccurredAt),
	}, nil
}

func (r domainEventRepo) Record(ctx context.Context, e *webhook.Event) error {
	q, err := r.t.queries(ctx, e.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.RecordDomainEvent(ctx, db.RecordDomainEventParams{
		OrganizationID: e.OrganizationID.UUID(),
		ID:             e.ID.UUID(),
		EventType:      string(e.Type),
		ObjectType:     e.ObjectType,
		ObjectID:       e.ObjectID,
		Payload:        e.Payload,
		OccurredAt:     utc(e.OccurredAt),
	})
	return insertErr("domain_event", n, err)
}

func (r domainEventRepo) Get(ctx context.Context, org core.OrganizationID, id core.AuditEventID) (*webhook.Event, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetDomainEvent(ctx, db.GetDomainEventParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("domain_event", id, err)
	}
	return domainEventOf(row)
}

func (r domainEventRepo) ListByObject(ctx context.Context, org core.OrganizationID, objectType string, objectID uuid.UUID, page core.PageRequest) (core.Page[*webhook.Event], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*webhook.Event]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*webhook.Event]{}, err
	}
	rows, err := q.ListDomainEventsByObject(ctx, db.ListDomainEventsByObjectParams{
		OrganizationID: org.UUID(), ObjectType: objectType, ObjectID: objectID, After: after, Lim: lim,
	})
	if err != nil {
		return core.Page[*webhook.Event]{}, fmt.Errorf("list domain events: %w", mapErr(err))
	}
	return pageOf(rows, page, domainEventOf, func(e *webhook.Event) string { return e.ID.String() })
}
