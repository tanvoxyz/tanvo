package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Properties are the pricing properties of a charge, a charge filter or a
// fixed charge (domain §7.3), kept as the raw JSON object the API received.
// libs/billing decodes them with libs/pricing for the charge's model; core
// only checks that they are a JSON object. The pricing_group_keys and
// presentation_group_keys of §7.3 live inside the object too.
//
// The zero Properties is "no properties"; it marshals as {}.
type Properties struct {
	raw json.RawMessage
}

// NewProperties wraps raw, which must be a JSON object, failing with
// core.ErrInvalidValue otherwise. The bytes are copied.
func NewProperties(raw []byte) (Properties, error) {
	p := Properties{raw: bytes.Clone(bytes.TrimSpace(raw))}
	if !p.IsObject() {
		return Properties{}, fmt.Errorf("%w: properties must be a JSON object", core.ErrInvalidValue)
	}
	return p, nil
}

// MustProperties is [NewProperties] that panics on error; for tests.
func MustProperties(raw string) Properties {
	p, err := NewProperties([]byte(raw))
	if err != nil {
		panic(err)
	}
	return p
}

// Raw returns a copy of the JSON object ("{}" when unset).
func (p Properties) Raw() json.RawMessage {
	if p.IsZero() {
		return json.RawMessage("{}")
	}
	return bytes.Clone(p.raw)
}

// IsZero reports whether no properties are set.
func (p Properties) IsZero() bool { return len(p.raw) == 0 }

// IsObject reports whether the properties are unset or a JSON object.
func (p Properties) IsObject() bool {
	if p.IsZero() {
		return true
	}
	if p.raw[0] != '{' || !json.Valid(p.raw) {
		return false
	}
	return true
}

// Clone returns a copy that shares no storage with p.
func (p Properties) Clone() Properties { return Properties{raw: bytes.Clone(p.raw)} }

// Equal reports whether p and q hold the same JSON value, ignoring key
// order and whitespace.
func (p Properties) Equal(q Properties) bool {
	var a, b any
	if err := json.Unmarshal(p.Raw(), &a); err != nil {
		return false
	}
	if err := json.Unmarshal(q.Raw(), &b); err != nil {
		return false
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

// MarshalJSON implements json.Marshaler.
func (p Properties) MarshalJSON() ([]byte, error) { return p.Raw(), nil }

// UnmarshalJSON implements json.Unmarshaler; null decodes as unset.
func (p *Properties) UnmarshalJSON(b []byte) error {
	if string(bytes.TrimSpace(b)) == "null" {
		*p = Properties{}
		return nil
	}
	v, err := NewProperties(b)
	if err != nil {
		return err
	}
	*p = v
	return nil
}

// presentationGroupKeys is the presentation_group_keys of properties.
type presentationGroupKeys struct {
	Keys []struct {
		Value   *string `json:"value"`
		Options *struct {
			DisplayInInvoice *bool `json:"display_in_invoice"`
		} `json:"options"`
	} `json:"presentation_group_keys"`
}

// presentationKeys returns the values of the presentation group keys,
// those displayed on invoices when displayed is set; properties that do
// not decode have none.
func (p Properties) presentationKeys(displayed bool) []string {
	if p.IsZero() {
		return nil
	}
	var g presentationGroupKeys
	if err := json.Unmarshal(p.raw, &g); err != nil {
		return nil
	}
	var out []string
	for _, k := range g.Keys {
		if k.Value == nil {
			continue
		}
		if displayed && (k.Options == nil || k.Options.DisplayInInvoice == nil || !*k.Options.DisplayInInvoice) {
			continue
		}
		out = append(out, *k.Value)
	}
	return out
}

// PresentationGroupKeys returns the event properties the units are broken
// down by for presentation (lago Charge#presentation_group_keys_values).
//
// source: app/models/charge.rb:75-79
func (p Properties) PresentationGroupKeys() []string { return p.presentationKeys(false) }

// PresentationGroupKeysDisplayedInInvoice returns the presentation group
// keys whose breakdown is shown on invoices and in the fee payloads (lago
// Charge#presentation_group_keys_values_displayed_in_invoice).
//
// source: app/models/charge.rb:81-88
func (p Properties) PresentationGroupKeysDisplayedInInvoice() []string {
	return p.presentationKeys(true)
}
