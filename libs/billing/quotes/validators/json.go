package validators

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// kind is the JSON type of a value.
type kind uint8

const (
	kindNull kind = iota
	kindBool
	kindNumber
	kindString
	kindArray
	kindObject
)

// value is a decoded JSON value that keeps the order of object keys, which
// the structural pass reports additional keys in. A duplicate key keeps
// the position of its first occurrence and the value of its last, as Ruby's
// JSON parser does.
type value struct {
	kind kind
	b    bool
	// s is the string, or the literal of a number.
	s    string
	arr  []*value
	keys []string
	vals map[string]*value
}

// decodeValue decodes one JSON document.
func decodeValue(data []byte) (*value, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeNext(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the JSON document")
	}
	return v, nil
}

func decodeNext(dec *json.Decoder) (*value, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	switch t := tok.(type) {
	case nil:
		return &value{kind: kindNull}, nil
	case bool:
		return &value{kind: kindBool, b: t}, nil
	case json.Number:
		return &value{kind: kindNumber, s: t.String()}, nil
	case string:
		return &value{kind: kindString, s: t}, nil
	case json.Delim:
		switch t {
		case '[':
			v := &value{kind: kindArray, arr: []*value{}}
			for dec.More() {
				e, err := decodeNext(dec)
				if err != nil {
					return nil, err
				}
				v.arr = append(v.arr, e)
			}
			if _, err := dec.Token(); err != nil {
				return nil, fmt.Errorf("decode: %w", err)
			}
			return v, nil
		case '{':
			v := &value{kind: kindObject, vals: map[string]*value{}}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("decode: %w", err)
				}
				key, _ := kt.(string)
				e, err := decodeNext(dec)
				if err != nil {
					return nil, err
				}
				v.set(key, e)
			}
			if _, err := dec.Token(); err != nil {
				return nil, fmt.Errorf("decode: %w", err)
			}
			return v, nil
		}
	}
	return nil, fmt.Errorf("decode: unexpected token %v", tok)
}

// set stores key in an object.
func (v *value) set(key string, e *value) {
	if _, ok := v.vals[key]; !ok {
		v.keys = append(v.keys, key)
	}
	v.vals[key] = e
}

// isNull reports whether v is absent or JSON null (Ruby nil).
func (v *value) isNull() bool { return v == nil || v.kind == kindNull }

// get returns the member key of an object; nil when v is not an object or
// has no such key (Ruby Hash#[] / dig).
func (v *value) get(path ...string) *value {
	cur := v
	for _, k := range path {
		if cur == nil || cur.kind != kindObject {
			return nil
		}
		cur = cur.vals[k]
	}
	return cur
}

// str returns v's string and whether v is a string.
func (v *value) str() (string, bool) {
	if v == nil || v.kind != kindString {
		return "", false
	}
	return v.s, true
}

// items returns the elements of an array, none for anything else (Ruby
// Array(x) on the shapes the structural pass lets through: nil or an
// array).
func (v *value) items() []*value {
	if v == nil || v.kind != kindArray {
		return nil
	}
	return v.arr
}

// orElse returns v unless it is absent or null, then other (Ruby ||; JSON
// false is falsy too).
func (v *value) orElse(other *value) *value {
	if v.isNull() || (v.kind == kindBool && !v.b) {
		return other
	}
	return v
}

// decimal returns a number as a decimal.
func (v *value) decimal() (money.Decimal, bool) {
	if v == nil || v.kind != kindNumber {
		return money.Decimal{}, false
	}
	d, err := money.Parse(v.s)
	return d, err == nil
}

// equal reports JSON equality.
func (v *value) equal(o *value) bool {
	if v.isNull() || o.isNull() {
		return v.isNull() && o.isNull()
	}
	if v.kind != o.kind {
		return false
	}
	switch v.kind {
	case kindBool:
		return v.b == o.b
	case kindString:
		return v.s == o.s
	case kindNumber:
		a, aok := v.decimal()
		b, bok := o.decimal()
		return aok && bok && a.Equal(b)
	case kindArray:
		if len(v.arr) != len(o.arr) {
			return false
		}
		for i := range v.arr {
			if !v.arr[i].equal(o.arr[i]) {
				return false
			}
		}
		return true
	default:
		if len(v.keys) != len(o.keys) {
			return false
		}
		for _, k := range v.keys {
			ov, ok := o.vals[k]
			if !ok || !v.vals[k].equal(ov) {
				return false
			}
		}
		return true
	}
}

// native converts v to the shapes encoding/json decodes into with
// UseNumber (map[string]any, []any, json.Number, string, bool, nil), for
// libs/pricing.
func (v *value) native() any {
	if v == nil {
		return nil
	}
	switch v.kind {
	case kindBool:
		return v.b
	case kindNumber:
		return json.Number(v.s)
	case kindString:
		return v.s
	case kindArray:
		out := make([]any, len(v.arr))
		for i, e := range v.arr {
			out[i] = e.native()
		}
		return out
	case kindObject:
		out := make(map[string]any, len(v.keys))
		for _, k := range v.keys {
			out[k] = v.vals[k].native()
		}
		return out
	default:
		return nil
	}
}
