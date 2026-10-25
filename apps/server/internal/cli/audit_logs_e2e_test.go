package cli_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/apps/server/internal/dockertest"
)

// TestScenarioAuditLogsRetention lists the activity and API logs of an
// organization with the default retention period of 30 days
// (audit_logs_period, db/structure.sql:3726): a log is listed until it is
// 30 days old, and hidden after (activity_logs_query.rb:44-46,
// api_logs_query.rb:40-42).
//
// source: spec/queries/activity_logs_query_spec.rb:40
// source: spec/queries/api_logs_query_spec.rb:41
func TestScenarioAuditLogsRetention(t *testing.T) {
	t.Parallel()
	start := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	s := newScenario(t, start, scenarioOptions{})
	s.api(http.MethodPost, "/v1/billable_metrics", map[string]any{
		"name": "M", "code": "m", "aggregation_type": "AGGREGATION_TYPE_COUNT",
	})
	s.idle()

	count := func(kind, activityType string) int {
		t.Helper()
		n := 0
		for _, l := range s.api(http.MethodGet, "/v1/"+kind, nil).list(kind) {
			if kind == "api_logs" || l.str("activity_type") == activityType {
				n++
			}
		}
		return n
	}
	// The API logs are written in batches off the request path.
	if err := dockertest.Wait(t.Context(), time.Minute, func(context.Context) error {
		if n := count("api_logs", ""); n != 1 {
			return fmt.Errorf("%d API logs, want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		at   time.Time
		want int
	}{
		{"on the day", start, 1},
		{"29 days later", start.AddDate(0, 0, 29), 1},
		{"31 days later", start.AddDate(0, 0, 31), 0},
	} {
		s.at(tt.at)
		if got := count("activity_logs", "billable_metric.created"); got != tt.want {
			t.Errorf("%s: billable_metric.created activity logs: got = %d, want = %d", tt.name, got, tt.want)
		}
		// The API log of the metric's creation: reads of the logs have no
		// side effects and are not logged.
		if got := count("api_logs", ""); got != tt.want {
			t.Errorf("%s: API logs: got = %d, want = %d", tt.name, got, tt.want)
		}
	}
}
