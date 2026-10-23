package event

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// MaxBatchSize bounds the events of one ingestion batch.
const MaxBatchSize = 100

// Field error codes specific to events.
const (
	// CodeInvalidTimestamp: the timestamp is neither RFC 3339 nor Unix
	// seconds.
	CodeInvalidTimestamp = "invalid_timestamp"
	// CodeInvalidProperties: a property is not a string, number or bool.
	CodeInvalidProperties = "invalid_properties"
	// CodeTooManyEvents: a batch holds more than [MaxBatchSize] events.
	CodeTooManyEvents = "too_many_events"
)

// OperationTypeProperty is the property naming an [OperationType] for
// unique count metrics: "remove" retracts a previously added value.
const OperationTypeProperty = "operation_type"

// Event is one usage event (domain §10.1).
type Event struct {
	// ID identifies the stored event.
	ID core.EventID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// TransactionID is the client's idempotency key, unique per
	// organization (≤ 255).
	TransactionID string
	// Code is the billable metric code (≤ 255).
	Code string
	// ExternalSubscriptionID is the external ID of the subscription
	// (≤ 255).
	ExternalSubscriptionID string
	// Timestamp is the instant of the usage; the ingestion time by
	// default.
	Timestamp time.Time
	// Properties are the event properties; numbers are normalized to
	// decimal strings (see [NormalizeProperties]).
	Properties map[string]string
	// PreciseTotalAmount is the amount of the event for dynamic charges.
	PreciseTotalAmount *money.Decimal
	// AttributionLabels are optional labels usage queries can group by
	// (domain §24 item 5).
	AttributionLabels map[string]string
	// IngestedAt is the ingestion instant.
	IngestedAt time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change (a replacement).
	UpdatedAt time.Time
}

// Validate checks the event's invariants.
func (e *Event) Validate() error {
	var v core.Validator
	v.String("transaction_id", e.TransactionID, 255)
	v.String("code", e.Code, 255)
	v.String("external_subscription_id", e.ExternalSubscriptionID, 255)
	v.Check(!e.Timestamp.IsZero(), "timestamp", core.CodeRequired)
	if op, ok := e.Properties[OperationTypeProperty]; ok {
		v.Check(OperationType(op).Valid(), "properties."+OperationTypeProperty, core.CodeInvalid)
	}
	for k := range e.AttributionLabels {
		if !v.Check(k != "", "attribution_labels", core.CodeInvalid) {
			break
		}
	}
	return v.Err()
}

// Operation returns the event's operation type, add by default.
func (e *Event) Operation() OperationType {
	if op := OperationType(e.Properties[OperationTypeProperty]); op.Valid() {
		return op
	}
	return OperationTypeAdd
}

// Clone returns a deep copy of e.
func (e *Event) Clone() *Event {
	c := *e
	c.Properties = maps.Clone(e.Properties)
	c.AttributionLabels = maps.Clone(e.AttributionLabels)
	if e.PreciseTotalAmount != nil {
		d := *e.PreciseTotalAmount
		c.PreciseTotalAmount = &d
	}
	return &c
}

// ParseTimestamp parses an event timestamp: an RFC 3339 string or Unix
// seconds with an optional fraction (as a string, a JSON number, an
// integer or a float). It returns the instant in UTC with microsecond
// precision.
func ParseTimestamp(v any) (time.Time, error) {
	var s string
	switch x := v.(type) {
	case string:
		s = strings.TrimSpace(x)
	case json.Number:
		s = x.String()
	case int:
		return time.Unix(int64(x), 0).UTC(), nil
	case int64:
		return time.Unix(x, 0).UTC(), nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return time.Time{}, fmt.Errorf("%w: timestamp", core.ErrInvalidValue)
		}
		s = strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return time.Time{}, fmt.Errorf("%w: timestamp of type %T", core.ErrInvalidValue, v)
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC().Truncate(time.Microsecond), nil
	}
	return parseUnix(s)
}

func parseUnix(s string) (time.Time, error) {
	sec, frac, _ := strings.Cut(s, ".")
	n, err := strconv.ParseInt(sec, 10, 64)
	if err != nil || len(frac) > 9 || strings.Trim(frac, "0123456789") != "" {
		return time.Time{}, fmt.Errorf("%w: timestamp %q", core.ErrInvalidValue, s)
	}
	var nanos int64
	if frac != "" {
		nanos, _ = strconv.ParseInt(frac+strings.Repeat("0", 9-len(frac)), 10, 64)
		if strings.HasPrefix(sec, "-") {
			nanos = -nanos
		}
	}
	return time.Unix(n, nanos).UTC().Truncate(time.Microsecond), nil
}

// NormalizeProperties converts decoded JSON properties to strings: strings
// are kept, numbers (json.Number, floats, integers) become plain decimal
// strings, booleans become "true"/"false" and nulls are dropped. Nested
// objects and arrays are refused with [core.ErrInvalidValue].
func NormalizeProperties(in map[string]any) (map[string]string, error) {
	if in == nil {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		var s string
		switch x := v.(type) {
		case nil:
			continue
		case string:
			s = x
		case bool:
			s = strconv.FormatBool(x)
		case json.Number:
			d, err := money.Parse(x.String())
			if err != nil {
				return nil, fmt.Errorf("%w: property %q: %w", core.ErrInvalidValue, k, err)
			}
			s = d.String()
		case float64:
			d, err := money.NewFromFloat(x)
			if err != nil {
				return nil, fmt.Errorf("%w: property %q: %w", core.ErrInvalidValue, k, err)
			}
			s = d.String()
		case int:
			s = strconv.Itoa(x)
		case int64:
			s = strconv.FormatInt(x, 10)
		default:
			return nil, fmt.Errorf("%w: property %q of type %T", core.ErrInvalidValue, k, v)
		}
		out[k] = s
	}
	return out, nil
}

// ValidateBatch checks the batch size and validates each event
// independently. It returns a batch-level error for an empty or oversized
// batch, and otherwise one entry per event (nil when valid).
func ValidateBatch(events []*Event) ([]error, error) {
	var v core.Validator
	v.Check(len(events) > 0, "events", core.CodeRequired)
	v.Check(len(events) <= MaxBatchSize, "events", CodeTooManyEvents)
	if err := v.Err(); err != nil {
		return nil, err
	}
	errs := make([]error, len(events))
	for i, e := range events {
		errs[i] = e.Validate()
	}
	return errs, nil
}

// Failure is a post-validation error of an accepted event, reported in the
// batched events.errors webhook (domain §10.2).
type Failure struct {
	// TransactionID is the failed event's transaction ID.
	TransactionID string
	// ExternalSubscriptionID is the failed event's external subscription
	// ID.
	ExternalSubscriptionID string
	// Code is the failed event's metric code.
	Code string
	// Error is the failure kind.
	Error ErrorCode
}
