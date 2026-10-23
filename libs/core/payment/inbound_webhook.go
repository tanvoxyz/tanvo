package payment

import (
	"encoding/json"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Inbound webhook processing policy (domain §15.6).
const (
	// WebhookRetryInterval is the delay between processing attempts.
	WebhookRetryInterval = 15 * time.Minute
	// WebhookRetryWindow is how long after reception attempts are made.
	WebhookRetryWindow = 2 * time.Hour
	// WebhookRetention is how long webhooks are kept after their last
	// change.
	WebhookRetention = 90 * 24 * time.Hour
)

// InboundWebhook is a webhook received from a payment provider, stored
// before processing (domain §15.6).
type InboundWebhook struct {
	// ID identifies the webhook.
	ID core.InboundWebhookID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Source is the provider that sent it, such as "stripe"; required.
	Source string
	// IntegrationID is the receiving provider connection.
	IntegrationID core.IntegrationID
	// Code is the receiving connection's code.
	Code *string
	// EventType is the provider event type; required.
	EventType string
	// Payload is the raw body; required.
	Payload json.RawMessage
	// Signature is the provider signature header.
	Signature *string
	// Status is the processing status.
	Status InboundWebhookStatus
	// Attempts counts the processing attempts.
	Attempts int
	// ProcessingAt is when the last attempt started.
	ProcessingAt *time.Time
	// CreatedAt is the reception instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// NewInboundWebhook returns a pending webhook received at now.
func NewInboundWebhook(org core.OrganizationID, id core.InboundWebhookID, source, eventType string, payload json.RawMessage, now time.Time) *InboundWebhook {
	return &InboundWebhook{
		ID:             id,
		OrganizationID: org,
		Source:         source,
		EventType:      eventType,
		Payload:        payload,
		Status:         InboundWebhookStatusPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// Validate checks that source, event type, payload and status are present.
func (w *InboundWebhook) Validate() error {
	var v core.Validator
	v.Required("event_type", w.EventType)
	v.Check(len(w.Payload) > 0, "payload", core.CodeRequired)
	v.Required("source", w.Source)
	v.Enum("status", w.Status)
	return v.Err()
}

// CanWebhookTransition reports whether a webhook may go from one status to
// another: pending → processing → succeeded or failed; a stuck processing
// or failed webhook goes back to processing on retry.
func CanWebhookTransition(from, to InboundWebhookStatus) bool {
	switch from {
	case InboundWebhookStatusPending, InboundWebhookStatusFailed:
		return to == InboundWebhookStatusProcessing
	case InboundWebhookStatusProcessing:
		return to == InboundWebhookStatusProcessing || to == InboundWebhookStatusSucceeded || to == InboundWebhookStatusFailed
	default:
		return false
	}
}

// Transition moves the webhook to status to at now; entering processing
// records the attempt.
func (w *InboundWebhook) Transition(to InboundWebhookStatus, now time.Time) error {
	if !CanWebhookTransition(w.Status, to) {
		return &core.TransitionError{Entity: "inbound_webhook", From: string(w.Status), To: string(to)}
	}
	w.Status = to
	if to == InboundWebhookStatusProcessing {
		w.ProcessingAt = &now
		w.Attempts++
	}
	w.UpdatedAt = now
	return nil
}

// NextRetry returns when the webhook should next be processed and whether
// it should be at all: a pending, failed or stuck processing webhook is
// retried [WebhookRetryInterval] after its last attempt (or its reception)
// as long as that falls within [WebhookRetryWindow] of its reception.
func (w *InboundWebhook) NextRetry() (time.Time, bool) {
	if w.Status == InboundWebhookStatusSucceeded {
		return time.Time{}, false
	}
	last := w.CreatedAt
	if w.ProcessingAt != nil {
		last = *w.ProcessingAt
	}
	next := last.Add(WebhookRetryInterval)
	if w.Status == InboundWebhookStatusPending && w.ProcessingAt == nil {
		next = w.CreatedAt
	}
	if next.After(w.CreatedAt.Add(WebhookRetryWindow)) {
		return time.Time{}, false
	}
	return next, true
}

// Retriable reports whether the webhook is due for processing at now.
func (w *InboundWebhook) Retriable(now time.Time) bool {
	next, ok := w.NextRetry()
	return ok && !next.After(now)
}

// Expired reports whether the webhook is past its retention at now and can
// be deleted.
func (w *InboundWebhook) Expired(now time.Time) bool {
	return !w.UpdatedAt.Add(WebhookRetention).After(now)
}
