package webhook

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Event is a domain event (domain §2.3): one state change of one object,
// recorded in the transaction that made it. Webhook deliveries and activity
// logs are derived from events.
type Event struct {
	// ID identifies the event; it is also the Standard Webhooks message id
	// (webhook-id) of every delivery of the event.
	ID core.AuditEventID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Type is the event type.
	Type EventType
	// ObjectType names the kind of the changed object ("invoice",
	// "customer", …).
	ObjectType string
	// ObjectID is the id of the changed object.
	ObjectID uuid.UUID
	// Payload is the API representation of the object (domain §18.3).
	Payload json.RawMessage
	// OccurredAt is the instant of the change.
	OccurredAt time.Time
}

// Validate checks that the event is complete.
func (e *Event) Validate() error {
	var v core.Validator
	v.Enum("type", e.Type)
	v.Required("object_type", e.ObjectType)
	v.Check(e.ObjectID != uuid.Nil, "object_id", core.CodeRequired)
	v.Check(len(e.Payload) > 0 && json.Valid(e.Payload), "payload", core.CodeInvalidFormat)
	v.Check(!e.OccurredAt.IsZero(), "occurred_at", core.CodeRequired)
	return v.Err()
}
