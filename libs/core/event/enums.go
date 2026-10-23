package event

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// OperationType is the operation an event applies to a unique count metric (domain §7.1).
type OperationType string

// OperationType values.
const (
	// OperationTypeAdd is "add": adds the value.
	OperationTypeAdd OperationType = "add"
	// OperationTypeRemove is "remove": removes the value.
	OperationTypeRemove OperationType = "remove"
)

// OperationTypeValues returns every OperationType, in declaration order.
func OperationTypeValues() []OperationType {
	return []OperationType{OperationTypeAdd, OperationTypeRemove}
}

// Valid reports whether v is one of the declared OperationType values.
func (v OperationType) Valid() bool {
	switch v {
	case OperationTypeAdd, OperationTypeRemove:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v OperationType) String() string { return string(v) }

// ParseOperationType parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseOperationType(s string) (OperationType, error) {
	if v := OperationType(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("event.OperationType", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v OperationType) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *OperationType) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseOperationType(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// ErrorCode is a post-validation error of an ingested event, reported in the events.errors webhook (domain §10.2).
type ErrorCode string

// ErrorCode values.
const (
	// ErrorCodeInvalidCode is "invalid_code": no billable metric has the event's code.
	ErrorCodeInvalidCode ErrorCode = "invalid_code"
	// ErrorCodeMissingAggregationProperty is "missing_aggregation_property": the metric's field_name is absent from the properties.
	ErrorCodeMissingAggregationProperty ErrorCode = "missing_aggregation_property"
	// ErrorCodeInvalidFilterValues is "invalid_filter_values": a filter property has a value outside the filter's values.
	ErrorCodeInvalidFilterValues ErrorCode = "invalid_filter_values"
)

// ErrorCodeValues returns every ErrorCode, in declaration order.
func ErrorCodeValues() []ErrorCode {
	return []ErrorCode{ErrorCodeInvalidCode, ErrorCodeMissingAggregationProperty, ErrorCodeInvalidFilterValues}
}

// Valid reports whether v is one of the declared ErrorCode values.
func (v ErrorCode) Valid() bool {
	switch v {
	case ErrorCodeInvalidCode, ErrorCodeMissingAggregationProperty, ErrorCodeInvalidFilterValues:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v ErrorCode) String() string { return string(v) }

// ParseErrorCode parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseErrorCode(s string) (ErrorCode, error) {
	if v := ErrorCode(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("event.ErrorCode", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v ErrorCode) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *ErrorCode) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseErrorCode(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}
