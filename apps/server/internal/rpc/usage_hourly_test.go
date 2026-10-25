package rpc_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// TestGetHourlyUsage checks the refusals of the hourly usage over REST;
// its breakdown is read from the usage buckets of the PostgreSQL store in
// the cli scenario TestScenarioHourlyUsage, as the test API's event store
// keeps none.
func TestGetHourlyUsage(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	h := s.Fixtures
	c := h.Customer()
	p := h.Plan(period.Monthly, 0)
	calls := h.Charge(p, h.Metric("calls", catalog.AggregationTypeCount, ""), catalog.ChargeModelStandard, `{"amount":"1"}`)
	peak := h.Charge(p, h.Metric("peak", catalog.AggregationTypeMax, "value"), catalog.ChargeModelStandard, `{"amount":"1"}`)
	other := h.Charge(h.Plan(period.Monthly, 0), h.Metric("other", catalog.AggregationTypeCount, ""), catalog.ChargeModelStandard, `{"amount":"1"}`)
	sub := h.Subscription(c, p, billingtest.SubscribedAt(rpctest.Now.AddDate(0, 0, -5)))
	path := "/v1/customers/external_id%3A" + c.ExternalID + "/subscriptions/external_id%3A" + sub.ExternalID + "/usage:hourly?"
	ts := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }

	for _, tc := range []struct {
		name, source string
		query        url.Values
		status       int
		reason       string
	}{
		{
			name: "charge of another plan", source: "spec/graphql/resolvers/subscriptions/hourly_usage_resolver_spec.rb:141",
			query: url.Values{"charge_id": {other.ID.String()}}, status: http.StatusNotFound, reason: "charge_not_found",
		},
		{
			name: "aggregation whose hours do not add up", source: "spec/services/realtime_usage/hourly_breakdown_service_spec.rb:237",
			query: url.Values{"charge_id": {peak.ID.String()}}, status: http.StatusForbidden, reason: "feature_unavailable",
		},
		{
			// Checked before the window, as lago checks that realtime
			// usage is enabled (hourly_breakdown_service.rb:34-36).
			name:  "event store without usage buckets",
			query: url.Values{"charge_id": {calls.ID.String()}}, status: http.StatusForbidden, reason: "feature_unavailable",
		},
		{
			name: "window ending before it starts", source: "spec/services/realtime_usage/hourly_breakdown_service_spec.rb:246",
			query:  url.Values{"charge_id": {calls.ID.String()}, "from": {ts(rpctest.Now)}, "to": {ts(rpctest.Now.Add(-time.Hour))}},
			status: http.StatusBadRequest, reason: "range.order",
		},
		{
			name: "no charge", query: url.Values{}, status: http.StatusBadRequest, reason: "charge_id",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := s.REST(t, s.APIKey, http.MethodGet, path+tc.query.Encode(), nil)
			if code != tc.status || !strings.Contains(string(body), tc.reason) {
				t.Errorf("got = %d %s, want = %d with %s", code, body, tc.status, tc.reason)
			}
		})
	}
}
