package validators

import (
	"encoding/json"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/pricing"
)

// preservedKey is the property whose value keeps its keys as written
// (Utils::ChargeProperties::PRESERVED_KEY).
const preservedKey = "custom_properties"

// underscoreKeys is Utils::ChargeProperties.underscore_keys on an object:
// every key is underscored, deeply, except under custom_properties. A
// later key that underscores to the same name wins.
func underscoreKeys(v *value) map[string]any {
	out := make(map[string]any, len(v.keys))
	for _, k := range v.keys {
		key := underscore(k)
		if key == preservedKey {
			out[key] = v.vals[k].native()
		} else {
			out[key] = underscoreValue(v.vals[k])
		}
	}
	return out
}

func underscoreValue(v *value) any {
	switch {
	case v == nil:
		return nil
	case v.kind == kindObject:
		return underscoreKeys(v)
	case v.kind == kindArray:
		out := make([]any, len(v.arr))
		for i, e := range v.arr {
			out[i] = underscoreValue(e)
		}
		return out
	default:
		return v.native()
	}
}

// underscore is ActiveSupport's String#underscore without acronyms: "::"
// becomes "/", "_" goes between a lowercase letter or digit and an
// uppercase one and before the last capital of a run followed by a
// lowercase letter, "-" becomes "_", and the result is downcased.
func underscore(s string) string {
	if !strings.ContainsAny(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ-") && !strings.Contains(s, "::") {
		return s
	}
	s = strings.ReplaceAll(s, "::", "/")
	isUpper := func(b byte) bool { return b >= 'A' && b <= 'Z' }
	isLower := func(b byte) bool { return b >= 'a' && b <= 'z' }
	isDigit := func(b byte) bool { return b >= '0' && b <= '9' }
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i > 0 && isUpper(s[i]) {
			prev := s[i-1]
			if (isLower(prev) || isDigit(prev)) || (isUpper(prev) && i+1 < len(s) && isLower(s[i+1])) {
				b.WriteByte('_')
			}
		}
		b.WriteByte(s[i])
	}
	return strings.ToLower(strings.ReplaceAll(b.String(), "-", "_"))
}

// propertyErrors validates the properties of a charge or fixed charge of
// model with libs/pricing and returns lago's per-key error codes, grouped
// by key in the order each key first failed. raised is true when lago's
// validator raises on these properties (it reports invalid_value on the
// properties then).
func propertyErrors(model pricing.ChargeModel, props map[string]any, latest bool) (keys []string, codes map[string][]string, raised bool) {
	if validatorRaises(model, props) {
		return nil, nil, true
	}
	codes = map[string][]string{}
	add := func(k, c string) {
		if _, ok := codes[k]; !ok {
			keys = append(keys, k)
		}
		codes[k] = append(codes[k], c)
	}
	for _, e := range pricing.Validate(model, props, pricing.ValidateOptions{LatestAggregation: latest}) {
		add(e.Field, e.Code)
		// Integer#size is 8 in Ruby: an integer presentation_group_keys has
		// too many keys as well.
		if e.Field == "presentation_group_keys" && e.Code == "invalid_type" && isRubyInteger(props["presentation_group_keys"]) {
			add(e.Field, "too_many_keys")
		}
	}
	return keys, codes, false
}

// validatorRaises reports whether lago's Charges::Validators service of
// model raises on props instead of reporting errors.
func validatorRaises(model pricing.ChargeModel, props map[string]any) bool {
	switch model {
	case pricing.Graduated:
		return rangesRaise(props["graduated_ranges"], false) || presentationKeysRaise(props)
	case pricing.GraduatedPercentage:
		return rangesRaise(props["graduated_percentage_ranges"], false) || presentationKeysRaise(props)
	case pricing.Volume:
		return rangesRaise(props["volume_ranges"], true) || presentationKeysRaise(props)
	case pricing.Percentage:
		return minMaxRaise(props) || presentationKeysRaise(props)
	default:
		return presentationKeysRaise(props)
	}
}

// rangesRaise mirrors the ranges of the graduated, graduated percentage
// and volume validators and RangeBoundsValidator: a ranges value that is
// not a list of objects, a bound BigDecimal(x.to_s) cannot read, and, for
// volume, a to_value that cannot be incremented.
func rangesRaise(x any, volume bool) bool {
	var list []any
	switch r := x.(type) {
	case nil:
	case bool:
		// (false || []) is [] for graduated; false&.map raises for volume.
		if r || volume {
			return true
		}
	case map[string]any:
		if len(r) > 0 {
			return true
		}
	case []any:
		list = r
	default:
		return true
	}
	ranges := make([]map[string]any, 0, len(list))
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			return true
		}
		ranges = append(ranges, m)
	}
	next := any(json.Number("0"))
	for i, r := range ranges {
		from, ok, fromFinite := toSDecimal(r["from_value"])
		if !ok {
			return true
		}
		nextFrom, ok, nextFinite := toSDecimal(next)
		if !ok {
			return true
		}
		validFrom := fromFinite && nextFinite && (from.Equal(nextFrom) || from.Equal(nextFrom.Add(money.One)))
		if validFrom && i < len(ranges)-1 {
			to := r["to_value"]
			if to == nil {
				to = json.Number("0")
			}
			if _, ok, _ := toSDecimal(to); !ok {
				return true
			}
		}
		to := r["to_value"]
		if to == nil || to == false {
			to = json.Number("0")
		}
		if !volume {
			next = to
			continue
		}
		n, isNumber := to.(json.Number)
		if !isNumber {
			return true
		}
		d, err := money.Parse(n.String())
		if err != nil {
			return true
		}
		next = json.Number(d.Add(money.One).String())
	}
	return false
}

// minMaxRaise mirrors BigDecimal(min) <= BigDecimal(max) of the percentage
// validator, which runs when both are set (the premium gate is dropped):
// BigDecimal raises on a float, a boolean, an object, an array or an
// unparsable string.
func minMaxRaise(props map[string]any) bool {
	lo, hi := props["per_transaction_min_amount"], props["per_transaction_max_amount"]
	if lo == nil || hi == nil {
		return false
	}
	return !bigDecimalArg(lo) || !bigDecimalArg(hi)
}

// presentationKeysRaise mirrors raw_keys.size, which raises on true and on
// a float.
func presentationKeysRaise(props map[string]any) bool {
	switch x := props["presentation_group_keys"].(type) {
	case bool:
		return x
	case json.Number:
		return !isRubyInteger(x)
	default:
		return false
	}
}

// filterRaises mirrors FilterPropertiesService on a fixed charge, whose
// pricing_group_keys.reject(&:empty?) raises when the present keys are not
// a list or an object, or hold an element without empty? (a number, a
// boolean or nil).
func filterRaises(props map[string]any) bool {
	x := props["pricing_group_keys"]
	if !rubyPresent(x) {
		return false
	}
	switch list := x.(type) {
	case map[string]any:
		return false
	case []any:
		for _, e := range list {
			switch e.(type) {
			case string, []any, map[string]any:
			default:
				return true
			}
		}
		return false
	default:
		return true
	}
}

// toSDecimal is BigDecimal(x.to_s): nil, booleans, objects and arrays
// raise (ok false); numbers and decimal strings parse, Infinity and NaN
// to a value that is not finite.
func toSDecimal(x any) (d money.Decimal, ok, finite bool) {
	switch v := x.(type) {
	case json.Number:
		d, err := money.Parse(v.String())
		return d, err == nil, err == nil
	case string:
		return rubyDecimal(v)
	default:
		return money.Decimal{}, false, false
	}
}

// bigDecimalArg reports whether BigDecimal(x) accepts x: an integer or a
// decimal string.
func bigDecimalArg(x any) bool {
	switch v := x.(type) {
	case json.Number:
		return isRubyInteger(v)
	case string:
		_, ok, _ := rubyDecimal(v)
		return ok
	default:
		return false
	}
}

// isRubyInteger reports whether x is a JSON number Ruby reads as an
// Integer.
func isRubyInteger(x any) bool {
	n, ok := x.(json.Number)
	return ok && !strings.ContainsAny(n.String(), ".eE")
}

// rubyPresent is Object#present? on a JSON value.
func rubyPresent(x any) bool {
	switch v := x.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return strings.TrimSpace(v) != ""
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	default:
		return true
	}
}
