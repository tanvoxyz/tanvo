package catalog

import (
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
)

func TestBillableMetricValidate(t *testing.T) {
	type filter struct {
		Key    string   `yaml:"key"`
		Values []string `yaml:"values"`
	}
	type tc struct {
		Name             string   `yaml:"name"`
		Source           string   `yaml:"source"`
		AggregationType  string   `yaml:"aggregation_type"`
		FieldName        string   `yaml:"field_name"`
		Recurring        bool     `yaml:"recurring"`
		WeightedInterval string   `yaml:"weighted_interval"`
		Filters          []filter `yaml:"filters"`
		FieldNameAfter   *string  `yaml:"field_name_after"`
		PayableInAdvance *bool    `yaml:"payable_in_advance"`
		Errors           []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "metric.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			m := &BillableMetric{
				ID:               catalogTestMetric,
				OrganizationID:   catalogTestOrg,
				Code:             "api_calls",
				Name:             "API calls",
				AggregationType:  AggregationType(c.AggregationType),
				Recurring:        c.Recurring,
				WeightedInterval: WeightedInterval(c.WeightedInterval),
			}
			if c.FieldName != "" {
				m.FieldName = catalogPtr(c.FieldName)
			}
			for _, f := range c.Filters {
				m.Filters = append(m.Filters, MetricFilter{Key: f.Key, Values: f.Values})
			}
			m.Normalize()
			catalogCheckErrors(t, "Validate()", m.Validate(), c.Errors, c.Source)
			if c.FieldNameAfter != nil {
				got := ""
				if m.FieldName != nil {
					got = *m.FieldName
				}
				if got != *c.FieldNameAfter {
					t.Errorf("FieldName got = %q, want = %q (%s)", got, *c.FieldNameAfter, c.Source)
				}
			}
			if c.PayableInAdvance != nil {
				if got := m.PayableInAdvance(); got != *c.PayableInAdvance {
					t.Errorf("PayableInAdvance() got = %v, want = %v (%s)", got, *c.PayableInAdvance, c.Source)
				}
			}
		})
	}
}

func TestValidateMetricUpdate(t *testing.T) {
	old := catalogTestMetricOf(AggregationTypeSum, false)
	tests := []struct {
		name   string
		change func(m *BillableMetric)
		used   bool
		want   []string
	}{
		{"unused metric may change", func(m *BillableMetric) { m.AggregationType = AggregationTypeMax }, false, []string{}},
		{"name of a used metric may change", func(m *BillableMetric) { m.Name = "Calls" }, true, []string{}},
		{"aggregation of a used metric is locked", func(m *BillableMetric) { m.AggregationType = AggregationTypeMax }, true, []string{"aggregation_type: value_is_immutable"}},
		{"field name of a used metric is locked", func(m *BillableMetric) { m.FieldName = catalogPtr("total") }, true, []string{"field_name: value_is_immutable"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updated := *old
			tt.change(&updated)
			catalogCheckErrors(t, "ValidateMetricUpdate()", ValidateMetricUpdate(old, &updated, tt.used), tt.want, "tanvo")
		})
	}
}

func TestDiffMetricFiltersAndTrim(t *testing.T) {
	old := []MetricFilter{{Key: "region", Values: []string{"eu", "us", "apac"}}, {Key: "tier", Values: []string{"a"}}}
	updated := []MetricFilter{{Key: "region", Values: []string{"eu", "us"}}}
	ch := DiffMetricFilters(old, updated)
	if !slices.Equal(ch.RemovedKeys, []string{"tier"}) || !slices.Equal(ch.RemovedValues["region"], []string{"apac"}) {
		t.Fatalf("DiffMetricFilters() got = %+v, want = tier removed, region apac removed", ch)
	}
	tests := []struct {
		name   string
		values map[string][]string
		keep   bool
		after  []string
	}{
		// source: spec/services/billable_metric_filters/create_or_update_batch_service_spec.rb:322
		{"trims a removed value", map[string][]string{"region": {"eu", "apac"}}, true, []string{"eu"}},
		{"drops a filter left without values", map[string][]string{"region": {"apac"}}, false, nil},
		{"drops a removed key and keeps the filter", map[string][]string{"region": {"eu"}, "tier": {"a"}}, true, []string{"eu"}},
		{"drops a filter whose only key is removed", map[string][]string{"tier": {AllFilterValues}}, false, nil},
		{"drops a filter whose keys are all removed or emptied", map[string][]string{"region": {"apac"}, "tier": {"a"}}, false, nil},
		{"keeps the sentinel", map[string][]string{"region": {AllFilterValues}}, true, []string{AllFilterValues}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &ChargeFilter{Values: tt.values}
			if got := TrimChargeFilter(f, ch); got != tt.keep {
				t.Fatalf("TrimChargeFilter() got = %v, want = %v", got, tt.keep)
			}
			if tt.keep && !slices.Equal(f.Values["region"], tt.after) {
				t.Errorf("region values got = %v, want = %v", f.Values["region"], tt.after)
			}
		})
	}
}
