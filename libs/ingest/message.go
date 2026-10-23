package ingest

import (
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// RawMessage is the message of the events.raw topic: a validated,
// normalized event. Connectors produce it too, with ExpressionEvaluated
// unset, so that the enrichment consumer evaluates the metric's
// expression.
type RawMessage struct {
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID `json:"organization_id"`
	// TransactionID is the event's idempotency key.
	TransactionID string `json:"transaction_id"`
	// Code is the billable metric code.
	Code string `json:"code"`
	// ExternalSubscriptionID is the subscription's external ID.
	ExternalSubscriptionID string `json:"external_subscription_id"`
	// Timestamp is the instant of the usage.
	Timestamp time.Time `json:"timestamp"`
	// Properties are the normalized properties.
	Properties map[string]string `json:"properties,omitempty"`
	// PreciseTotalAmountCents is the precise amount, when sent.
	PreciseTotalAmountCents *money.Decimal `json:"precise_total_amount_cents,omitempty"`
	// IngestedAt is the ingestion instant.
	IngestedAt time.Time `json:"ingested_at"`
	// Source is where the event came from ([SourceAPI] or a connector
	// name).
	Source string `json:"source,omitempty"`
	// ExpressionEvaluated reports that the metric's expression was
	// evaluated at intake.
	ExpressionEvaluated bool `json:"expression_evaluated,omitempty"`
	// Metadata describes the API request.
	Metadata *Metadata `json:"metadata,omitempty"`
}

// Key returns the partition key of m ([PartitionKey]).
func (m *RawMessage) Key() []byte {
	return PartitionKey(m.OrganizationID, m.ExternalSubscriptionID, m.TransactionID)
}

// rawMessage returns the message of an accepted event.
func rawMessage(e *event.Event, source string) RawMessage {
	m := RawMessage{
		OrganizationID:          e.OrganizationID,
		TransactionID:           e.TransactionID,
		Code:                    e.Code,
		ExternalSubscriptionID:  e.ExternalSubscriptionID,
		Timestamp:               e.Timestamp,
		Properties:              maps.Clone(e.Properties),
		PreciseTotalAmountCents: e.PreciseTotalAmount,
		IngestedAt:              e.IngestedAt,
		Source:                  source,
		ExpressionEvaluated:     true,
	}
	if md := e.Metadata; md != (Metadata{}) {
		m.Metadata = &md
	}
	return m
}

// DecodeRaw decodes and validates a message of the events.raw topic. An
// error means the message can never be processed.
func DecodeRaw(b []byte) (*RawMessage, *event.Event, error) {
	var m RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, nil, fmt.Errorf("ingest: decode raw event: %w", err)
	}
	if m.OrganizationID.IsZero() {
		return nil, nil, fmt.Errorf("ingest: raw event %q: %w: organization_id", m.TransactionID, core.ErrInvalidValue)
	}
	e := &event.Event{
		OrganizationID:         m.OrganizationID,
		TransactionID:          m.TransactionID,
		Code:                   m.Code,
		ExternalSubscriptionID: m.ExternalSubscriptionID,
		Timestamp:              m.Timestamp.UTC().Truncate(time.Microsecond),
		Properties:             m.Properties,
		PreciseTotalAmount:     m.PreciseTotalAmountCents,
		IngestedAt:             m.IngestedAt.UTC().Truncate(time.Microsecond),
	}
	if m.Metadata != nil {
		e.Metadata = *m.Metadata
	}
	if e.Properties == nil {
		e.Properties = map[string]string{}
	}
	var v core.Validator
	checkProperties(&v, e.Properties)
	if e.PreciseTotalAmount != nil {
		v.Check(integerDigits(*e.PreciseTotalAmount) <= MaxIntegerDigits, "precise_total_amount_cents", core.CodeOutOfRange)
	}
	v.Merge("", e.Validate())
	if err := v.Err(); err != nil {
		return nil, nil, fmt.Errorf("ingest: raw event %q: %w", m.TransactionID, err)
	}
	return &m, e, nil
}

// DecodeEnriched decodes a message of the events.enriched topic.
func DecodeEnriched(b []byte) (*Enriched, error) {
	var e Enriched
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, fmt.Errorf("ingest: decode enriched event: %w", err)
	}
	return &e, nil
}

// Dead letter error codes ([DeadLetter.ErrorCode]).
const (
	// ErrorDecode: the message is not a valid event.
	ErrorDecode = "decode_event"
	// ErrorExpression: the metric's expression cannot be evaluated.
	ErrorExpression = "evaluate_expression"
	// ErrorCatalog: the catalog could not be read.
	ErrorCatalog = "fetch_catalog"
	// ErrorProduce: the enriched event could not be produced.
	ErrorProduce = "produce_enriched"
	// ErrorWrite: the events could not be written.
	ErrorWrite = "write_events"
	// ErrorInAdvance: in-advance billing failed.
	ErrorInAdvance = "bill_in_advance"
)

// DeadLetter is the message of the dead letter topic: a message that could
// not be processed, with why (the events-processor's FailedEvent).
type DeadLetter struct {
	// Event is the original message.
	Event json.RawMessage `json:"event"`
	// Topic, Partition and Offset locate the original message.
	Topic string `json:"topic"`
	// Partition is the original message's partition.
	Partition int32 `json:"partition"`
	// Offset is the original message's offset.
	Offset int64 `json:"offset"`
	// ErrorCode classifies the failure (ErrorDecode, …).
	ErrorCode string `json:"error_code"`
	// ErrorMessage is the failure's text.
	ErrorMessage string `json:"error_message"`
	// FailedAt is the instant of the failure.
	FailedAt time.Time `json:"failed_at"`
}

// MarshalDeadLetter encodes a dead letter; a message that is not JSON is
// kept as a JSON string.
func MarshalDeadLetter(d DeadLetter) ([]byte, error) {
	if !json.Valid(d.Event) {
		s, err := json.Marshal(string(d.Event))
		if err != nil {
			return nil, fmt.Errorf("ingest: encode dead letter: %w", err)
		}
		d.Event = s
	}
	b, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("ingest: encode dead letter: %w", err)
	}
	return b, nil
}
