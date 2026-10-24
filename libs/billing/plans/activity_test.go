package plans_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// setupLogged returns a harness recording activity logs and its engine.
func setupLogged(t *testing.T) (*billingtest.Harness, *billing.Engine) {
	t.Helper()
	h := billingtest.New(t, now)
	h.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceAPI})
	e, err := billing.New(*h.Deps)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	return h, e
}

// source: spec/services/plans/create_service_spec.rb:147,
// update_service_spec.rb:262, destroy_service_spec.rb:31,
// spec/services/catalog_plans/create_service_spec.rb:45,
// update_service_spec.rb:27
func TestActivityLogs(t *testing.T) {
	h, e := setupLogged(t)
	p, err := e.Plans.Create(h.Ctx, h.OrgID(), createInput())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{Name: core.Some("Renamed")}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if _, err := e.Plans.Destroy(h.Ctx, h.OrgID(), p.ID); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	id := p.ID.UUID()
	h.WantActivities(
		billingtest.ActivityWant{Type: auditlog.ActivityPlanCreated, ResourceType: auditlog.ResourcePlan, ResourceID: id},
		billingtest.ActivityWant{Type: auditlog.ActivityPlanUpdated, ResourceType: auditlog.ResourcePlan, ResourceID: id, Changes: []string{"name"}},
		billingtest.ActivityWant{Type: auditlog.ActivityPlanDeleted, ResourceType: auditlog.ResourcePlan, ResourceID: id},
	)
}

// The update of an override plan is not logged.
//
// source: spec/services/plans/update_service_spec.rb:266-274
func TestActivityLogsOverridePlan(t *testing.T) {
	h, e := setupLogged(t)
	parent := h.Plan(period.Monthly, 1000)
	child := h.Plan(period.Monthly, 900, overrideOf(parent))
	if _, err := e.Plans.Update(h.Ctx, h.OrgID(), child.ID, plans.UpdateInput{Name: core.Some("Renamed")}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	h.WantActivities()
}

// A change that fails logs nothing.
//
// source: spec/services/catalog_plans/create_service_spec.rb:62,
// update_service_spec.rb:52
func TestActivityLogsFailure(t *testing.T) {
	h, e := setupLogged(t)
	in := createInput()
	in.Name = ""
	if _, err := e.Plans.Create(h.Ctx, h.OrgID(), in); err == nil {
		t.Fatal("Create() error = nil, want an error")
	}
	p := h.Plan(period.Monthly, 1000)
	if _, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{Name: core.Some("")}); err == nil {
		t.Fatal("Update() error = nil, want an error")
	}
	h.WantActivities()
}
