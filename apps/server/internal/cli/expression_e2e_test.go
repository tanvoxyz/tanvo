package cli_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestScenarioMetricExpression creates a metric whose field is computed by
// an expression, refuses one whose expression does not parse, ingests
// events through the API (the expression's result is stored in the field,
// and an event the expression cannot evaluate is refused) and bills the
// computed units at the end of the period.
func TestScenarioMetricExpression(t *testing.T) {
	t.Parallel()
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newScenario(t, start, scenarioOptions{})

	// source: spec/models/billable_metric_spec.rb:126
	status, body := s.send(http.MethodPost, "/v1/billable_metrics", s.key, "", map[string]any{
		"name": "Bad", "code": "bad", "aggregation_type": "AGGREGATION_TYPE_SUM", "field_name": "value",
		"expression": "1 +",
	})
	if status == http.StatusOK || !strings.Contains(string(body), "invalid_expression") {
		t.Fatalf("metric with an invalid expression: got = %d %s, want = an invalid_expression refusal", status, body)
	}

	// source: spec/requests/api/v1/billable_metrics_controller_spec.rb:24
	m := s.api(http.MethodPost, "/v1/billable_metrics", map[string]any{
		"name": "Sum", "code": "sum", "aggregation_type": "AGGREGATION_TYPE_SUM", "field_name": "value",
		"expression": "event.properties.a + event.properties.b",
	})
	if got := m.str("billable_metric.expression"); got != "event.properties.a + event.properties.b" {
		t.Errorf("metric expression: got = %q, want = the expression created", got)
	}
	s.api(http.MethodPost, "/v1/plans", map[string]any{
		"name": "P", "code": "p", "interval": "INTERVAL_MONTHLY", "amount": money("0"),
		"charges": []map[string]any{{
			"code": "sum", "billable_metric_id": "code:sum", "charge_model": "CHARGE_MODEL_STANDARD",
			"properties": map[string]any{"standard": map[string]any{"amount": "1"}},
		}},
	})
	s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer"})
	s.api(http.MethodPost, "/v1/subscriptions", map[string]any{"external_id": "s", "customer_id": "external_id:cust", "plan_id": "code:p"})
	s.idle()

	s.at(start.AddDate(0, 0, 3))
	// source: spec/requests/api/v1/events_controller_spec.rb:144
	ev := s.api(http.MethodPost, "/v1/events", map[string]any{
		"transaction_id": "tx-1", "external_subscription_id": "s", "code": "sum",
		"timestamp":  s.clock.Now().Format(time.RFC3339),
		"properties": map[string]any{"a": "1", "b": "2"},
	})
	if got := ev.str("event.properties.value"); got != "3.0" {
		t.Errorf("event value: got = %q, want = 3.0", got)
	}
	// source: spec/requests/api/v1/events_controller_spec.rb:164
	status, body = s.send(http.MethodPost, "/v1/events", s.key, "", map[string]any{
		"transaction_id": "tx-2", "external_subscription_id": "s", "code": "sum",
		"timestamp":  s.clock.Now().Format(time.RFC3339),
		"properties": map[string]any{"a": "1"},
	})
	// The evaluator's message is the violation's description.
	if status != http.StatusBadRequest || !strings.Contains(string(body), `"reason":"expression_evaluation_failed"`) ||
		!strings.Contains(string(body), `"description":"Variable: b not found"`) {
		t.Errorf("event without b: got = %d %s, want = 400 expression_evaluation_failed, Variable: b not found", status, body)
	}
	s.event("s", "sum", "tx-3", map[string]any{"a": 4.5, "b": "0.5"})
	s.idle()

	s.bill(start.AddDate(0, 1, 0))
	invs := s.waitInvoices("cust", 1)
	if len(invs) != 1 {
		t.Fatalf("invoices: got = %d, want = 1", len(invs))
	}
	// 3 + 5 units at 1 EUR.
	wantCents(t, "invoice", invs[0], map[string]int64{"fees_amount": 800, "total_amount": 800})
	s.noFailedJobs()
}
