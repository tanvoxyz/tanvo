package webhook

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// EndpointRepository stores webhook endpoints.
type EndpointRepository interface {
	// Create inserts e; a URL already used in the organization fails with
	// core.ErrConflict. The caller checks [CheckEndpointLimit] with Count
	// in the same transaction.
	Create(ctx context.Context, e *Endpoint) error
	// Update replaces the stored endpoint e.ID with e (same uniqueness
	// rule as Create).
	Update(ctx context.Context, e *Endpoint) error
	// Get returns the endpoint id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.WebhookEndpointID) (*Endpoint, error)
	// List returns the organization's endpoints in creation order.
	List(ctx context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*Endpoint], error)
	// ListSubscribed returns the endpoints that receive events of type t
	// ([Endpoint.Subscribes]), in creation order.
	ListSubscribed(ctx context.Context, org core.OrganizationID, t EventType) ([]*Endpoint, error)
	// Count returns the number of endpoints of the organization.
	Count(ctx context.Context, org core.OrganizationID) (int, error)
	// Delete removes the endpoint id and its deliveries, or fails with
	// core.ErrNotFound.
	Delete(ctx context.Context, org core.OrganizationID, id core.WebhookEndpointID) error
}

// DeliveryFilter narrows [DeliveryRepository.ListByEndpoint].
type DeliveryFilter struct {
	// Statuses keeps deliveries in one of these statuses; empty keeps all.
	Statuses []Status
	// EventTypes keeps deliveries of these event types; empty keeps all.
	EventTypes []EventType
}

// DeliveryRepository stores webhook deliveries.
type DeliveryRepository interface {
	// Create inserts d.
	Create(ctx context.Context, d *Delivery) error
	// Update replaces the stored delivery d.ID with d.
	Update(ctx context.Context, d *Delivery) error
	// Get returns the delivery id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.WebhookID) (*Delivery, error)
	// ListByEndpoint returns the deliveries of an endpoint in creation order.
	ListByEndpoint(ctx context.Context, org core.OrganizationID, endpoint core.WebhookEndpointID, f DeliveryFilter, page core.PageRequest) (core.Page[*Delivery], error)
	// ListDue returns up to limit pending or retrying deliveries of every
	// organization whose next attempt is due at now ([Delivery.Due]),
	// earliest due first; it feeds the dispatcher sweep.
	ListDue(ctx context.Context, now time.Time, limit int) ([]*Delivery, error)
	// PurgeBefore deletes the deliveries of every organization created
	// before cutoff (now − [Retention]) and returns how many.
	PurgeBefore(ctx context.Context, cutoff time.Time) (int, error)
}

// EventRepository stores domain events.
type EventRepository interface {
	// Record inserts e; an id already recorded fails with
	// core.ErrConflict.
	Record(ctx context.Context, e *Event) error
	// Get returns the event id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.AuditEventID) (*Event, error)
	// ListByObject returns the events of one object in occurrence order.
	ListByObject(ctx context.Context, org core.OrganizationID, objectType string, objectID uuid.UUID, page core.PageRequest) (core.Page[*Event], error)
}
