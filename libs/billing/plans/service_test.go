package plans_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var now = time.Date(2024, 3, 15, 10, 0, 0, 0, time.UTC)

// setup returns a harness and its engine.
func setup(t *testing.T) (*billingtest.Harness, *billing.Engine) {
	t.Helper()
	h := billingtest.New(t, now)
	e, err := billing.New(*h.Deps)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	return h, e
}

func reloadPlan(h *billingtest.Harness, id core.PlanID) *catalog.Plan {
	h.T.Helper()
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) (*catalog.Plan, error) {
		return r.Plans.Get(ctx, h.OrgID(), id)
	})
}

func reloadSubscription(h *billingtest.Harness, id core.SubscriptionID) *subscription.Subscription {
	h.T.Helper()
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) (*subscription.Subscription, error) {
		return r.Subscriptions.Get(ctx, h.OrgID(), id)
	})
}

// destroyJobs returns the pending plan destroy jobs.
func destroyJobs(h *billingtest.Harness) []port.Job {
	return slices.DeleteFunc(h.Store.Jobs(), func(j port.Job) bool { return j.Kind != kernel.JobDestroyPlan })
}

func pendingDeletion(p *catalog.Plan) { p.Status = catalog.PlanStatusPendingDeletion }

func overrideOf(parent *catalog.Plan) func(*catalog.Plan) {
	return func(p *catalog.Plan) { p.ParentID = parent.ID; p.Code = parent.Code }
}

func TestPrepareDestroy(t *testing.T) {
	// source: spec/services/plans/prepare_destroy_service_spec.rb:13
	t.Run("sets pending deletion on the plan and its override plans", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		child := h.Plan(period.Monthly, 900, overrideOf(p))
		got, err := e.Plans.PrepareDestroy(h.Ctx, h.OrgID(), p.ID)
		if err != nil {
			t.Fatalf("PrepareDestroy() error = %v", err)
		}
		// source: spec/services/plans/prepare_destroy_service_spec.rb:24
		if got.ID != p.ID {
			t.Errorf("plan = %s, want = %s", got.ID, p.ID)
		}
		for _, id := range []core.PlanID{p.ID, child.ID} {
			if st := reloadPlan(h, id).Status; st != catalog.PlanStatusPendingDeletion {
				t.Errorf("plan %s status = %s, want = pending_deletion", id, st)
			}
		}
	})

	// source: spec/services/plans/prepare_destroy_service_spec.rb:18
	t.Run("enqueues the destroy job and the plan.deleted webhook", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		if _, err := e.Plans.PrepareDestroy(h.Ctx, h.OrgID(), p.ID); err != nil {
			t.Fatalf("PrepareDestroy() error = %v", err)
		}
		jobs := destroyJobs(h)
		if len(jobs) != 1 {
			t.Fatalf("jobs = %v, want = [%s]", h.JobKinds(), kernel.JobDestroyPlan)
		}
		var a kernel.PlanArgs
		if err := json.Unmarshal(jobs[0].Args, &a); err != nil || a.PlanID != p.ID {
			t.Errorf("job args = %s (%v), want = plan %s", jobs[0].Args, err, p.ID)
		}
		if got, want := h.Webhooks(), []webhook.EventType{webhook.EventTypePlanDeleted}; !slices.Equal(got, want) {
			t.Errorf("webhooks = %v, want = %v", got, want)
		}
	})

	// source: spec/jobs/plans/destroy_job_spec.rb:16
	t.Run("enqueues one destroy job per plan", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		for range 2 {
			if _, err := e.Plans.PrepareDestroy(h.Ctx, h.OrgID(), p.ID); err != nil {
				t.Fatalf("PrepareDestroy() error = %v", err)
			}
		}
		if got := len(destroyJobs(h)); got != 1 {
			t.Errorf("destroy jobs = %d, want = 1", got)
		}
	})

	// source: spec/services/plans/prepare_destroy_service_spec.rb:29
	t.Run("not found", func(t *testing.T) {
		tests := []struct {
			name string
			id   func(h *billingtest.Harness) core.PlanID
		}{
			{"missing plan", func(h *billingtest.Harness) core.PlanID { return core.NewID[core.PlanID](h.IDs) }},
			{"deleted plan", func(h *billingtest.Harness) core.PlanID {
				return h.Plan(period.Monthly, 1000, func(p *catalog.Plan) { p.Status = catalog.PlanStatusDeleted }).ID
			}},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				h, e := setup(t)
				_, err := e.Plans.PrepareDestroy(h.Ctx, h.OrgID(), tc.id(h))
				if !errors.Is(err, core.ErrNotFound) {
					t.Errorf("PrepareDestroy() error = %v, want = %v", err, core.ErrNotFound)
				}
				if got := len(destroyJobs(h)); got != 0 {
					t.Errorf("destroy jobs = %d, want = 0", got)
				}
			})
		}
	})
}

func TestDestroy(t *testing.T) {
	// source: spec/services/plans/destroy_service_spec.rb:17
	// source: spec/services/plans/destroy_service_spec.rb:24
	t.Run("marks the plan deleted and keeps it", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000, pendingDeletion)
		got, err := e.Plans.Destroy(h.Ctx, h.OrgID(), p.ID)
		if err != nil {
			t.Fatalf("Destroy() error = %v", err)
		}
		if got.Status != catalog.PlanStatusDeleted {
			t.Errorf("result status = %s, want = deleted", got.Status)
		}
		if st := reloadPlan(h, p.ID).Status; st != catalog.PlanStatusDeleted {
			t.Errorf("stored status = %s, want = deleted", st)
		}
	})

	// source: spec/services/plans/destroy_service_spec.rb:34
	t.Run("not found", func(t *testing.T) {
		h, e := setup(t)
		_, err := e.Plans.Destroy(h.Ctx, h.OrgID(), core.NewID[core.PlanID](h.IDs))
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("Destroy() error = %v, want = %v", err, core.ErrNotFound)
		}
	})

	// source: spec/services/plans/destroy_service_spec.rb:45
	t.Run("terminates the active subscriptions", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000, pendingDeletion)
		c := h.Customer()
		subs := []*subscription.Subscription{
			h.Subscription(c, p, billingtest.SubscribedAt(now.AddDate(0, 0, -10))),
			h.Subscription(c, p, billingtest.SubscribedAt(now.AddDate(0, 0, -5))),
		}
		if _, err := e.Plans.Destroy(h.Ctx, h.OrgID(), p.ID); err != nil {
			t.Fatalf("Destroy() error = %v", err)
		}
		for _, s := range subs {
			if st := reloadSubscription(h, s.ID).Status; st != subscription.StatusTerminated {
				t.Errorf("subscription %s status = %s, want = terminated", s.ID, st)
			}
		}
	})

	// source: spec/services/plans/destroy_service_spec.rb:61
	t.Run("cancels the pending subscriptions", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000, pendingDeletion)
		c := h.Customer()
		subs := []*subscription.Subscription{
			h.Subscription(c, p, billingtest.SubscribedAt(now.AddDate(0, 1, 0))),
			h.Subscription(c, p, billingtest.SubscribedAt(now.AddDate(0, 2, 0))),
		}
		if _, err := e.Plans.Destroy(h.Ctx, h.OrgID(), p.ID); err != nil {
			t.Fatalf("Destroy() error = %v", err)
		}
		for _, s := range subs {
			got := reloadSubscription(h, s.ID)
			if got.Status != subscription.StatusCanceled {
				t.Errorf("subscription %s status = %s, want = canceled", s.ID, got.Status)
			}
			// mark_as_canceled! records no reason (app/models/subscription.rb:139-142).
			if got.CancellationReason != "" {
				t.Errorf("subscription %s reason = %q, want = none", s.ID, got.CancellationReason)
			}
		}
	})

	// source: spec/services/plans/destroy_service_spec.rb:77
	t.Run("finalizes the draft invoices", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000, pendingDeletion)
		c := h.Customer()
		s := h.Subscription(c, p, billingtest.SubscribedAt(now.AddDate(0, 0, -10)))
		reasons := []invoice.InvoicingReason{invoice.InvoicingReasonSubscriptionStarting, invoice.InvoicingReasonSubscriptionPeriodic}
		var drafts []*invoice.Invoice
		for _, reason := range reasons {
			inv := h.StoredInvoice(c, func(i *invoice.Invoice) { i.Status = invoice.StatusDraft })
			h.LinkSubscription(inv, s, reason, now.AddDate(0, 0, -10), now.AddDate(0, 0, 20))
			drafts = append(drafts, inv)
		}
		if _, err := e.Plans.Destroy(h.Ctx, h.OrgID(), p.ID); err != nil {
			t.Fatalf("Destroy() error = %v", err)
		}
		for _, inv := range drafts {
			if st := h.Invoice(inv.ID).Status; st != invoice.StatusFinalized {
				t.Errorf("invoice %s status = %s, want = finalized", inv.ID, st)
			}
		}
	})

	// source: spec/services/plans/destroy_service_spec.rb:97
	t.Run("discards the entitlements", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000, pendingDeletion)
		f := &catalog.Feature{
			ID: core.NewID[core.FeatureID](h.IDs), OrganizationID: h.OrgID(), Code: "seats",
			CreatedAt: now, UpdatedAt: now,
		}
		ent := &catalog.Entitlement{
			ID: core.NewID[core.EntitlementID](h.IDs), OrganizationID: h.OrgID(), FeatureID: f.ID, PlanID: p.ID,
			CreatedAt: now, UpdatedAt: now,
		}
		h.Do(func(ctx context.Context, r port.Repos) error {
			if err := r.Features.Create(ctx, f); err != nil {
				return err
			}
			return r.Entitlements.Create(ctx, ent)
		})
		if _, err := e.Plans.Destroy(h.Ctx, h.OrgID(), p.ID); err != nil {
			t.Fatalf("Destroy() error = %v", err)
		}
		got := billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*catalog.Entitlement, error) {
			return r.Entitlements.ListByPlan(ctx, h.OrgID(), p.ID)
		})
		if len(got) != 0 {
			t.Errorf("kept entitlements = %d, want = 0", len(got))
		}
		h.Do(func(ctx context.Context, r port.Repos) error {
			if _, err := r.Entitlements.Get(ctx, h.OrgID(), ent.ID); !errors.Is(err, core.ErrNotFound) {
				t.Errorf("Get(discarded) error = %v, want = %v", err, core.ErrNotFound)
			}
			return nil
		})
	})

	// source: spec/services/plans/destroy_service_spec.rb:113
	t.Run("an already deleted plan", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000, func(p *catalog.Plan) { p.Status = catalog.PlanStatusDeleted })
		got, err := e.Plans.Destroy(h.Ctx, h.OrgID(), p.ID)
		if err != nil {
			t.Fatalf("Destroy() error = %v", err)
		}
		if got.ID != p.ID || got.Status != catalog.PlanStatusDeleted {
			t.Errorf("plan = %s %s, want = %s deleted", got.ID, got.Status, p.ID)
		}
	})
}

// recorder records the plans of the subscriptions it terminates.
type recorder struct {
	h     *billingtest.Harness
	plans []core.PlanID
	err   error
}

func (r *recorder) Terminate(_ context.Context, in lifecycle.TerminateInput) (*subscription.Subscription, error) {
	s := reloadSubscription(r.h, in.SubscriptionID)
	r.plans = append(r.plans, s.PlanID)
	return s, r.err
}

func (r *recorder) FinalizeDraft(context.Context, core.OrganizationID, core.InvoiceID) (*invoice.Invoice, error) {
	return nil, nil
}

func TestDestroyJob(t *testing.T) {
	// source: spec/jobs/plans/destroy_job_spec.rb:39
	t.Run("destroys the override plans first", func(t *testing.T) {
		h := billingtest.New(t, now)
		rec := &recorder{h: h}
		svc := plans.New(h.Deps, rec, rec)
		p := h.Plan(period.Monthly, 1000, pendingDeletion)
		child := h.Plan(period.Monthly, 900, overrideOf(p), pendingDeletion)
		c := h.Customer()
		h.Subscription(c, p, billingtest.SubscribedAt(now.AddDate(0, 0, -10)))
		h.Subscription(c, child, billingtest.SubscribedAt(now.AddDate(0, 0, -10)))
		args, err := json.Marshal(kernel.PlanArgs{PlanID: p.ID})
		if err != nil {
			t.Fatal(err)
		}
		ok, err := svc.HandleJob(h.Ctx, port.Job{OrganizationID: h.OrgID(), Kind: kernel.JobDestroyPlan, Args: args})
		if !ok || err != nil {
			t.Fatalf("HandleJob() = %t, %v, want = true, nil", ok, err)
		}
		if want := []core.PlanID{child.ID, p.ID}; !slices.Equal(rec.plans, want) {
			t.Errorf("terminated plans = %v, want = %v", rec.plans, want)
		}
		for _, id := range []core.PlanID{p.ID, child.ID} {
			if st := reloadPlan(h, id).Status; st != catalog.PlanStatusDeleted {
				t.Errorf("plan %s status = %s, want = deleted", id, st)
			}
		}
	})

	// A refused termination is skipped, as lago ignores the result
	// (app/services/plans/destroy_service.rb:21-23); another failure stops
	// the job (spec/jobs/plans/destroy_job_spec.rb:54).
	t.Run("termination failures", func(t *testing.T) {
		tests := []struct {
			name        string
			err         error
			wantErr     bool
			wantDeleted bool
		}{
			{"refused by a rule", core.NewRuleError("not_terminable", "refused"), false, true},
			{"failed", errors.New("boom"), true, false},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				h := billingtest.New(t, now)
				rec := &recorder{h: h, err: tc.err}
				svc := plans.New(h.Deps, rec, rec)
				p := h.Plan(period.Monthly, 1000, pendingDeletion)
				h.Subscription(h.Customer(), p, billingtest.SubscribedAt(now.AddDate(0, 0, -10)))
				err := svc.DestroyWithChildren(h.Ctx, h.OrgID(), p.ID)
				if (err != nil) != tc.wantErr {
					t.Errorf("DestroyWithChildren() error = %v, want error = %t", err, tc.wantErr)
				}
				if got := reloadPlan(h, p.ID).Deleted(); got != tc.wantDeleted {
					t.Errorf("deleted = %t, want = %t", got, tc.wantDeleted)
				}
			})
		}
	})

	t.Run("other jobs", func(t *testing.T) {
		h := billingtest.New(t, now)
		svc := plans.New(h.Deps, &recorder{h: h}, &recorder{h: h})
		if ok, err := svc.HandleJob(h.Ctx, port.Job{Kind: "billing.other"}); ok || err != nil {
			t.Errorf("HandleJob() = %t, %v, want = false, nil", ok, err)
		}
	})
}
