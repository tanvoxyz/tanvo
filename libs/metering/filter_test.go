package metering_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/metering"
)

// filterCase is a charge filter case of testdata/charge_filters.json.
type filterCase struct {
	Name          string                  `json:"name"`
	Source        string                  `json:"source"`
	Skip          string                  `json:"skip,omitempty"`
	MetricFilters []metering.MetricFilter `json:"metric_filters,omitempty"`
	ChargeFilters []metering.ChargeFilter `json:"charge_filters,omitempty"`

	// Match matches event properties against the charge filters.
	Match *struct {
		Properties   metering.Properties `json:"properties"`
		WantMatching []string            `json:"want_matching"`
		WantSelected string              `json:"want_selected"`
	} `json:"match,omitempty"`
	// Scope builds the query filter of a charge filter ("" for the default
	// bucket).
	Scope *struct {
		Selected string          `json:"selected"`
		Want     metering.Filter `json:"want"`
	} `json:"scope,omitempty"`
	// Minimize minimizes ignored clauses.
	Minimize *struct {
		Clauses []map[string][]string `json:"clauses"`
		Want    []map[string][]string `json:"want"`
	} `json:"minimize,omitempty"`
	// Expand expands the values of a charge filter.
	Expand *struct {
		Filter string              `json:"filter"`
		Want   map[string][]string `json:"want"`
	} `json:"expand,omitempty"`
	// Precedence lists charge filter IDs from the one taking precedence.
	Precedence []string `json:"precedence,omitempty"`
}

func clause(c map[string][]string) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(c)) {
		if len(c[k]) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s=%v;", k, slices.Sorted(slices.Values(c[k])))
	}
	return b.String()
}

func clauses(cs []map[string][]string) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = clause(c)
	}
	return out
}

func TestChargeFilters(t *testing.T) {
	b, err := os.ReadFile("testdata/charge_filters.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []filterCase `json:"cases"`
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		t.Fatal(err)
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Skip != "" {
				t.Skipf("%s: %s", c.Source, c.Skip)
			}
			runFilterCase(t, c)
		})
	}
}

func runFilterCase(t *testing.T, c filterCase) {
	t.Helper()
	ran := false
	if m := c.Match; m != nil {
		ran = true
		matching, selected := metering.MatchChargeFilter(c.ChargeFilters, c.MetricFilters, m.Properties)
		ids := make([]string, len(matching))
		for i, f := range matching {
			ids[i] = f.ID
		}
		got := ""
		if selected >= 0 {
			got = c.ChargeFilters[selected].ID
		}
		if !slices.Equal(ids, m.WantMatching) && (len(ids) != 0 || len(m.WantMatching) != 0) {
			t.Errorf("%s: matching got = %v, want = %v", c.Source, ids, m.WantMatching)
		}
		if got != m.WantSelected {
			t.Errorf("%s: selected got = %q, want = %q", c.Source, got, m.WantSelected)
		}
	}
	if s := c.Scope; s != nil {
		ran = true
		got, err := metering.ChargeFilterScope(c.ChargeFilters, c.MetricFilters, s.Selected)
		if err != nil {
			t.Fatalf("%s: %v", c.Source, err)
		}
		if clause(got.Matching) != clause(s.Want.Matching) {
			t.Errorf("%s: matching got = %v, want = %v", c.Source, got.Matching, s.Want.Matching)
		}
		if !slices.Equal(clauses(got.Ignored), clauses(s.Want.Ignored)) {
			t.Errorf("%s: ignored got = %v, want = %v", c.Source, got.Ignored, s.Want.Ignored)
		}
	}
	if m := c.Minimize; m != nil {
		ran = true
		got := metering.MinimizeIgnoredFilters(m.Clauses)
		if !slices.Equal(clauses(got), clauses(m.Want)) {
			t.Errorf("%s: got = %v, want = %v", c.Source, got, m.Want)
		}
	}
	if e := c.Expand; e != nil {
		ran = true
		i := slices.IndexFunc(c.ChargeFilters, func(f metering.ChargeFilter) bool { return f.ID == e.Filter })
		if i < 0 {
			t.Fatalf("%s: unknown filter %q", c.Source, e.Filter)
		}
		got := c.ChargeFilters[i].Expand(c.MetricFilters)
		if clause(got) != clause(e.Want) || len(got) != len(e.Want) {
			t.Errorf("%s: got = %v, want = %v", c.Source, got, e.Want)
		}
	}
	if len(c.Precedence) > 0 {
		ran = true
		filters := slices.Clone(c.ChargeFilters)
		slices.SortStableFunc(filters, func(a, b metering.ChargeFilter) int {
			return metering.ComparePrecedence(a, b, c.MetricFilters)
		})
		ids := make([]string, len(filters))
		for i, f := range filters {
			ids[i] = f.ID
		}
		if !slices.Equal(ids, c.Precedence) {
			t.Errorf("%s: precedence got = %v, want = %v", c.Source, ids, c.Precedence)
		}
	}
	if !ran {
		t.Fatalf("%s: case without a check", c.Source)
	}
}

// TestMinimizeIgnoredFiltersThousands ports
// spec/services/events/billing_period_filters/minimize_ignored_filters_service_spec.rb:166:
// a filter per model and type, plus one per model, type and zone, reduces
// to a single clause.
func TestMinimizeIgnoredFiltersThousands(t *testing.T) {
	var models []string
	for i := range 160 {
		models = append(models, fmt.Sprintf("model-%d", i))
	}
	types := []string{"input", "output", "cached"}
	var ignored []map[string][]string
	for _, m := range models {
		for _, ty := range types {
			ignored = append(ignored, map[string][]string{"model": {m}, "type": {ty}})
			for _, z := range []string{"eu", "us"} {
				ignored = append(ignored, map[string][]string{"model": {m}, "type": {ty}, "zone": {z}})
			}
		}
	}
	if len(ignored) != 1440 {
		t.Fatalf("clauses got = %d, want = 1440", len(ignored))
	}
	got := metering.MinimizeIgnoredFilters(ignored)
	want := []map[string][]string{{"model": models, "type": types}}
	if len(got) != 1 || !slices.Equal(got[0]["model"], models) || !slices.Equal(got[0]["type"], types) || len(got[0]) != 2 {
		t.Errorf("got = %v, want = %v", got, want)
	}
}
