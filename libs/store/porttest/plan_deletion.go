package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// testPlanDeletion covers deleted plans (domain §7.2): a plan is never
// removed; its deleted status, stored with Update, keeps it and its
// children readable by ID, hides it from lookups by code and from lists,
// and frees its code.
//
// source: app/services/plans/destroy_service.rb:38 (plan.discard!),
// app/models/plan.rb:63 (default_scope kept) and :190 (the code index
// ignores discarded plans)
func testPlanDeletion(t *testing.T, h Harness) {
	e := newEnv(t, h)
	b, ob := e.twoOrgs()
	org := b.OrganizationID
	metric := catalogFixtureMetric(e, org, "calls")
	addOn := catalogFixtureAddOn(e, org, "setup")
	e.tx(func(ctx context.Context, r port.Repos) error {
		noErr(t, "Metrics.Create()", r.Metrics.Create(ctx, metric))
		noErr(t, "AddOns.Create()", r.AddOns.Create(ctx, addOn))
		return nil
	})
	subscribed := catalogFeaturesPlan(e, b, "premium")
	catalogFeaturesSubscription(e, b, subscribed, "sub_1")
	parent := catalogFeaturesPlan(e, b, "basic")
	child := parent.NewOverride(core.NewID[core.PlanID](e.ids), at(time.Hour))
	free := catalogFeaturesPlan(e, b, "free")
	unreferenced := catalogFeaturesPlan(e, b, "unreferenced")
	e.tx(func(ctx context.Context, r port.Repos) error {
		noErr(t, "Create() override", r.Plans.Create(ctx, child))
		noErr(t, "Charges.Create()", r.Charges.Create(ctx, catalogFixtureCharge(e, org, unreferenced.ID, metric.ID, "c")))
		noErr(t, "FixedCharges.Create()", r.FixedCharges.Create(ctx, catalogFixtureFixedCharge(e, org, unreferenced.ID, addOn.ID, "f")))
		noErr(t, "Commitments.Create()", r.Commitments.Create(ctx, catalogFeaturesCommitment(e, unreferenced, 100)))
		noErr(t, "UsageThresholds.Create()", r.UsageThresholds.Create(ctx, catalogFeaturesThreshold(e, unreferenced, nil, 100, false)))
		return nil
	})

	e.tx(func(ctx context.Context, r port.Repos) error {
		// The override is deleted first, then the plans (Plans::DestroyJob).
		for _, p := range []*catalog.Plan{child, subscribed, parent, unreferenced} {
			noErr(t, "MarkDeleted()", p.MarkDeleted(at(2*time.Hour)))
			noErr(t, "Update() to deleted", r.Plans.Update(ctx, p))
		}
		children, err := r.Plans.ListChildren(ctx, org, parent.ID)
		noErr(t, "ListChildren()", err)
		equal(t, "ListChildren() leaves out deleted overrides", children, []*catalog.Plan{})

		for _, p := range []*catalog.Plan{subscribed, unreferenced} {
			got, err := r.Plans.Get(ctx, org, p.ID)
			noErr(t, "Get() deleted", err)
			equal(t, "Get() deleted", got, p)
		}
		byIDs, err := r.Plans.ListByIDs(ctx, org, []core.PlanID{parent.ID, child.ID})
		noErr(t, "ListByIDs() deleted", err)
		equal(t, "ListByIDs() deleted", byIDs, []*catalog.Plan{parent, child})
		_, err = r.Plans.GetByCode(ctx, org, "premium")
		wantErr(t, "GetByCode() deleted", err, core.ErrNotFound)
		_, err = r.Plans.Get(ctx, ob.OrganizationID, unreferenced.ID)
		wantErr(t, "Get() deleted from another organization", err, core.ErrNotFound)

		// A deleted plan keeps its children rows, which its invoices read.
		charges, err := r.Charges.ListByPlan(ctx, org, unreferenced.ID)
		noErr(t, "Charges.ListByPlan()", err)
		equal(t, "charges of a deleted plan", len(charges), 1)
		fixed, err := r.FixedCharges.ListByPlan(ctx, org, unreferenced.ID)
		noErr(t, "FixedCharges.ListByPlan()", err)
		equal(t, "fixed charges of a deleted plan", len(fixed), 1)
		commitments, err := r.Commitments.ListByPlan(ctx, org, unreferenced.ID)
		noErr(t, "Commitments.ListByPlan()", err)
		equal(t, "commitments of a deleted plan", len(commitments), 1)
		thresholds, err := r.UsageThresholds.ListByPlan(ctx, org, unreferenced.ID)
		noErr(t, "UsageThresholds.ListByPlan()", err)
		equal(t, "usage thresholds of a deleted plan", len(thresholds), 1)

		list := func(f catalog.PlanListFilter) []core.PlanID {
			return ids(listAll(t, 1, func(p core.PageRequest) (core.Page[*catalog.Plan], error) {
				return r.Plans.List(ctx, org, f, p)
			}), catalogPlanID)
		}
		equal(t, "List() leaves out deleted plans", list(catalog.PlanListFilter{IncludeChildren: true}), []core.PlanID{free.ID})
		equal(t, "List() deleted",
			list(catalog.PlanListFilter{IncludeChildren: true, Statuses: []catalog.PlanStatus{catalog.PlanStatusDeleted}}),
			[]core.PlanID{subscribed.ID, parent.ID, child.ID, unreferenced.ID})

		// The code is free again, and the deleted plan cannot come back
		// while it is taken.
		again := catalogFixturePlan(e, org, "premium")
		noErr(t, "Create() with the code of a deleted plan", r.Plans.Create(ctx, again))
		got, err := r.Plans.GetByCode(ctx, org, "premium")
		noErr(t, "GetByCode() after reuse", err)
		equal(t, "GetByCode() after reuse", got, again)
		noErr(t, "Update() of a deleted plan with a taken code", r.Plans.Update(ctx, subscribed))
		restored := *subscribed
		restored.Status = catalog.PlanStatusActive
		wantErr(t, "Update() back to active with a taken code", r.Plans.Update(ctx, &restored), core.ErrConflict)
		return nil
	})
}
