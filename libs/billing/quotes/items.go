package quotes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// object is a decoded JSON object of the billing items; numbers are
// json.Number.
type object = map[string]any

// decodeItems decodes the billing items of a version; anything but an
// object is no items (lago's billing_items.is_a?(Hash) guards).
func decodeItems(raw json.RawMessage) object {
	if len(raw) == 0 {
		return object{}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return object{}
	}
	if m, ok := v.(object); ok {
		return m
	}
	return object{}
}

// dig is Hash#dig over objects: nil when a step is missing or not an
// object.
func dig(v any, path ...string) any {
	for _, k := range path {
		m, ok := v.(object)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// array returns v as an array; anything else is empty (the validated
// billing items only hold arrays there).
func array(v any) []any {
	x, _ := v.([]any)
	return x
}

// objects returns the objects of the array v.
func objects(v any) []object {
	var out []object
	for _, e := range array(v) {
		if m, ok := e.(object); ok {
			out = append(out, m)
		}
	}
	return out
}

// effective is lago effective_value: the negotiated value of field,
// overrides first, then the catalog snapshot (Ruby ||: nil and false fall
// through).
func effective(item object, field string) any {
	if v := dig(item, "overrides", field); truthy(v) {
		return v
	}
	return dig(item, "payload", field)
}

func truthy(v any) bool {
	if v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return true
}

// presence is Object#presence for a string: nil when blank or not a
// string.
func presence(v any) *string {
	s, ok := v.(string)
	if !ok || len(bytes.TrimSpace([]byte(s))) == 0 {
		return nil
	}
	return &s
}

// str returns v as a string pointer; nil when v is nil. A number is
// formatted.
func str(v any) *string {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return &x
	case json.Number:
		s := x.String()
		return &s
	case bool:
		s := strconv.FormatBool(x)
		return &s
	default:
		return nil
	}
}

// decimal reads a JSON number or numeric string; nil when v is nil.
func decimal(field string, v any) (*money.Decimal, error) {
	var s string
	switch x := v.(type) {
	case nil:
		return nil, nil
	case json.Number:
		s = x.String()
	case string:
		s = x
	default:
		return nil, fieldError(field, core.CodeInvalid)
	}
	d, err := money.Parse(s)
	if err != nil {
		return nil, fieldError(field, core.CodeInvalid)
	}
	return &d, nil
}

// integer reads a JSON integer (or a numeric string of one); nil when v is
// nil.
func integer(field string, v any) (*int64, error) {
	d, err := decimal(field, v)
	if err != nil || d == nil {
		return nil, err
	}
	n, err := strconv.ParseFloat(d.String(), 64)
	if err != nil || n != math.Trunc(n) || math.Abs(n) > math.MaxInt64/2 {
		return nil, fieldError(field, core.CodeInvalid)
	}
	i := int64(n)
	return &i, nil
}

// boolean reads a JSON boolean; nil when v is nil.
func boolean(field string, v any) (*bool, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case bool:
		return &x, nil
	default:
		return nil, fieldError(field, core.CodeInvalid)
	}
}

// encode re-encodes billing items.
func encode(m object) (json.RawMessage, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode billing items: %w", err)
	}
	return b, nil
}
