package metrics_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/metrics"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
)

func setupLogged(t *testing.T) (*billingtest.Harness, *metrics.Service) {
	t.Helper()
	h := billingtest.New(t, now)
	h.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceAPI})
	s, err := metrics.New(h.Deps, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return h, s
}

// source: spec/services/billable_metrics/create_service_spec.rb:144,
// update_service_spec.rb:46, destroy_service_spec.rb:94
func TestActivityLogs(t *testing.T) {
	h, s := setupLogged(t)
	m, err := s.Create(t.Context(), h.OrgID(), metrics.CreateInput{Code: "calls", Name: "Calls", AggregationType: catalog.AggregationTypeCount})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := s.Update(t.Context(), h.OrgID(), m.ID, metrics.UpdateInput{Name: core.Some("API calls")}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if _, err := s.Delete(t.Context(), h.OrgID(), m.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	id := m.ID.UUID()
	h.WantActivities(
		billingtest.ActivityWant{Type: auditlog.ActivityBillableMetricCreated, ResourceType: auditlog.ResourceBillableMetric, ResourceID: id},
		billingtest.ActivityWant{Type: auditlog.ActivityBillableMetricUpdated, ResourceType: auditlog.ResourceBillableMetric, ResourceID: id, Changes: []string{"name"}},
		billingtest.ActivityWant{Type: auditlog.ActivityBillableMetricDeleted, ResourceType: auditlog.ResourceBillableMetric, ResourceID: id},
	)
}

// A change that fails logs nothing.
func TestActivityLogsFailure(t *testing.T) {
	h, s := setupLogged(t)
	if _, err := s.Create(t.Context(), h.OrgID(), metrics.CreateInput{Code: "", Name: "Calls", AggregationType: catalog.AggregationTypeCount}); err == nil {
		t.Fatal("Create() error = nil, want an error")
	}
	h.WantActivities()
}
