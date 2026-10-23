package metering

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// OperationTypeProperty is the event property carrying the operation of a
// unique-count event: "add" (the default) or "remove".
const OperationTypeProperty = "operation_type"

// Operation is the operation of a unique-count event.
type Operation string

// Unique-count operations.
const (
	OperationAdd    Operation = "add"
	OperationRemove Operation = "remove"
)

// Event is one usage event as the aggregations see it.
type Event struct {
	// OrganizationID, SubscriptionID and Code scope the event: it belongs to
	// the external subscription ID (shared by the subscriptions that replace
	// each other on upgrade or downgrade) and to the billable metric code.
	OrganizationID string `json:"organization_id,omitempty"`
	SubscriptionID string `json:"subscription_id,omitempty"`
	Code           string `json:"code,omitempty"`
	// TransactionID identifies the event within its organization and
	// subscription: the store holds one event per (OrganizationID,
	// SubscriptionID, TransactionID), and a copy stored again with that key
	// replaces the earlier one, whatever its code and timestamp.
	TransactionID string    `json:"transaction_id,omitempty"`
	Timestamp     time.Time `json:"timestamp"`
	// Properties holds the event properties in their text form.
	Properties Properties `json:"properties,omitempty"`
	// PreciseTotalAmountCents is the amount carried by events of dynamic
	// charges.
	PreciseTotalAmountCents *money.Decimal `json:"precise_total_amount_cents,omitempty"`
	// IngestedAt is when the event was stored; it orders events sharing a
	// timestamp and picks the latest copy of a re-sent event.
	IngestedAt time.Time `json:"ingested_at,omitzero"`
}

// Operation returns the unique-count operation of the event.
func (e Event) Operation() Operation {
	if op, ok := e.Properties[OperationTypeProperty]; ok {
		return Operation(op)
	}
	return OperationAdd
}

// Properties are event properties in their text form: JSON strings as they
// are, numbers and booleans as written.
type Properties map[string]string

// UnmarshalJSON accepts an object whose values are strings, numbers,
// booleans or null (null properties are dropped).
func (p *Properties) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("metering: properties: %w", err)
	}
	out := make(Properties, len(raw))
	for k, v := range raw {
		v = bytes.TrimSpace(v)
		switch {
		case bytes.Equal(v, []byte("null")):
		case len(v) > 0 && v[0] == '"':
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return fmt.Errorf("metering: property %q: %w", k, err)
			}
			out[k] = s
		case len(v) > 0 && (v[0] == '{' || v[0] == '['):
			return fmt.Errorf("metering: property %q: nested values are not supported", k)
		default:
			out[k] = string(v)
		}
	}
	*p = out
	return nil
}

var numericValue = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

// numeric returns the property as a decimal when it is written as a plain
// decimal number ("12", "-3.5"); other values do not take part in numeric
// aggregations.
func numeric(p Properties, key string) (money.Decimal, bool) {
	s, ok := p[key]
	if !ok || !numericValue.MatchString(s) {
		return money.Zero, false
	}
	d, err := money.Parse(s)
	if err != nil {
		return money.Zero, false
	}
	return d, true
}

// Groups maps grouping keys to the event values they take; a nil value means
// the property is missing or empty.
type Groups map[string]*string

// GroupsOf returns the groups of the properties for the keys.
func GroupsOf(p Properties, keys []string) Groups {
	g := make(Groups, len(keys))
	for _, k := range keys {
		if v := p[k]; v != "" {
			g[k] = &v
		} else {
			g[k] = nil
		}
	}
	return g
}

// NilGroups returns groups with a nil value for every key.
func NilGroups(keys []string) Groups {
	g := make(Groups, len(keys))
	for _, k := range keys {
		g[k] = nil
	}
	return g
}

// Value returns the value of key, and whether it is set.
func (g Groups) Value(key string) (string, bool) {
	if v := g[key]; v != nil {
		return *v, true
	}
	return "", false
}

// Key returns a canonical string identifying the groups, usable as a map
// key. Groups with the same keys and values have the same Key.
func (g Groups) Key() string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(g)) {
		fmt.Fprintf(&b, "%q", k)
		if v := g[k]; v != nil {
			fmt.Fprintf(&b, "=%q;", *v)
		} else {
			b.WriteString("=nil;")
		}
	}
	return b.String()
}

// Equal reports whether g and h have the same keys and values. A nil Groups
// equals an empty one.
func (g Groups) Equal(h Groups) bool {
	return g.Key() == h.Key()
}

// matches reports whether the properties carry the group values: a set value
// must be equal, a nil value requires the property to be missing or empty.
func (g Groups) matches(p Properties) bool {
	for k, v := range g {
		if v == nil {
			if p[k] != "" {
				return false
			}
		} else if p[k] != *v {
			return false
		}
	}
	return true
}

// String returns the groups for messages.
func (g Groups) String() string { return g.Key() }

// GroupedValue is a value attached to groups.
type GroupedValue struct {
	Groups Groups        `json:"groups"`
	Value  money.Decimal `json:"value"`
}
