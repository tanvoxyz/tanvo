package cli_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// usageStep ingests events at an instant and checks the current usage of
// the subscription's only charge.
type usageStep struct {
	at time.Time
	// events are the properties of the events; precise is the dynamic
	// amount of each, when set.
	events  []map[string]any
	precise []string
	// units and amount are the charge's usage; total, when not -1, is the
	// usage's total with taxes.
	units  string
	amount int64
	total  int64
	// events, when set, is the charge's events count.
	eventsCount string
}

// decimalString drops the trailing zeros of a decimal: "3.0" is "3".
func decimalString(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// TestScenarioChargeModels prices the usage of one charge per charge
// model and aggregation type through the current usage API.
func TestScenarioChargeModels(t *testing.T) {
	t.Parallel()
	day := func(y, m, d, h int) time.Time { return time.Date(y, time.Month(m), d, h, 0, 0, 0, time.UTC) }
	repeat := func(n int, props map[string]any) []map[string]any {
		out := make([]map[string]any, n)
		for i := range out {
			out[i] = props
		}
		return out
	}
	amount := func(a string) map[string]any { return map[string]any{"amount": a} }
	item := func(v string) map[string]any { return map[string]any{"item_id": v} }
	for _, tt := range []struct {
		name string
		// metric is the billable metric's aggregation type and field.
		aggregation, field string
		model              string
		properties         map[string]any
		// planAmount is the plan's in-arrears amount; tax is the billing
		// entity's default tax rate, when set.
		planAmount, tax string
		start           time.Time
		steps           []usageStep
	}{
		{
			// source: spec/scenarios/current_usage/by_charge_model/package_spec.rb:13
			name: "package", aggregation: "AGGREGATION_TYPE_COUNT", model: "CHARGE_MODEL_PACKAGE",
			properties: map[string]any{"package": map[string]any{"amount": "100", "free_units": 0, "package_size": 10}},
			start:      day(2024, 3, 5, 0),
			steps: []usageStep{
				{at: day(2024, 3, 6, 0), events: repeat(3, nil), units: "3", amount: 10_000, total: -1},
				{at: day(2024, 3, 6, 1), events: repeat(12, nil), units: "15", amount: 20_000, total: -1},
			},
		},
		{
			// source: spec/scenarios/current_usage/by_charge_model/volume_spec.rb:25
			// source: spec/scenarios/current_usage/by_charge_model/volume_spec.rb:52
			name: "volume", aggregation: "AGGREGATION_TYPE_COUNT", model: "CHARGE_MODEL_VOLUME",
			properties: map[string]any{"volume": map[string]any{"ranges": []map[string]any{
				{"from_value": "0", "to_value": "10", "per_unit_amount": "5", "flat_amount": "100"},
				{"from_value": "11", "per_unit_amount": "2", "flat_amount": "50"},
			}}},
			start: day(2024, 3, 5, 0),
			steps: []usageStep{
				{at: day(2024, 3, 6, 0), events: repeat(5, nil), units: "5", amount: 12_500, total: -1},
				// 15 × 2 + 50 in the second range.
				{at: day(2024, 3, 6, 1), events: repeat(10, nil), units: "15", amount: 8000, total: -1},
			},
		},
		{
			// source: spec/scenarios/current_usage/by_charge_model/graduated_percentage_spec.rb:25
			// source: spec/scenarios/current_usage/by_charge_model/graduated_percentage_spec.rb:50
			name: "graduated percentage", aggregation: "AGGREGATION_TYPE_SUM", field: "amount", model: "CHARGE_MODEL_GRADUATED_PERCENTAGE",
			properties: map[string]any{"graduated_percentage": map[string]any{"ranges": []map[string]any{
				{"from_value": "0", "to_value": "100", "rate": "5", "flat_amount": "0"},
				{"from_value": "101", "rate": "2", "flat_amount": "10"},
			}}},
			start: day(2024, 3, 5, 0),
			steps: []usageStep{
				{at: day(2024, 3, 6, 0), events: []map[string]any{amount("50")}, units: "50", amount: 250, total: -1},
				{at: day(2024, 3, 6, 1), events: []map[string]any{amount("100")}, units: "150", amount: 1600, total: -1},
			},
		},
		{
			// source: spec/scenarios/current_usage/by_charge_model/percentage_spec.rb:17
			name: "percentage", aggregation: "AGGREGATION_TYPE_SUM", field: "amount", model: "CHARGE_MODEL_PERCENTAGE",
			properties: map[string]any{"percentage": map[string]any{"rate": "1", "fixed_amount": "5", "free_units_per_events": 3}},
			planAmount: "10", tax: "20", start: day(2023, 3, 5, 0),
			steps: []usageStep{
				{at: day(2023, 3, 6, 0), events: []map[string]any{amount("10")}, units: "10", amount: 0, total: 0},
				{at: day(2023, 3, 6, 1), events: []map[string]any{amount("10")}, units: "20", amount: 0, total: 0},
				{at: day(2023, 3, 6, 2), events: []map[string]any{amount("10")}, units: "30", amount: 0, total: 0},
				{at: day(2023, 3, 6, 3), events: []map[string]any{amount("10")}, units: "40", amount: 510, total: 612},
			},
		},
		{
			// source: spec/scenarios/current_usage/by_charge_model/dynamic_spec.rb:27
			name: "dynamic", aggregation: "AGGREGATION_TYPE_SUM", field: "item_id", model: "CHARGE_MODEL_DYNAMIC",
			properties: map[string]any{"dynamic": map[string]any{}}, planAmount: "10", start: day(2023, 3, 5, 0),
			steps: []usageStep{
				// The precise total amounts are in cents.
				{at: day(2023, 3, 6, 0), events: []map[string]any{item("10")}, precise: []string{"10.1"}, units: "10", amount: 10, total: 10},
				{at: day(2023, 3, 6, 1), events: []map[string]any{item("10")}, precise: []string{"901.9"}, units: "20", amount: 912, total: 912},
			},
		},
		{
			// source: spec/scenarios/current_usage/by_aggregation_type/latest_agg_spec.rb:20
			name: "latest", aggregation: "AGGREGATION_TYPE_LATEST", field: "item_id", model: "CHARGE_MODEL_STANDARD",
			properties: map[string]any{"standard": map[string]any{"amount": "5"}}, start: day(2024, 3, 1, 0),
			steps: []usageStep{
				{at: day(2024, 3, 5, 1), events: []map[string]any{item("10")}, units: "10", amount: 5000, total: -1},
				{at: day(2024, 3, 5, 2), events: []map[string]any{item("3")}, units: "3", amount: 1500, total: -1},
				{at: day(2024, 3, 5, 3), events: []map[string]any{item("50")}, units: "50", amount: 25_000, total: -1, eventsCount: "3"},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newScenario(t, tt.start, scenarioOptions{})
			if tt.tax != "" {
				s.api(http.MethodPost, "/v1/taxes", map[string]any{"code": "tax", "name": "Tax", "rate": tt.tax})
				entity := s.api(http.MethodGet, "/v1/billing_entities", nil).str("billing_entities.0.id")
				s.api(http.MethodPatch, "/v1/billing_entities/"+entity, map[string]any{"tax_ids": []string{"code:tax"}})
			}
			s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer"})
			metric := map[string]any{"name": "Metric", "code": "metric", "aggregation_type": tt.aggregation}
			if tt.field != "" {
				metric["field_name"] = tt.field
			}
			s.api(http.MethodPost, "/v1/billable_metrics", metric)
			planAmount := tt.planAmount
			if planAmount == "" {
				planAmount = "0"
			}
			s.api(http.MethodPost, "/v1/plans", map[string]any{
				"name": "Plan", "code": "plan", "interval": "INTERVAL_MONTHLY", "amount": money(planAmount),
				"charges": []map[string]any{{
					"code": "metric", "billable_metric_id": "code:metric", "charge_model": tt.model, "properties": tt.properties,
				}},
			})
			s.api(http.MethodPost, "/v1/subscriptions", map[string]any{"external_id": "cust", "customer_id": "external_id:cust", "plan_id": "code:plan"})
			s.idle()

			n := 0
			for i, st := range tt.steps {
				s.at(st.at)
				for j, props := range st.events {
					ev := map[string]any{
						"transaction_id": fmt.Sprintf("tx-%d", n), "external_subscription_id": "cust", "code": "metric",
						"timestamp": st.at.Format(time.RFC3339),
					}
					n++
					if props != nil {
						ev["properties"] = props
					}
					if j < len(st.precise) {
						ev["precise_total_amount"] = st.precise[j]
					}
					s.api(http.MethodPost, "/v1/events", ev)
				}
				s.idle()
				u := s.api(http.MethodGet, "/v1/customers/external_id%3Acust/subscriptions/external_id%3Acust/usage:current", nil).at("usage")
				charges := u.list("charges")
				if len(charges) != 1 {
					t.Fatalf("step %d: charges usage: got = %d, want = 1", i, len(charges))
				}
				if got := decimalString(charges[0].str("units")); got != st.units {
					t.Errorf("step %d: units: got = %s, want = %s", i, got, st.units)
				}
				if got := max(charges[0].cents("amount"), 0); got != st.amount {
					t.Errorf("step %d: amount: got = %d, want = %d", i, got, st.amount)
				}
				if got := charges[0].str("events_count"); st.eventsCount != "" && got != st.eventsCount {
					t.Errorf("step %d: events count: got = %s, want = %s", i, got, st.eventsCount)
				}
				if st.total >= 0 {
					if got := max(u.cents("total_amount"), 0); got != st.total {
						t.Errorf("step %d: total amount: got = %d, want = %d", i, got, st.total)
					}
				}
			}
			s.noFailedJobs()
		})
	}
}
