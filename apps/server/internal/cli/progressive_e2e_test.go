package cli_test

import (
	"net/http"
	"testing"
	"time"
)

// TestScenarioProgressiveBilling invoices the usage of a subscription
// when it crosses the plan's threshold, then credits that invoice on the
// period's invoice, or on the termination invoice.
//
// source: spec/scenarios/invoices/progressive_billing_spec.rb:23
// source: spec/scenarios/invoices/progressive_billing_spec.rb:104
func TestScenarioProgressiveBilling(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		// events are the days after the start with an event of 1,000,000
		// units (200 EUR each).
		events []int
		// terminate is the day of the termination; 0 bills the period.
		terminate int
		// The last invoice's total, fees and progressive billing credit.
		total, fees, credit int64
	}{
		{"period end", []int{5, 15}, 0, 3100 + 20_000, 3100 + 40_000, 20_000},
		{"termination", []int{15}, 17, 1800, 21_800, 20_000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			start := time.Date(2022, 12, 1, 0, 0, 0, 0, time.UTC)
			s := newScenario(t, start, scenarioOptions{})
			s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer"})
			s.api(http.MethodPost, "/v1/billable_metrics", map[string]any{
				"name": "Total", "code": "total", "aggregation_type": "AGGREGATION_TYPE_SUM", "field_name": "total",
			})
			s.api(http.MethodPost, "/v1/plans", map[string]any{
				"name": "Plan", "code": "plan", "interval": "INTERVAL_MONTHLY", "amount": map[string]any{"currency": "EUR", "amount": "31"},
				"charges": []map[string]any{{
					"code": "total", "billable_metric_id": "code:total", "charge_model": "CHARGE_MODEL_STANDARD",
					"properties": map[string]any{"standard": map[string]any{"amount": "0.0002"}},
				}},
				"usage_thresholds": []map[string]any{{"amount": "200"}},
			})
			s.api(http.MethodPost, "/v1/subscriptions", map[string]any{"external_id": "cust", "customer_id": "external_id:cust", "plan_id": "code:plan"})
			s.idle()

			for i, day := range tt.events {
				s.at(start.AddDate(0, 0, day))
				s.event("cust", "total", "tx-"+string(rune('a'+i)), map[string]any{"total": 1_000_000})
				s.idle()
				s.usageUpdate()
				invs := s.waitInvoices("cust", 1)
				if len(invs) != 1 {
					t.Fatalf("invoices after the event of day %d: got = %d, want = 1", day, len(invs))
				}
				if got := invs[0].cents("total_amount"); got != 20_000 {
					t.Errorf("progressive billing invoice total after day %d: got = %d, want = 20000", day, got)
				}
				if got := invs[0].str("invoice_type"); got != "INVOICE_TYPE_PROGRESSIVE_BILLING" {
					t.Errorf("progressive billing invoice type: got = %s, want = INVOICE_TYPE_PROGRESSIVE_BILLING", got)
				}
			}

			if tt.terminate == 0 {
				s.bill(start.AddDate(0, 1, 0))
			} else {
				s.at(start.AddDate(0, 0, tt.terminate))
				s.api(http.MethodPost, "/v1/subscriptions/external_id%3Acust:terminate", map[string]any{})
				s.idle()
			}
			invs := s.waitInvoices("cust", 2)
			if len(invs) != 2 {
				t.Fatalf("invoices: got = %d, want = 2", len(invs))
			}
			inv := invs[1]
			for path, want := range map[string]int64{"total_amount": tt.total, "fees_amount": tt.fees, "progressive_billing_credit_amount": tt.credit} {
				if got := inv.cents(path); got != want {
					t.Errorf("last invoice %s: got = %d, want = %d", path, got, want)
				}
			}
			s.noFailedJobs()
		})
	}
}
