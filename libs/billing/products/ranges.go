package products

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// rangeKeys are the tiered properties of the catalog's rate models.
//
// source: app/services/rate_properties/normalize_ranges_service.rb
var rangeKeys = []string{"graduated_ranges", "graduated_percentage_ranges", "volume_ranges"}

// NormalizeRanges turns catalog tiers, which name only their upper bound
// (to_value, nil on the open last tier), into the charge models' ranges:
// each tier starts where the previous one ends, the first at 0. A tier
// naming its from_value is <key> from_value_not_allowed; bounds that are
// not positive and strictly increasing, or a closed last tier, are <key>
// invalid_<key>. Properties without tiers are returned as they are.
//
// Lago stores fractional bounds as floats and refuses those with more
// than 15 significant digits; Tanvo keeps every bound as an exact decimal.
//
// source: app/services/rate_properties/normalize_ranges_service.rb:14-62
func NormalizeRanges(props catalog.Properties) (catalog.Properties, error) {
	if !props.IsObject() {
		return props, nil
	}
	m := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(props.Raw()))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return props, nil //nolint:nilerr // not an object: left to the model's validator
	}
	touched := false
	for _, key := range rangeKeys {
		list, ok := m[key].([]any)
		if !ok || len(list) == 0 {
			continue
		}
		for _, r := range list {
			if o, ok := r.(map[string]any); ok {
				if _, has := o["from_value"]; has {
					return catalog.Properties{}, fieldError("properties", catalog.CodeFromValueNotAllowed)
				}
			}
		}
		bounds, ok := upperBounds(list)
		if !ok {
			return catalog.Properties{}, fieldError("properties", "invalid_"+key)
		}
		for i, r := range list {
			o := r.(map[string]any)
			if i == 0 {
				o["from_value"] = json.Number("0")
			} else {
				o["from_value"] = bounds[i-1]
			}
			if i < len(bounds) {
				o["to_value"] = bounds[i]
			} else {
				o["to_value"] = nil
			}
		}
		touched = true
	}
	if !touched {
		return props, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return catalog.Properties{}, fmt.Errorf("encode properties: %w", err)
	}
	return catalog.NewProperties(b)
}

// upperBounds returns the closed tiers' upper bounds as JSON numbers:
// every tier is an object, the last one open, the others' bounds positive
// and strictly increasing.
//
// source: app/services/rate_properties/normalize_ranges_service.rb:40-52
func upperBounds(list []any) ([]json.Number, bool) {
	var (
		out  []json.Number
		prev *money.Decimal
	)
	for i, r := range list {
		o, ok := r.(map[string]any)
		if !ok {
			return nil, false
		}
		to := o["to_value"]
		if i == len(list)-1 {
			return out, to == nil
		}
		d, ok := bound(to)
		if !ok || !d.IsPositive() || (prev != nil && !d.GreaterThan(*prev)) {
			return nil, false
		}
		prev = &d
		out = append(out, json.Number(d.Reduce().String()))
	}
	return out, true
}

// bound parses a tier bound: a JSON number or a numeric string, never a
// boolean.
func bound(v any) (money.Decimal, bool) {
	var s string
	switch t := v.(type) {
	case json.Number:
		s = t.String()
	case string:
		s = strings.TrimSpace(t)
	default:
		return money.Decimal{}, false
	}
	d, err := money.Parse(s)
	if err != nil {
		return money.Decimal{}, false
	}
	return d, true
}

// PresentRanges returns stored properties as the catalog shows them: each
// tier without its from_value, its to_value a decimal string (nil on the
// open last tier). Properties without tiers are returned as they are.
//
// source: app/services/rate_properties.rb:11-31
func PresentRanges(props catalog.Properties) (catalog.Properties, error) {
	if !props.IsObject() {
		return props, nil
	}
	m := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(props.Raw()))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return props, nil //nolint:nilerr // not an object: shown as stored
	}
	touched := false
	for _, key := range rangeKeys {
		list, ok := m[key].([]any)
		if !ok {
			continue
		}
		for _, r := range list {
			o, ok := r.(map[string]any)
			if !ok {
				continue
			}
			delete(o, "from_value")
			if to := o["to_value"]; to != nil {
				if d, ok := bound(to); ok {
					o["to_value"] = d.Reduce().String()
				}
			}
		}
		touched = true
	}
	if !touched {
		return props, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return catalog.Properties{}, fmt.Errorf("encode properties: %w", err)
	}
	return catalog.NewProperties(b)
}
