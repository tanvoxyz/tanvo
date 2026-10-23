package webhook

import (
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Delivery limits (domain §18.2).
const (
	// MaxAttempts is the number of automatic attempts of a delivery.
	MaxAttempts = 3
	// MaxResponseBytes bounds the stored response body.
	MaxResponseBytes = 64 << 10
	// Retention is how long deliveries are kept.
	Retention = 90 * 24 * time.Hour
)

// Delivery is the sending of one event to one endpoint (domain §18.2).
type Delivery struct {
	// ID identifies the delivery.
	ID core.WebhookID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// EndpointID is the destination.
	EndpointID core.WebhookEndpointID
	// EventID is the domain event delivered; it is the Standard Webhooks
	// message id.
	EventID core.AuditEventID
	// EventType is the type of the event.
	EventType EventType
	// ObjectType names the kind of the object of the event.
	ObjectType string
	// ObjectID is the id of the object of the event.
	ObjectID uuid.UUID
	// Payload is the JSON body sent, stored as sent.
	Payload []byte
	// Status is the lifecycle state.
	Status Status
	// Attempts counts the attempts made, automatic and manual.
	Attempts int
	// LastAttemptAt is the instant of the last attempt.
	LastAttemptAt *time.Time
	// NextAttemptAt is when the next automatic attempt is due (pending and
	// retrying deliveries).
	NextAttemptAt *time.Time
	// HTTPStatus is the status code of the last response, if any.
	HTTPStatus *int
	// Response is the last response body (or a connection error summary),
	// truncated by [TruncateResponse].
	Response *string
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// NewDelivery returns a pending delivery of e to endpoint, due now.
func NewDelivery(id core.WebhookID, endpoint core.WebhookEndpointID, e *Event, now time.Time) *Delivery {
	return &Delivery{
		ID:             id,
		OrganizationID: e.OrganizationID,
		EndpointID:     endpoint,
		EventID:        e.ID,
		EventType:      e.Type,
		ObjectType:     e.ObjectType,
		ObjectID:       e.ObjectID,
		Payload:        append([]byte(nil), e.Payload...),
		Status:         StatusPending,
		NextAttemptAt:  &now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// Validate checks that the delivery is complete.
func (d *Delivery) Validate() error {
	var v core.Validator
	v.Check(!d.EndpointID.IsZero(), "webhook_endpoint_id", core.CodeRequired)
	v.Enum("event_type", d.EventType)
	v.Enum("status", d.Status)
	v.Check(d.Attempts >= 0, "attempts", core.CodeOutOfRange)
	v.Check(len(d.Payload) > 0, "payload", core.CodeRequired)
	return v.Err()
}

var deliveryTransitions = map[Status][]Status{
	StatusPending:  {StatusRetrying, StatusSucceeded, StatusFailed},
	StatusRetrying: {StatusRetrying, StatusSucceeded, StatusFailed, StatusPending},
	StatusFailed:   {StatusPending},
}

// CanTransition reports whether a delivery may go from one status to
// another: pending → retrying | succeeded | failed, retrying → retrying |
// succeeded | failed, and a manual retry from retrying or failed back to
// pending. Succeeded is final.
func CanTransition(from, to Status) bool {
	for _, s := range deliveryTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

func (d *Delivery) to(s Status) error {
	if !CanTransition(d.Status, s) {
		return &core.TransitionError{Entity: "webhook", From: string(d.Status), To: string(s)}
	}
	d.Status = s
	return nil
}

// Succeed records a successful attempt at now with its response.
func (d *Delivery) Succeed(httpStatus int, body []byte, now time.Time) error {
	if err := d.to(StatusSucceeded); err != nil {
		return err
	}
	d.record(&httpStatus, TruncateResponse(body), now)
	d.NextAttemptAt = nil
	return nil
}

// Fail records a failed attempt at now: the delivery is retrying, due
// after [Backoff], while fewer than [MaxAttempts] attempts were made, and
// failed afterwards. httpStatus is nil when no response was received;
// response is the body or a connection error summary. jitter in [0, 1)
// randomizes the backoff.
func (d *Delivery) Fail(httpStatus *int, response string, jitter float64, now time.Time) error {
	next := StatusFailed
	if d.Attempts+1 < MaxAttempts {
		next = StatusRetrying
	}
	if err := d.to(next); err != nil {
		return err
	}
	d.record(httpStatus, TruncateResponse([]byte(response)), now)
	d.NextAttemptAt = nil
	if next == StatusRetrying {
		due := now.Add(Backoff(d.Attempts, jitter))
		d.NextAttemptAt = &due
	}
	return nil
}

// Retry schedules a manual retry at now (lago Webhooks::RetryService). A
// succeeded delivery cannot be retried. The retry is one attempt: if it
// fails after [MaxAttempts] attempts, the delivery fails again at once.
func (d *Delivery) Retry(now time.Time) error {
	if err := d.to(StatusPending); err != nil {
		return err
	}
	d.NextAttemptAt = &now
	d.UpdatedAt = now
	return nil
}

func (d *Delivery) record(httpStatus *int, response string, now time.Time) {
	d.Attempts++
	d.LastAttemptAt = &now
	d.HTTPStatus = httpStatus
	d.Response = &response
	d.UpdatedAt = now
}

// Due reports whether an automatic attempt is due at now.
func (d *Delivery) Due(now time.Time) bool {
	return (d.Status == StatusPending || d.Status == StatusRetrying) && d.NextAttemptAt != nil && !d.NextAttemptAt.After(now)
}

// Backoff returns the wait before the next attempt after attempts failed
// attempts: attempts⁴ + jitter·0.15·attempts⁴ + 2 seconds (the Active Job
// polynomial algorithm lago uses). jitter is clamped to [0, 1].
func Backoff(attempts int, jitter float64) time.Duration {
	jitter = math.Max(0, math.Min(1, jitter))
	p := math.Pow(float64(max(attempts, 0)), 4)
	return time.Duration((p + jitter*p*0.15 + 2) * float64(time.Second))
}

// TruncateResponse returns body as valid UTF-8 (invalid bytes dropped) cut
// to at most [MaxResponseBytes] bytes on a character boundary.
func TruncateResponse(body []byte) string {
	s := strings.ToValidUTF8(string(body), "")
	if len(s) <= MaxResponseBytes {
		return s
	}
	s = s[:MaxResponseBytes]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
