package metering

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// Filter restricts a [Query] to the events of one charge filter.
type Filter struct {
	// Matching requires, for every key, the property to be present and one
	// of the values.
	Matching map[string][]string `json:"matching,omitempty"`
	// Ignored excludes the events matching any of its clauses. A clause
	// matches when, for every key with values, the property (a missing one
	// reads as "") is one of the values. Clauses without any key with
	// values are skipped.
	Ignored []map[string][]string `json:"ignored,omitempty"`
}

// Matches reports whether the properties pass the filter.
func (f Filter) Matches(p Properties) bool {
	for k, values := range f.Matching {
		v, ok := p[k]
		if !ok || !slices.Contains(values, v) {
			return false
		}
	}
	for _, clause := range f.Ignored {
		if clauseMatches(clause, p) {
			return false
		}
	}
	return true
}

func clauseMatches(clause map[string][]string, p Properties) bool {
	constrained := false
	for k, values := range clause {
		if len(values) == 0 {
			continue
		}
		constrained = true
		if !slices.Contains(values, p[k]) {
			return false
		}
	}
	return constrained
}

// AllFilterValues is the charge filter value standing for every value the
// billable metric filter declares.
const AllFilterValues = "__ALL_FILTER_VALUES__"

// MetricFilter is a filter key declared by a billable metric, with the values
// it allows.
type MetricFilter struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

// ChargeFilter is a filter of a charge: for some metric filter keys, the
// values an event must carry to be billed on it.
type ChargeFilter struct {
	ID string `json:"id"`
	// Values maps metric filter keys to allowed values; [AllFilterValues]
	// alone stands for every value of the metric filter.
	Values map[string][]string `json:"values"`
	// CreatedAt orders filters of the same specificity. The zero time marks
	// an unsaved filter, which comes after every saved one.
	CreatedAt time.Time `json:"created_at,omitzero"`
}

// Expand returns the filter values with [AllFilterValues] replaced by the
// values of the metric filter.
func (f ChargeFilter) Expand(metric []MetricFilter) map[string][]string {
	out := make(map[string][]string, len(f.Values))
	for k, values := range f.Values {
		if len(values) == 1 && values[0] == AllFilterValues {
			values = nil
			for _, m := range metric {
				if m.Key == k {
					values = m.Values
					break
				}
			}
		}
		out[k] = slices.Clone(values)
	}
	return out
}

// ComparePrecedence orders two filters by the precedence of billing an event
// matching both: the filter with more keys first, then the one allowing
// fewer values, then the older one (an unsaved filter last, ties broken by
// ID). It returns a negative number when a takes precedence over b.
func ComparePrecedence(a, b ChargeFilter, metric []MetricFilter) int {
	ea, eb := a.Expand(metric), b.Expand(metric)
	if c := cmp.Compare(len(eb), len(ea)); c != 0 {
		return c
	}
	if c := cmp.Compare(countValues(ea), countValues(eb)); c != 0 {
		return c
	}
	switch {
	case a.CreatedAt.IsZero() && b.CreatedAt.IsZero():
		return 0
	case a.CreatedAt.IsZero():
		return 1
	case b.CreatedAt.IsZero():
		return -1
	}
	if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
		return c
	}
	return cmp.Compare(a.ID, b.ID)
}

func countValues(m map[string][]string) int {
	n := 0
	for _, v := range m {
		n += len(v)
	}
	return n
}

// MatchChargeFilter returns the filters of a charge the event properties
// match, in the order of filters, and the one the event is billed on: the
// matching filter taking precedence (see [ComparePrecedence]). It returns
// nil and -1 when no filter matches and the event falls in the default
// bucket.
//
// Only the properties named by a metric filter are looked at; filters
// without values match no event.
func MatchChargeFilter(filters []ChargeFilter, metric []MetricFilter, p Properties) (matching []ChargeFilter, selected int) {
	selected = -1
	for i, f := range filters {
		values := f.Expand(metric)
		if len(values) == 0 || !filterMatches(values, metric, p) {
			continue
		}
		matching = append(matching, f)
		if selected < 0 || ComparePrecedence(f, filters[selected], metric) < 0 {
			selected = i
		}
	}
	return matching, selected
}

func filterMatches(values map[string][]string, metric []MetricFilter, p Properties) bool {
	for k, allowed := range values {
		if !slices.ContainsFunc(metric, func(m MetricFilter) bool { return m.Key == k }) {
			return false
		}
		v, ok := p[k]
		if !ok || !slices.Contains(allowed, v) {
			return false
		}
	}
	return true
}

// ChargeFilterScope returns the [Filter] selecting the events billed on the
// charge filter with the given ID, or on the default bucket of the charge
// when id is empty.
//
// The scope matches the expanded values of the filter and ignores every
// overlapping filter taking precedence over it, since an event matching
// both is billed on the other one. The ignored clauses are minimized with
// [MinimizeIgnoredFilters].
func ChargeFilterScope(filters []ChargeFilter, metric []MetricFilter, id string) (Filter, error) {
	selected := ChargeFilter{}
	if id != "" {
		i := slices.IndexFunc(filters, func(f ChargeFilter) bool { return f.ID == id })
		if i < 0 {
			return Filter{}, fmt.Errorf("metering: charge filter %q not found", id)
		}
		selected = filters[i]
	}
	matching := selected.Expand(metric)

	var ignored []map[string][]string
	for _, f := range filters {
		if id != "" && f.ID == id {
			continue
		}
		values := f.Expand(metric)
		if len(values) == 0 || ComparePrecedence(f, selected, metric) >= 0 {
			continue
		}
		if overlaps(values, matching) {
			ignored = append(ignored, values)
		}
	}

	scope := Filter{Ignored: MinimizeIgnoredFilters(ignored)}
	if len(matching) > 0 {
		scope.Matching = matching
	}
	return scope, nil
}

func overlaps(child, matching map[string][]string) bool {
	for k, values := range child {
		m, ok := matching[k]
		if ok && !slices.ContainsFunc(values, func(v string) bool { return slices.Contains(m, v) }) {
			return false
		}
	}
	return true
}

// MinimizeIgnoredFilters rewrites ignored clauses into an equivalent, usually
// much smaller, list: it drops keys without values and empty clauses, merges
// clauses differing on a single key and drops clauses covered by another,
// until the list stops shrinking. The events the clauses exclude are
// unchanged.
func MinimizeIgnoredFilters(clauses []map[string][]string) []map[string][]string {
	filters := factorize(normalizeClauses(clauses))
	for {
		reduced := factorize(absorb(filters))
		if sameClauses(reduced, filters) {
			return filters
		}
		filters = reduced
	}
}

func normalizeClauses(clauses []map[string][]string) []map[string][]string {
	var out []map[string][]string
	for _, c := range clauses {
		n := make(map[string][]string, len(c))
		for k, values := range c {
			if len(values) > 0 {
				n[k] = uniq(values)
			}
		}
		if len(n) > 0 {
			out = append(out, n)
		}
	}
	return out
}

func uniq(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// groupBy groups items by key, keeping the order in which keys first appear.
func groupBy[T any](items []T, key func(T) string) [][]T {
	var groups [][]T
	index := map[string]int{}
	for _, it := range items {
		k := key(it)
		i, ok := index[k]
		if !ok {
			i = len(groups)
			index[k] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], it)
	}
	return groups
}

func signature(c map[string][]string) string {
	return strings.Join(slices.Sorted(maps.Keys(c)), "\x00")
}

func comparableClause(c map[string][]string, except string) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(c)) {
		if k == except {
			continue
		}
		fmt.Fprintf(&b, "%q:%q;", k, slices.Sorted(slices.Values(c[k])))
	}
	return b.String()
}

func orderedClause(c map[string][]string) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(c)) {
		fmt.Fprintf(&b, "%q:%q;", k, c[k])
	}
	return b.String()
}

func sameClauses(a, b []map[string][]string) bool {
	return slices.EqualFunc(a, b, func(x, y map[string][]string) bool { return orderedClause(x) == orderedClause(y) })
}

func factorize(filters []map[string][]string) []map[string][]string {
	var out []map[string][]string
	for _, group := range groupBy(filters, signature) {
		merged := group
		for _, pivot := range slices.Sorted(maps.Keys(group[0])) {
			merged = mergeOn(merged, pivot)
		}
		out = append(out, merged...)
	}
	return out
}

func mergeOn(filters []map[string][]string, pivot string) []map[string][]string {
	if len(filters) < 2 {
		return filters
	}
	var out []map[string][]string
	for _, group := range groupBy(filters, func(c map[string][]string) string { return comparableClause(c, pivot) }) {
		if len(group) == 1 {
			out = append(out, group[0])
			continue
		}
		m := maps.Clone(group[0])
		var values []string
		for _, c := range group {
			values = append(values, c[pivot]...)
		}
		m[pivot] = uniq(values)
		out = append(out, m)
	}
	return out
}

func absorb(filters []map[string][]string) []map[string][]string {
	var out []map[string][]string
	for i, f := range filters {
		covered := false
		for j, other := range filters {
			if i == j || !covers(other, f) {
				continue
			}
			// Equivalent clauses cover each other: only the first is kept.
			if j < i || !covers(f, other) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, f)
		}
	}
	return out
}

// covers reports whether every event matching covered also matches
// covering.
func covers(covering, covered map[string][]string) bool {
	for k, values := range covering {
		c, ok := covered[k]
		if !ok {
			return false
		}
		for _, v := range c {
			if !slices.Contains(values, v) {
				return false
			}
		}
	}
	return true
}
