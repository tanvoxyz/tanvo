package expression

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// Event is what an expression is evaluated for.
type Event struct {
	// Code is the event's code, event.code.
	Code string
	// Timestamp is the event's time, event.timestamp: a number of seconds
	// since the Unix epoch, as lago-api passes it (Time#to_i).
	Timestamp time.Time
	// Properties are the event's properties, event.properties.<name>.
	//
	// A string is a number when its text is a decimal ("12.5", "-3",
	// "1e3"), otherwise a string. [money.Decimal], integers, floats and
	// [encoding/json.Number] are numbers; a float that is not finite, or a
	// json.Number that is not a decimal, makes the event invalid
	// ([ErrInvalidEvent]). A bool is the string "true" or "false", nil the
	// empty string, a [fmt.Stringer] its String, and any other value its
	// JSON encoding, as lago-api turns a non-numeric value into its text.
	Properties map[string]any
}

// Kind is the type of a [Value].
type Kind uint8

// The kinds of value.
const (
	// KindNumber is a decimal number.
	KindNumber Kind = iota + 1
	// KindString is a string.
	KindString
)

// String returns "number" or "string".
func (k Kind) String() string {
	switch k {
	case KindNumber:
		return "number"
	case KindString:
		return "string"
	default:
		return "invalid"
	}
}

// Value is the result of an expression: a number or a string. The zero
// Value is invalid.
type Value struct {
	kind Kind
	num  money.Decimal
	str  string
}

// NumberValue returns the number d.
func NumberValue(d money.Decimal) Value { return Value{kind: KindNumber, num: d} }

// StringValue returns the string s.
func StringValue(s string) Value { return Value{kind: KindString, str: s} }

// Kind returns the value's kind, 0 for the zero Value.
func (v Value) Kind() Kind { return v.kind }

// IsNumber reports whether v is a number.
func (v Value) IsNumber() bool { return v.kind == KindNumber }

// Decimal returns the number v and true, or zero and false when v is not a
// number.
func (v Value) Decimal() (money.Decimal, bool) {
	if v.kind != KindNumber {
		return money.Zero, false
	}
	return v.num, true
}

// Text returns the string v and true, or "" and false when v is not a
// string.
func (v Value) Text() (string, bool) {
	if v.kind != KindString {
		return "", false
	}
	return v.str, true
}

// String returns a number in plain notation, keeping its scale ("3.0"), or
// the string.
func (v Value) String() string {
	if v.kind == KindNumber {
		return v.num.String()
	}
	return v.str
}

// Equal reports whether v and w are the same string, or numbers of the same
// value (3 and 3.0 are equal).
func (v Value) Equal(w Value) bool {
	if v.kind != w.kind {
		return false
	}
	if v.kind == KindNumber {
		return v.num.Equal(w.num)
	}
	return v.str == w.str
}

// Result is the outcome of evaluating an expression for one event of a batch:
// a value, or the reason it could not be computed.
type Result struct {
	// Value is the event's value when Err is nil.
	Value Value
	// Err is an [*EvaluationError] when the event could not be evaluated.
	Err error
}
