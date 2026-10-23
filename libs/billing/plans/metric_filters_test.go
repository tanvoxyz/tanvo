package plans_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func TestDestroyMetricFilters(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		discard bool
		missing bool
		want    int
	}{
		// source: spec/services/billable_metric_filters/destroy_all_service_spec.rb:18
		{name: "discarded metric", discard: true, want: 0},
		// source: spec/services/billable_metric_filters/destroy_all_service_spec.rb:25
		{name: "kept metric", want: 2},
		// source: spec/services/billable_metric_filters/destroy_all_service_spec.rb:34
		{name: "missing metric", missing: true, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, e := setup(t)
			m := h.Metric("m", catalog.AggregationTypeCount, "", func(m *catalog.BillableMetric) {
				m.Filters = []catalog.MetricFilter{
					{ID: core.NewID[core.BillableMetricFilterID](h.IDs), Key: "cloud", Values: []string{"aws"}},
					{ID: core.NewID[core.BillableMetricFilterID](h.IDs), Key: "region", Values: []string{"eu", "us"}},
				}
			})
			if tt.discard {
				h.Do(func(ctx context.Context, r port.Repos) error { return r.Metrics.Discard(ctx, h.OrgID(), m.ID, h.Now()) })
			}
			id := m.ID
			if tt.missing {
				id = core.NewID[core.BillableMetricID](h.IDs)
			}
			args, err := json.Marshal(kernel.MetricArgs{BillableMetricID: id})
			if err != nil {
				t.Fatal(err)
			}
			j := port.Job{Kind: kernel.JobDestroyMetricFilters, OrganizationID: h.OrgID(), Args: args}
			for range 2 {
				if ok, err := e.Plans.HandleJob(h.Ctx, j); !ok || err != nil {
					t.Fatalf("HandleJob() = %t, %v, want = true, nil", ok, err)
				}
			}
			got := billingtest.Read(h, func(ctx context.Context, r port.Repos) (*catalog.BillableMetric, error) {
				return r.Metrics.GetWithDiscarded(ctx, h.OrgID(), m.ID)
			})
			if len(got.Filters) != tt.want {
				t.Errorf("filters got = %d, want = %d", len(got.Filters), tt.want)
			}
		})
	}
}
