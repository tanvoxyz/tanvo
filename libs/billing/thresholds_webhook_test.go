package billing_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// TestUsageThresholdReachedWebhook ports the webhook cases of
// LifetimeUsages::CheckThresholdsService: each threshold the lifetime
// usage passes is announced once, with the subscription.
func TestUsageThresholdReachedWebhook(t *testing.T) {
	tests := []struct {
		name       string
		thresholds []int64
		before     string
		total      string
		want       []int64
	}{
		// source: spec/services/lifetime_usages/check_thresholds_service_spec.rb:42
		{name: "one threshold", thresholds: []int64{10}, total: "20", want: []int64{10}},
		// source: spec/services/lifetime_usages/check_thresholds_service_spec.rb:87, 96
		{name: "several thresholds", thresholds: []int64{10, 400}, total: "401", want: []int64{10, 400}},
		// source: spec/services/lifetime_usages/check_thresholds_service_spec.rb:142
		{name: "after a progressive billing invoice", thresholds: []int64{10, 400}, before: "20", total: "381", want: []int64{400}},
		// source: spec/services/lifetime_usages/check_thresholds_service_spec.rb:170
		{name: "no threshold passed", thresholds: []int64{3000}, total: "20"},
	}
	day0 := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newScenario(t, day0)
			c := s.Customer()
			p := s.Plan(period.Monthly, 0)
			m := s.Metric("sum_agg", catalog.AggregationTypeSum, "total")
			s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"0.01"}`)
			byID := map[core.UsageThresholdID]int64{}
			for _, a := range tt.thresholds {
				byID[s.UsageThreshold(p, a, false).ID] = a
			}
			sub := s.invCreateSub(c, p)
			s.travelTo(day0.AddDate(0, 0, 3))
			if tt.before != "" {
				s.ingest(sub, m.Code, "total", tt.before)
				s.travelTo(day0.AddDate(0, 0, 5))
			}
			s.Emitted.Reset()
			s.ingest(sub, m.Code, "total", tt.total)
			var got []int64
			for _, ev := range s.Emitted.Events() {
				if ev.Type != webhook.EventTypeSubscriptionUsageThresholdReached {
					continue
				}
				var payload struct {
					Subscription struct {
						ID core.SubscriptionID `json:"id"`
					} `json:"subscription"`
					UsageThreshold struct {
						ID core.UsageThresholdID `json:"id"`
					} `json:"usage_threshold"`
				}
				if err := json.Unmarshal(ev.Payload, &payload); err != nil {
					t.Fatalf("payload: %v", err)
				}
				if ev.ObjectID != sub.ID.UUID() {
					t.Errorf("object: got = %s, want = %s", ev.ObjectID, sub.ID)
				}
				got = append(got, byID[payload.UsageThreshold.ID])
			}
			slices.Sort(got)
			if !slices.Equal(got, tt.want) {
				t.Errorf("thresholds reached: got = %v, want = %v", got, tt.want)
			}
		})
	}
}
