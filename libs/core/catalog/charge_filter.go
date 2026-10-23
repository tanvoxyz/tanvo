package catalog

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// AllFilterValues is the sentinel value of a charge filter key that matches
// every value of the metric filter (domain §7.4).
const AllFilterValues = "__ALL_FILTER_VALUES__"

// ChargeFilterCodeSlugLimit bounds the readable part of a charge filter
// code; the code adds "_" and 8 hex characters.
const ChargeFilterCodeSlugLimit = 200

// ChargeFilter prices the events of a charge that match its values with
// its own properties (domain §7.4). Filters are stored with their charge.
type ChargeFilter struct {
	// ID identifies the filter.
	ID core.ChargeFilterID
	// ChargeID is the owning charge.
	ChargeID core.ChargeID
	// Values maps metric filter keys to the allowed values, or to the single
	// [AllFilterValues] sentinel.
	Values map[string][]string
	// Properties are the charge model's properties for this slice.
	Properties Properties
	// InvoiceDisplayName replaces the generated display name.
	InvoiceDisplayName *string
	// Code is the stable code of the filter, set at creation with
	// [ChargeFilterCode] and [NextFreeCode] and never changed; copies keep
	// it.
	Code string
	// CreatedAt is the creation instant; it orders filters of equal
	// specificity.
	CreatedAt time.Time
}

// Clone returns a deep copy of f.
func (f ChargeFilter) Clone() ChargeFilter {
	f.Values = chargeFilterCloneValues(f.Values)
	f.Properties = f.Properties.Clone()
	return f
}

func chargeFilterCloneValues(m map[string][]string) map[string][]string {
	if m == nil {
		return nil
	}
	out := make(map[string][]string, len(m))
	for k, v := range m {
		out[k] = slices.Clone(v)
	}
	return out
}

// IsAll reports whether key uses the [AllFilterValues] sentinel.
func (f *ChargeFilter) IsAll(key string) bool {
	return chargeFilterIsAll(f.Values[key])
}

func chargeFilterIsAll(values []string) bool {
	return len(values) == 1 && values[0] == AllFilterValues
}

// ValidateChargeFilter checks f's values against metric m: at least one
// key; each key is a filter of m; each key has values, each one of the
// metric filter's values, or the single sentinel. Errors are reported on
// "values.<key>".
func ValidateChargeFilter(f *ChargeFilter, m *BillableMetric) error {
	var v core.Validator
	if !v.Check(len(f.Values) > 0, "values", core.CodeRequired) {
		return v.Err()
	}
	v.MaxLen("code", f.Code, ChargeFilterCodeSlugLimit+9)
	for _, key := range slices.Sorted(maps.Keys(f.Values)) {
		field := "values." + key
		mf, ok := m.Filter(key)
		if !v.Check(ok, field, core.CodeInvalid) {
			continue
		}
		values := f.Values[key]
		if !v.Check(len(values) > 0, field, core.CodeRequired) {
			continue
		}
		if chargeFilterIsAll(values) {
			continue
		}
		for _, val := range values {
			if !v.Check(slices.Contains(mf.Values, val), field, core.CodeInvalid) {
				break
			}
		}
	}
	return v.Err()
}

// ChargeFilterValuesEqual reports whether a and b allow the same values for
// the same keys, ignoring value order.
func ChargeFilterValuesEqual(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || !slices.Equal(slices.Sorted(slices.Values(av)), slices.Sorted(slices.Values(bv))) {
			return false
		}
	}
	return true
}

// ChargeFilterCode returns the stable code of a filter with values: the
// slug of the sorted "key:v1+v2" pairs joined by "|", truncated to
// [ChargeFilterCodeSlugLimit], then "_" and the first 8 hex characters of
// the SHA-256 of the canonical form (domain §7.4). It does not depend on
// key or value order.
func ChargeFilterCode(values map[string][]string) string {
	pairs := make([]string, 0, len(values))
	for _, k := range slices.Sorted(maps.Keys(values)) {
		pairs = append(pairs, k+":"+strings.Join(slices.Sorted(slices.Values(values[k])), "+"))
	}
	canonical := strings.Join(pairs, "|")
	slug := chargeFilterParameterize(canonical)
	if len(slug) > ChargeFilterCodeSlugLimit {
		slug = slug[:ChargeFilterCodeSlugLimit]
	}
	sum := sha256.Sum256([]byte(canonical))
	return slug + "_" + hex.EncodeToString(sum[:])[:8]
}

// chargeFilterParameterize lower-cases s, replaces each run of characters
// other than ASCII letters, digits, "-" and "_" by "_", collapses repeated
// "_" and trims "_" at both ends.
func chargeFilterParameterize(s string) string {
	var b strings.Builder
	sep := false
	for _, r := range strings.ToLower(s) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
		if !ok || r == '_' {
			sep = true
			continue
		}
		if sep && b.Len() > 0 {
			b.WriteByte('_')
		}
		sep = false
		b.WriteRune(r)
	}
	return b.String()
}

// ExpandedValues returns the filter's values with each sentinel replaced by
// every value of the metric filter of that key.
func (f *ChargeFilter) ExpandedValues(m *BillableMetric) map[string][]string {
	out := make(map[string][]string, len(f.Values))
	for k, v := range f.Values {
		if chargeFilterIsAll(v) {
			if mf, ok := m.Filter(k); ok {
				v = mf.Values
			}
		}
		out[k] = slices.Clone(v)
	}
	return out
}

// Matches reports whether an event with properties props falls in f: for
// every key of f, the event has the property and its value is allowed (any
// value of the metric filter for the sentinel).
func (f *ChargeFilter) Matches(props map[string]string, m *BillableMetric) bool {
	for k, allowed := range f.ExpandedValues(m) {
		val, ok := props[k]
		if !ok || !slices.Contains(allowed, val) {
			return false
		}
	}
	return true
}

// CompareChargeFilterPrecedence orders filters from the most specific: more
// keys first, then fewer allowed values (sentinels expanded with m), then
// the earlier CreatedAt, then the smaller ID.
func CompareChargeFilterPrecedence(a, b *ChargeFilter, m *BillableMetric) int {
	av, bv := a.ExpandedValues(m), b.ExpandedValues(m)
	if c := cmp.Compare(len(bv), len(av)); c != 0 {
		return c
	}
	count := func(vs map[string][]string) int {
		n := 0
		for _, v := range vs {
			n += len(v)
		}
		return n
	}
	if c := cmp.Compare(count(av), count(bv)); c != 0 {
		return c
	}
	if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
		return c
	}
	return a.ID.Compare(b.ID)
}

// SortChargeFilters sorts filters by [CompareChargeFilterPrecedence].
func SortChargeFilters(filters []ChargeFilter, m *BillableMetric) {
	slices.SortStableFunc(filters, func(a, b ChargeFilter) int { return CompareChargeFilterPrecedence(&a, &b, m) })
}

// MatchChargeFilter returns the filter an event with props is priced by:
// the most specific matching filter, or nil for the default bucket (the
// charge's own properties).
func MatchChargeFilter(filters []ChargeFilter, props map[string]string, m *BillableMetric) *ChargeFilter {
	var best *ChargeFilter
	for i := range filters {
		f := &filters[i]
		if f.Matches(props, m) && (best == nil || CompareChargeFilterPrecedence(f, best, m) < 0) {
			best = f
		}
	}
	return best
}

// DisplayName returns the invoice display name, or the filter's values
// joined with sep in key order (a sentinel shows its key).
func (f *ChargeFilter) DisplayName(sep string) string {
	if f.InvoiceDisplayName != nil && *f.InvoiceDisplayName != "" {
		return *f.InvoiceDisplayName
	}
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(f.Values)) {
		if f.IsAll(k) {
			parts = append(parts, k)
			continue
		}
		parts = append(parts, f.Values[k]...)
	}
	return strings.Join(parts, sep)
}

// TrimChargeFilter applies a change of the metric's filters to f (domain
// §7.1): values the metric no longer allows are removed from f. It reports
// whether f survives; a filter that references a removed key, or whose key
// is left without values, is deleted by the caller.
func TrimChargeFilter(f *ChargeFilter, change MetricFilterChange) bool {
	for _, k := range change.RemovedKeys {
		if _, ok := f.Values[k]; ok {
			return false
		}
	}
	for k, removed := range change.RemovedValues {
		vs, ok := f.Values[k]
		if !ok || chargeFilterIsAll(vs) {
			continue
		}
		vs = slices.DeleteFunc(vs, func(s string) bool { return slices.Contains(removed, s) })
		if len(vs) == 0 {
			return false
		}
		f.Values[k] = vs
	}
	return true
}
