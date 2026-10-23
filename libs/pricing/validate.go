package pricing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// FieldError is a validation error of one property.
type FieldError struct {
	// Field is the property, or "billable_metric" for a model the metric's
	// aggregation does not support.
	Field string `json:"field"`
	// Code is the error code, such as "invalid_amount".
	Code string `json:"code"`
}

// Error implements error.
func (e FieldError) Error() string { return e.Field + ": " + e.Code }

// ValidateOptions describe the charge whose properties are validated.
type ValidateOptions struct {
	// LatestAggregation is set when the charge's metric keeps the latest
	// value, which percentage models cannot price.
	LatestAggregation bool
	// ProductCatalog is set for catalog rates, whose volume tiers touch
	// (each starts where the previous one ends) instead of starting one
	// unit after it.
	ProductCatalog bool
}

// ValidateJSON decodes JSON properties and validates them with [Validate].
func ValidateJSON(model ChargeModel, data []byte, opts ValidateOptions) ([]FieldError, error) {
	raw := map[string]any{}
	if len(bytes.TrimSpace(data)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("pricing: decode properties: %w", err)
		}
	}
	return Validate(model, raw, opts), nil
}

// Validate checks the raw properties of a charge with model, as decoded from
// JSON with [json.Decoder.UseNumber] (integers must be [json.Number]s), and
// returns the errors in the
// order they were found; none when they are valid. Amounts must be strings
// of non-negative decimals, so that no float is ever parsed. The dynamic
// model has no properties of its own; every model validates its grouping
// keys.
func Validate(model ChargeModel, raw map[string]any, opts ValidateOptions) []FieldError {
	v := &validator{raw: raw}
	switch model {
	case Standard:
		v.amount("amount", "invalid_amount")
	case Package:
		v.amount("amount", "invalid_amount")
		if n, ok := integer(raw["free_units"]); !ok || n < 0 {
			v.add("free_units", "invalid_free_units")
		}
		if n, ok := integer(raw["package_size"]); !ok || n <= 0 {
			v.add("package_size", "invalid_package_size")
		}
	case Percentage:
		v.percentage(opts)
	case Graduated:
		v.ranges("graduated_ranges", "graduated", nextTo, func(r map[string]any) {
			v.rangeAmount(r, "per_unit_amount", "invalid_amount")
			v.rangeAmount(r, "flat_amount", "invalid_amount")
		})
	case Volume:
		next := nextAfterTo
		if opts.ProductCatalog {
			next = nextTo
		}
		v.ranges("volume_ranges", "volume", next, func(r map[string]any) {
			v.rangeAmount(r, "per_unit_amount", "invalid_amount")
			v.rangeAmount(r, "flat_amount", "invalid_amount")
		})
	case GraduatedPercentage:
		if opts.LatestAggregation {
			v.add("billable_metric", "invalid_value")
		}
		v.ranges("graduated_percentage_ranges", "graduated_percentage", nextTo, func(r map[string]any) {
			v.rangeAmount(r, "flat_amount", "invalid_amount")
			v.rangeAmount(r, "rate", "invalid_rate")
		})
	}
	v.pricingGroupKeys()
	v.presentationGroupKeys()
	return v.errs
}

type validator struct {
	raw  map[string]any
	errs []FieldError
}

func (v *validator) add(field, code string) {
	v.errs = append(v.errs, FieldError{Field: field, Code: code})
}

// amount requires raw[key] to be a valid amount.
func (v *validator) amount(key, code string) {
	if !validAmount(v.raw[key]) {
		v.add(key, code)
	}
}

// optionalAmount requires raw[key] to be absent (nil) or a valid amount.
func (v *validator) optionalAmount(key, code string) {
	if x, ok := v.raw[key]; ok && x != nil && !validAmount(x) {
		v.add(key, code)
	}
}

func (v *validator) rangeAmount(r map[string]any, key, code string) {
	if !validAmount(r[key]) {
		v.add(key, code)
	}
}

func (v *validator) percentage(opts ValidateOptions) {
	if opts.LatestAggregation {
		v.add("billable_metric", "invalid_value")
	}
	v.amount("rate", "invalid_rate")
	v.optionalAmount("fixed_amount", "invalid_fixed_amount")
	if x := v.raw["free_units_per_events"]; x != nil {
		if n, ok := integer(x); !ok || n <= 0 {
			v.add("free_units_per_events", "invalid_free_units_per_events")
		}
	}
	v.optionalAmount("free_units_per_total_aggregation", "invalid_free_units_per_total_aggregation")

	minRaw, maxRaw := v.raw["per_transaction_min_amount"], v.raw["per_transaction_max_amount"]
	if present(minRaw) && !validAmount(minRaw) {
		v.add("per_transaction_min_amount", "invalid_amount")
	}
	if present(maxRaw) && !validAmount(maxRaw) {
		v.add("per_transaction_max_amount", "invalid_amount")
	}
	if lo, ok := parseAmount(minRaw); ok {
		if hi, ok := parseAmount(maxRaw); ok && lo.GreaterThan(hi) {
			v.add("per_transaction_max_amount", "per_transaction_max_lower_than_per_transaction_min")
		}
	}
}

// nextTo is the lower bound a tier after r starts at (or one unit after,
// which is also accepted) when tiers touch.
func nextTo(r map[string]any) money.Decimal {
	to, _ := number(r["to_value"])
	return to
}

// nextAfterTo is the lower bound a volume tier after r starts at (or one
// unit after).
func nextAfterTo(r map[string]any) money.Decimal {
	to, _ := number(r["to_value"])
	return to.Add(money.One)
}

// ranges validates the tiers under key.
func (v *validator) ranges(key, name string, next func(map[string]any) money.Decimal, amounts func(map[string]any)) {
	list, _ := v.raw[key].([]any)
	if len(list) == 0 {
		v.add(key, "missing_"+name+"_ranges")
		return
	}
	nextFrom := money.Zero
	for i, x := range list {
		r, _ := x.(map[string]any)
		if r == nil {
			r = map[string]any{}
		}
		amounts(r)
		if !validBounds(r, i, len(list), nextFrom) {
			v.add(key, "invalid_"+name+"_ranges")
		}
		nextFrom = next(r)
	}
}

// validBounds reports whether tier i of n starts at nextFrom (or one unit
// after) and ends after it starts, the last tier having no upper bound.
func validBounds(r map[string]any, i, n int, nextFrom money.Decimal) bool {
	from, ok := number(r["from_value"])
	if r["from_value"] == nil || !ok || (!from.Equal(nextFrom) && !from.Equal(nextFrom.Add(money.One))) {
		return false
	}
	to, hasTo := r["to_value"]
	if i == n-1 {
		return !hasTo || to == nil
	}
	upper, _ := number(to)
	return upper.GreaterThan(from)
}

// pricingGroupKeys requires an array of non-blank strings, or nothing.
func (v *validator) pricingGroupKeys() {
	x, ok := v.raw["pricing_group_keys"]
	if !ok || x == nil {
		return
	}
	if list, ok := x.([]any); ok {
		valid := true
		for _, e := range list {
			if s, ok := e.(string); !ok || strings.TrimSpace(s) == "" {
				valid = false
				break
			}
		}
		if valid {
			return
		}
	}
	v.add("pricing_group_keys", "invalid_type")
}

// presentationGroupKeys requires at most two distinct {value, options}
// keys.
func (v *validator) presentationGroupKeys() {
	x := v.raw["presentation_group_keys"]
	if !present(x) {
		return
	}
	var values []string
	list, valid := x.([]any)
	if valid {
		for _, e := range list {
			key, ok := e.(map[string]any)
			if !ok {
				valid = false
				break
			}
			value, isString := key["value"].(string)
			valueValid := isString && strings.TrimSpace(value) != ""
			if valueValid {
				values = append(values, value)
			}
			if !validPresentationKey(key) || !valueValid {
				valid = false
				break
			}
		}
	}
	if !valid {
		v.add("presentation_group_keys", "invalid_type")
	}
	if size(x) > 2 {
		v.add("presentation_group_keys", "too_many_keys")
	}
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] {
			v.add("presentation_group_keys", "value_is_duplicated")
			break
		}
		seen[value] = true
	}
}

// validPresentationKey checks the keys and options of a presentation key.
func validPresentationKey(key map[string]any) bool {
	for k := range key {
		if k != "value" && k != "options" {
			return false
		}
	}
	if _, ok := key["value"]; !ok {
		return false
	}
	options, ok := key["options"]
	if !ok {
		return true
	}
	m, ok := options.(map[string]any)
	if !ok || len(m) != 1 {
		return false
	}
	_, isBool := m["display_in_invoice"].(bool)
	return isBool
}

// size is the size of a JSON value the way Ruby counts it: elements,
// keys or characters.
func size(x any) int {
	switch x := x.(type) {
	case []any:
		return len(x)
	case map[string]any:
		return len(x)
	case string:
		return utf8.RuneCountInString(x)
	default:
		return 0
	}
}

// present reports whether x is neither null, false, nor blank.
func present(x any) bool {
	switch x := x.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return strings.TrimSpace(x) != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	default:
		return true
	}
}

// parseAmount parses a string amount.
func parseAmount(x any) (money.Decimal, bool) {
	s, ok := x.(string)
	if !ok {
		return money.Zero, false
	}
	d, err := money.Parse(s)
	if err != nil {
		return money.Zero, false
	}
	return d, true
}

// validAmount reports whether x is a string holding a finite decimal ≥ 0.
// Numbers are rejected so that no amount is ever parsed from a float.
func validAmount(x any) bool {
	d, ok := parseAmount(x)
	return ok && !d.IsNegative()
}

// integer returns x as an int64 when it is a JSON integer (not a float,
// even an integral one, nor a string).
func integer(x any) (int64, bool) {
	switch x := x.(type) {
	case json.Number:
		n, err := strconv.ParseInt(x.String(), 10, 64)
		return n, err == nil
	case int:
		return int64(x), true
	case int64:
		return x, true
	default:
		return 0, false
	}
}

// number returns a JSON number (or a numeric string) as a decimal; nil is
// 0 and anything else is not a number.
func number(x any) (money.Decimal, bool) {
	switch x := x.(type) {
	case nil:
		return money.Zero, true
	case json.Number:
		d, err := money.Parse(x.String())
		return d, err == nil
	case string:
		d, err := money.Parse(x)
		return d, err == nil
	case int:
		return money.NewFromInt(int64(x)), true
	case int64:
		return money.NewFromInt(x), true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return money.Zero, false
		}
		d, err := money.NewFromFloat(x)
		return d, err == nil
	default:
		return money.Zero, false
	}
}
