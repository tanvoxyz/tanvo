package cli_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestScenarioHourlyUsage reads the hourly usage of a charge with a filter
// from the usage buckets of the PostgreSQL event store, which the first
// read defines and backfills.
//
// source: spec/graphql/resolvers/subscriptions/hourly_usage_resolver_spec.rb:72
func TestScenarioHourlyUsage(t *testing.T) {
	t.Parallel()
	day := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	at := func(h, m int) time.Time { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	s := newScenario(t, day.AddDate(0, 0, -4), scenarioOptions{})
	s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer"})
	s.api(http.MethodPost, "/v1/billable_metrics", map[string]any{
		"name": "Calls", "code": "calls", "aggregation_type": "AGGREGATION_TYPE_COUNT",
		"filters": []map[string]any{{"key": "region", "values": []string{"eu", "us"}}},
	})
	plan := s.api(http.MethodPost, "/v1/plans", map[string]any{
		"name": "Plan", "code": "plan", "interval": "INTERVAL_MONTHLY", "amount": money("0"),
		"charges": []map[string]any{{
			"code": "calls", "billable_metric_id": "code:calls", "charge_model": "CHARGE_MODEL_STANDARD",
			"properties": map[string]any{"standard": map[string]any{"amount": "1"}},
			"filters": []map[string]any{{
				"values":               map[string]any{"region": map[string]any{"values": []string{"eu"}}},
				"properties":           map[string]any{"standard": map[string]any{"amount": "2"}},
				"invoice_display_name": "Europe",
			}},
		}},
	})
	charge := plan.list("plan.charges")[0]
	filterID := charge.list("filters")[0].str("id")
	s.api(http.MethodPost, "/v1/subscriptions", map[string]any{"external_id": "cust", "customer_id": "external_id:cust", "plan_id": "code:plan"})
	s.idle()

	s.at(at(9, 45))
	for i := range 10 {
		region := "eu"
		if i >= 8 {
			region = "us"
		}
		s.api(http.MethodPost, "/v1/events", map[string]any{
			"transaction_id": fmt.Sprintf("tx-%d", i), "external_subscription_id": "cust", "code": "calls",
			"timestamp": at(9, 30+i).Format(time.RFC3339), "properties": map[string]any{"region": region},
		})
	}
	s.idle()
	s.at(at(12, 0))

	path := "/v1/customers/external_id%3Acust/subscriptions/external_id%3Acust/usage:hourly?"
	q := url.Values{"charge_id": {charge.str("id")}, "from": {at(9, 20).Format(time.RFC3339)}, "to": {at(11, 5).Format(time.RFC3339)}}
	u := s.api(http.MethodGet, path+q.Encode(), nil)
	for _, c := range []struct{ path, want string }{
		{"from", "2026-08-24T09:00:00Z"},
		{"to", "2026-08-24T11:15:00Z"},
		{"timezone", "UTC"},
		{"aggregation_type", "AGGREGATION_TYPE_COUNT"},
		// The Europe filter first, then the charge default.
		{"filters.0.charge_filter_id", filterID},
		{"filters.0.invoice_display_name", "Europe"},
		{"filters.0.values.region", "eu"},
		{"filters.0.units", "8"},
		{"filters.0.events_count", "8"},
		{"filters.1.charge_filter_id", ""},
		{"filters.1.units", "2"},
		{"hours.0.time", "2026-08-24T09:00:00Z"},
		{"hours.0.units", "10"},
		{"hours.0.breakdown.0.units", "8"},
		{"hours.0.breakdown.1.units", "2"},
		{"hours.1.units", "0"},
		{"hours.2.time", "2026-08-24T11:00:00Z"},
	} {
		if got := decimalString(u.str(c.path)); got != c.want {
			t.Errorf("%s: got = %q, want = %q", c.path, got, c.want)
		}
	}
	if n := len(u.list("hours")); n != 3 {
		t.Errorf("hours: got = %d, want = 3", n)
	}
	if got := u.str("last_ingested_at"); got == "" {
		t.Errorf("last_ingested_at: got = empty, want = the ingestion time")
	}

	// source: spec/services/realtime_usage/hourly_breakdown_service_spec.rb:210
	q.Set("to", at(9, 20).AddDate(0, 0, 40).Format(time.RFC3339))
	status, body := s.send(http.MethodGet, path+q.Encode(), s.key, "", nil)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "window_too_long") {
		t.Errorf("window too long: got = %d %s, want = 400 window_too_long", status, body)
	}
	s.noFailedJobs()
}
