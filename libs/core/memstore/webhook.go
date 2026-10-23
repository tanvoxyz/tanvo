package memstore

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// webhookData holds the tables of package webhook.
type webhookData struct {
	endpoints  *table[core.WebhookEndpointID, webhook.Endpoint]
	deliveries *table[core.WebhookID, webhook.Delivery]
	events     *table[core.AuditEventID, webhook.Event]
}

func newWebhookData() *webhookData {
	return &webhookData{
		endpoints: newTable[core.WebhookEndpointID](func(e webhook.Endpoint) webhook.Endpoint {
			e.EventTypes = slices.Clone(e.EventTypes)
			return e
		}),
		deliveries: newTable[core.WebhookID](func(d webhook.Delivery) webhook.Delivery {
			d.Payload = slices.Clone(d.Payload)
			return d
		}),
		events: newTable[core.AuditEventID](func(e webhook.Event) webhook.Event {
			e.Payload = slices.Clone(e.Payload)
			return e
		}),
	}
}

func (d *webhookData) snapshot() *webhookData {
	return &webhookData{
		endpoints:  d.endpoints.snapshot(),
		deliveries: d.deliveries.snapshot(),
		events:     d.events.snapshot(),
	}
}

// webhookEndpointRepo implements webhook.EndpointRepository.
type webhookEndpointRepo struct{ d *webhookData }

func (r webhookEndpointRepo) urlTaken(e *webhook.Endpoint) bool {
	return r.d.endpoints.any(func(x *webhook.Endpoint) bool {
		return x.ID != e.ID && x.OrganizationID == e.OrganizationID && x.URL == e.URL
	})
}

func (r webhookEndpointRepo) Create(_ context.Context, e *webhook.Endpoint) error {
	if r.d.endpoints.has(e.ID) {
		return conflict("webhook_endpoint", "id")
	}
	if r.urlTaken(e) {
		return conflict("webhook_endpoint", "url")
	}
	r.d.endpoints.put(e.ID, *e)
	return nil
}

func (r webhookEndpointRepo) Update(_ context.Context, e *webhook.Endpoint) error {
	cur, ok := r.d.endpoints.get(e.ID)
	if !ok || cur.OrganizationID != e.OrganizationID {
		return notFound("webhook_endpoint", e.ID)
	}
	if r.urlTaken(e) {
		return conflict("webhook_endpoint", "url")
	}
	r.d.endpoints.put(e.ID, *e)
	return nil
}

func (r webhookEndpointRepo) Get(_ context.Context, org core.OrganizationID, id core.WebhookEndpointID) (*webhook.Endpoint, error) {
	e, ok := r.d.endpoints.get(id)
	if !ok || e.OrganizationID != org {
		return nil, notFound("webhook_endpoint", id)
	}
	return e, nil
}

func (r webhookEndpointRepo) List(_ context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*webhook.Endpoint], error) {
	rows := r.d.endpoints.find(func(x *webhook.Endpoint) bool { return x.OrganizationID == org })
	return paginate(rows, func(x *webhook.Endpoint) core.WebhookEndpointID { return x.ID }, page)
}

func (r webhookEndpointRepo) ListSubscribed(_ context.Context, org core.OrganizationID, t webhook.EventType) ([]*webhook.Endpoint, error) {
	return r.d.endpoints.find(func(x *webhook.Endpoint) bool { return x.OrganizationID == org && x.Subscribes(t) }), nil
}

func (r webhookEndpointRepo) Count(_ context.Context, org core.OrganizationID) (int, error) {
	return r.d.endpoints.count(func(x *webhook.Endpoint) bool { return x.OrganizationID == org }), nil
}

func (r webhookEndpointRepo) Delete(_ context.Context, org core.OrganizationID, id core.WebhookEndpointID) error {
	e, ok := r.d.endpoints.get(id)
	if !ok || e.OrganizationID != org {
		return notFound("webhook_endpoint", id)
	}
	r.d.endpoints.del(id)
	r.d.deliveries.deleteWhere(func(x *webhook.Delivery) bool { return x.EndpointID == id })
	return nil
}

func (r webhookEndpointRepo) Organizations(context.Context) ([]core.OrganizationID, error) {
	seen := map[core.OrganizationID]bool{}
	var out []core.OrganizationID
	for _, e := range r.d.endpoints.find(func(*webhook.Endpoint) bool { return true }) {
		if !seen[e.OrganizationID] {
			seen[e.OrganizationID] = true
			out = append(out, e.OrganizationID)
		}
	}
	slices.SortFunc(out, core.OrganizationID.Compare)
	return out, nil
}

// webhookDeliveryRepo implements webhook.DeliveryRepository.
type webhookDeliveryRepo struct{ d *webhookData }

func (r webhookDeliveryRepo) Create(_ context.Context, d *webhook.Delivery) error {
	if r.d.deliveries.has(d.ID) {
		return conflict("webhook", "id")
	}
	r.d.deliveries.put(d.ID, *d)
	return nil
}

func (r webhookDeliveryRepo) Update(_ context.Context, d *webhook.Delivery) error {
	cur, ok := r.d.deliveries.get(d.ID)
	if !ok || cur.OrganizationID != d.OrganizationID {
		return notFound("webhook", d.ID)
	}
	r.d.deliveries.put(d.ID, *d)
	return nil
}

func (r webhookDeliveryRepo) Get(_ context.Context, org core.OrganizationID, id core.WebhookID) (*webhook.Delivery, error) {
	d, ok := r.d.deliveries.get(id)
	if !ok || d.OrganizationID != org {
		return nil, notFound("webhook", id)
	}
	return d, nil
}

func (r webhookDeliveryRepo) ListByEndpoint(_ context.Context, org core.OrganizationID, endpoint core.WebhookEndpointID, f webhook.DeliveryFilter, page core.PageRequest) (core.Page[*webhook.Delivery], error) {
	rows := r.d.deliveries.find(func(x *webhook.Delivery) bool {
		return x.OrganizationID == org && x.EndpointID == endpoint &&
			(len(f.Statuses) == 0 || slices.Contains(f.Statuses, x.Status)) &&
			(len(f.EventTypes) == 0 || slices.Contains(f.EventTypes, x.EventType))
	})
	return paginate(rows, func(x *webhook.Delivery) core.WebhookID { return x.ID }, page)
}

func (r webhookDeliveryRepo) ListDue(_ context.Context, now time.Time, limit int) ([]*webhook.Delivery, error) {
	rows := r.d.deliveries.find(func(x *webhook.Delivery) bool { return x.Due(now) })
	slices.SortStableFunc(rows, func(a, b *webhook.Delivery) int { return a.NextAttemptAt.Compare(*b.NextAttemptAt) })
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (r webhookDeliveryRepo) PurgeBefore(_ context.Context, org core.OrganizationID, cutoff time.Time, limit int) (int, error) {
	n := 0
	for _, x := range r.d.deliveries.find(func(x *webhook.Delivery) bool { return x.OrganizationID == org && x.UpdatedAt.Before(cutoff) }) {
		if limit > 0 && n >= limit {
			break
		}
		r.d.deliveries.del(x.ID)
		n++
	}
	return n, nil
}

// domainEventRepo implements webhook.EventRepository.
type domainEventRepo struct{ d *webhookData }

func (r domainEventRepo) Record(_ context.Context, e *webhook.Event) error {
	if r.d.events.has(e.ID) {
		return conflict("domain_event", "id")
	}
	r.d.events.put(e.ID, *e)
	return nil
}

func (r domainEventRepo) Get(_ context.Context, org core.OrganizationID, id core.AuditEventID) (*webhook.Event, error) {
	e, ok := r.d.events.get(id)
	if !ok || e.OrganizationID != org {
		return nil, notFound("domain_event", id)
	}
	return e, nil
}

func (r domainEventRepo) ListByObject(_ context.Context, org core.OrganizationID, objectType string, objectID uuid.UUID, page core.PageRequest) (core.Page[*webhook.Event], error) {
	rows := r.d.events.find(func(x *webhook.Event) bool {
		return x.OrganizationID == org && x.ObjectType == objectType && x.ObjectID == objectID
	})
	return paginate(rows, func(x *webhook.Event) core.AuditEventID { return x.ID }, page)
}

var (
	_ webhook.EndpointRepository = webhookEndpointRepo{}
	_ webhook.DeliveryRepository = webhookDeliveryRepo{}
	_ webhook.EventRepository    = domainEventRepo{}
)
