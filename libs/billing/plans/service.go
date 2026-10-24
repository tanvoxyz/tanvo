package plans

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Terminator terminates subscriptions; lifecycle.Service implements it.
type Terminator interface {
	// Terminate terminates the subscription of in, in its own transaction.
	Terminate(ctx context.Context, in lifecycle.TerminateInput) (*subscription.Subscription, error)
}

// DraftFinalizer finalizes draft invoices; invoicing.Service implements
// it.
type DraftFinalizer interface {
	// FinalizeDraft refreshes and finalizes the draft id, in its own
	// transaction.
	FinalizeDraft(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*invoice.Invoice, error)
}

// Service deletes plans.
type Service struct {
	d      *kernel.Deps
	subs   Terminator
	drafts DraftFinalizer
}

// New returns the service over the checked dependencies d, terminating
// subscriptions with subs and finalizing drafts with drafts.
func New(d *kernel.Deps, subs Terminator, drafts DraftFinalizer) *Service {
	return &Service{d: d, subs: subs, drafts: drafts}
}

// PrepareDestroy starts the deletion of the plan id (lago
// Plans::PrepareDestroyService): in one transaction it marks the plan and
// its override plans pending deletion, enqueues a [kernel.JobDestroyPlan]
// (one at a time per plan) and emits plan.deleted. A deleted plan is not
// found, as lago only finds kept plans (app/models/plan.rb:63).
//
// source: app/services/plans/prepare_destroy_service.rb:12-25
func (s *Service) PrepareDestroy(ctx context.Context, org core.OrganizationID, id core.PlanID) (*catalog.Plan, error) {
	var out *catalog.Plan
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := r.Plans.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load plan %s: %w", id, err)
		}
		if p.Deleted() {
			return fmt.Errorf("load plan %s: %w", id, core.ErrNotFound)
		}
		out = p
		now := s.d.Now()
		children, err := r.Plans.ListChildren(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load override plans of %s: %w", id, err)
		}
		for _, c := range append([]*catalog.Plan{p}, children...) {
			if err := markPendingDeletion(ctx, r, c, now); err != nil {
				return err
			}
		}
		args := kernel.PlanArgs{PlanID: id}
		if err := kernel.Enqueue(ctx, r, org, kernel.JobDestroyPlan, args, "plan:destroy:"+id.String(), now); err != nil {
			return err
		}
		return kernel.Emit(ctx, s.d, org, webhook.EventTypePlanDeleted, "plan", p.ID.UUID(), p)
	})
	if err != nil {
		return nil, fmt.Errorf("prepare plan %s deletion: %w", id, err)
	}
	return out, nil
}

// markPendingDeletion marks p pending deletion unless it already is
// (plan.update!(pending_deletion: true) sets the flag again).
func markPendingDeletion(ctx context.Context, r port.Repos, p *catalog.Plan, now time.Time) error {
	if p.Status == catalog.PlanStatusPendingDeletion {
		return nil
	}
	if err := p.MarkPendingDeletion(now); err != nil {
		return fmt.Errorf("plan %s: %w", p.ID, err)
	}
	if err := r.Plans.Update(ctx, p); err != nil {
		return fmt.Errorf("update plan %s: %w", p.ID, err)
	}
	return nil
}

// DestroyWithChildren runs a [kernel.JobDestroyPlan] (lago
// Plans::DestroyJob): it destroys the override plans of id that are not
// deleted, then id; the first failure stops it.
//
// source: app/jobs/plans/destroy_job.rb:9-15
func (s *Service) DestroyWithChildren(ctx context.Context, org core.OrganizationID, id core.PlanID) error {
	children, err := read(ctx, s.d, func(ctx context.Context, r port.Repos) ([]*catalog.Plan, error) {
		return r.Plans.ListChildren(ctx, org, id)
	})
	if err != nil {
		return fmt.Errorf("load override plans of %s: %w", id, err)
	}
	for _, c := range children {
		if _, err := s.Destroy(ctx, org, c.ID); err != nil {
			return err
		}
	}
	_, err = s.Destroy(ctx, org, id)
	return err
}

// Destroy deletes the plan id (lago Plans::DestroyService): it terminates
// the plan's active subscriptions (with their termination invoices),
// cancels its pending ones, finalizes the draft invoices billing a
// subscription of the plan, soft-deletes the plan's entitlements and marks the
// plan deleted, keeping it with its charges, fixed charges, commitments and
// usage thresholds. Each step runs in its own transaction; a termination or
// finalization refused by a rule (*core.RuleError) is logged and skipped,
// as lago ignores the failed result. A deleted plan is returned once the
// steps ran.
//
// source: app/services/plans/destroy_service.rb:17-48
func (s *Service) Destroy(ctx context.Context, org core.OrganizationID, id core.PlanID) (*catalog.Plan, error) {
	if _, err := read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.Plan, error) {
		p, err := r.Plans.Get(ctx, org, id)
		if err != nil {
			return nil, err
		}
		return p, contractLock(ctx, r, p)
	}); err != nil {
		return nil, fmt.Errorf("destroy plan %s: %w", id, err)
	}
	if err := s.terminateActive(ctx, org, id); err != nil {
		return nil, fmt.Errorf("destroy plan %s: %w", id, err)
	}
	if err := s.cancelPending(ctx, org, id); err != nil {
		return nil, fmt.Errorf("destroy plan %s: %w", id, err)
	}
	if err := s.finalizeDrafts(ctx, org, id); err != nil {
		return nil, fmt.Errorf("destroy plan %s: %w", id, err)
	}
	var out *catalog.Plan
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := r.Plans.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load plan: %w", err)
		}
		out = p
		ents, err := r.Entitlements.ListByPlan(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load entitlements: %w", err)
		}
		// source: app/services/plans/destroy_service.rb:32-34
		now := s.d.Now()
		for _, e := range ents {
			if err := r.Entitlements.Discard(ctx, org, e.ID, now); err != nil {
				return fmt.Errorf("discard entitlement %s: %w", e.ID, err)
			}
		}
		// source: app/services/catalog_plans/destroy_service.rb:27-30
		if _, err := r.PlanRateCards.DiscardByPlan(ctx, org, id, now); err != nil {
			return fmt.Errorf("discard plan rate cards: %w", err)
		}
		if !p.Deleted() {
			if err := p.MarkDeleted(now); err != nil {
				return err
			}
			if err := r.Plans.Update(ctx, p); err != nil {
				return fmt.Errorf("update plan: %w", err)
			}
		}
		// A plan already deleted is logged again, as lago's destroy
		// succeeds on it (destroy_service.rb:42-47).
		//
		// source: app/services/plans/destroy_service.rb:12-15
		return s.logPlan(ctx, r, auditlog.ActivityPlanDeleted, nil, p)
	})
	if err != nil {
		return nil, fmt.Errorf("destroy plan %s: %w", id, err)
	}
	return out, nil
}

// contractLock fails with plan plan_locked when a contract, of any
// status, uses the plan p (lago CatalogPlan#attached_to_contracts?): its
// contracts must move to another plan first.
//
// source: app/services/catalog_plans/destroy_service.rb:24-26,
// app/services/catalog_plans/update_service.rb:26-28
func contractLock(ctx context.Context, r port.Repos, p *catalog.Plan) error {
	ok, err := r.Contracts.ExistsForPlan(ctx, p.OrganizationID, p.ID)
	if err != nil {
		return fmt.Errorf("find contracts of plan %s: %w", p.ID, err)
	}
	if ok {
		return &core.ValidationError{Errors: []core.FieldError{{Field: "plan", Code: catalog.CodePlanLocked}}}
	}
	return nil
}

// rateCardsLock fails with currency not_editable_with_applied_rate_cards
// when the plan p applies a kept rate card.
//
// source: app/services/catalog_plans/update_service.rb:30-34
func rateCardsLock(ctx context.Context, r port.Repos, p *catalog.Plan) error {
	page, err := r.PlanRateCards.List(ctx, p.OrganizationID, catalog.PlanRateCardQuery{PlanID: p.ID}, core.PageRequest{Size: 1})
	if err != nil {
		return fmt.Errorf("list rate cards of plan %s: %w", p.ID, err)
	}
	if len(page.Items) > 0 {
		return &core.ValidationError{Errors: []core.FieldError{{Field: "currency", Code: catalog.CodeNotEditableWithAppliedRateCards}}}
	}
	return nil
}

// terminateActive terminates the active subscriptions of the plan id.
//
// source: app/services/plans/destroy_service.rb:20-23
func (s *Service) terminateActive(ctx context.Context, org core.OrganizationID, id core.PlanID) error {
	subs, err := s.livingSubscriptions(ctx, org, id, subscription.StatusActive)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		_, err := s.subs.Terminate(ctx, lifecycle.TerminateInput{OrganizationID: org, SubscriptionID: sub.ID})
		if err := s.skipRule(ctx, err, "subscription", sub.ID.String()); err != nil {
			return fmt.Errorf("terminate subscription %s: %w", sub.ID, err)
		}
	}
	return nil
}

// cancelPending cancels the pending subscriptions of the plan id, without
// webhooks (subscription.mark_as_canceled!).
//
// source: app/services/plans/destroy_service.rb:25-26, app/models/subscription.rb:139-142
func (s *Service) cancelPending(ctx context.Context, org core.OrganizationID, id core.PlanID) error {
	subs, err := s.livingSubscriptions(ctx, org, id, subscription.StatusPending)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			if err := kernel.LockSubscription(ctx, r, org, sub.ID); err != nil {
				return err
			}
			cur, err := r.Subscriptions.Get(ctx, org, sub.ID)
			if err != nil {
				return fmt.Errorf("load subscription: %w", err)
			}
			if cur.Status != subscription.StatusPending {
				return nil
			}
			// mark_as_canceled! records no reason.
			if err := cur.Cancel(s.d.Now(), ""); err != nil {
				return err
			}
			if err := r.Subscriptions.Update(ctx, cur); err != nil {
				return fmt.Errorf("update subscription: %w", err)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("cancel subscription %s: %w", sub.ID, err)
		}
	}
	return nil
}

// finalizeDrafts finalizes the draft invoices billing a subscription of
// the plan id.
//
// source: app/services/plans/destroy_service.rb:28-30
func (s *Service) finalizeDrafts(ctx context.Context, org core.OrganizationID, id core.PlanID) error {
	drafts, err := read(ctx, s.d, func(ctx context.Context, r port.Repos) ([]*invoice.Invoice, error) {
		subs, err := kernel.All(func(p core.PageRequest) (core.Page[*subscription.Subscription], error) {
			return r.Subscriptions.List(ctx, org, subscription.Filter{PlanID: id}, p)
		})
		if err != nil || len(subs) == 0 {
			return nil, err
		}
		ids := make([]core.SubscriptionID, 0, len(subs))
		for _, sub := range subs {
			ids = append(ids, sub.ID)
		}
		return kernel.All(func(p core.PageRequest) (core.Page[*invoice.Invoice], error) {
			return r.Invoices.List(ctx, org, invoice.Filter{Statuses: []invoice.Status{invoice.StatusDraft}, SubscriptionIDs: ids}, p)
		})
	})
	if err != nil {
		return fmt.Errorf("load draft invoices: %w", err)
	}
	for _, inv := range drafts {
		_, err := s.drafts.FinalizeDraft(ctx, org, inv.ID)
		if err := s.skipRule(ctx, err, "invoice", inv.ID.String()); err != nil {
			return fmt.Errorf("finalize invoice %s: %w", inv.ID, err)
		}
	}
	return nil
}

// livingSubscriptions returns the subscriptions of the plan id in status
// st (active or pending).
func (s *Service) livingSubscriptions(ctx context.Context, org core.OrganizationID, id core.PlanID, st subscription.Status) ([]*subscription.Subscription, error) {
	subs, err := read(ctx, s.d, func(ctx context.Context, r port.Repos) ([]*subscription.Subscription, error) {
		return r.Subscriptions.ListActiveOrPendingByPlan(ctx, org, id)
	})
	if err != nil {
		return nil, fmt.Errorf("load subscriptions: %w", err)
	}
	out := subs[:0]
	for _, sub := range subs {
		if sub.Status == st {
			out = append(out, sub)
		}
	}
	return out, nil
}

// skipRule logs and drops a *core.RuleError, and returns any other error.
func (s *Service) skipRule(ctx context.Context, err error, object, id string) error {
	var re *core.RuleError
	if !errors.As(err, &re) {
		return err
	}
	s.d.Logger.WarnContext(ctx, "plan deletion: step refused", slog.String("object", object), slog.String("id", id), slog.String("code", re.Code))
	return nil
}

// HandleJob runs a catalog job (a [kernel.JobDestroyPlan], or a job of
// the plan, charge and fixed-charge cascades) and reports whether j is one.
func (s *Service) HandleJob(ctx context.Context, j port.Job) (bool, error) {
	for _, hs := range []map[string]func(context.Context, port.Job) error{s.planJobs(), s.chargeJobs(), s.fixedChargeJobs()} {
		if h, ok := hs[j.Kind]; ok {
			return true, h(ctx, j)
		}
	}
	if j.Kind != kernel.JobDestroyPlan {
		return false, nil
	}
	var a kernel.PlanArgs
	if err := json.Unmarshal(j.Args, &a); err != nil {
		return true, fmt.Errorf("decode %s args: %w", j.Kind, err)
	}
	return true, s.DestroyWithChildren(ctx, j.OrganizationID, a.PlanID)
}

// read runs fn in a transaction of d and returns its result.
func read[T any](ctx context.Context, d *kernel.Deps, fn func(ctx context.Context, r port.Repos) (T, error)) (T, error) {
	var out T
	err := d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		v, err := fn(ctx, r)
		out = v
		return err
	})
	return out, err
}

// logPlan records the activity log t of plan p, whose attributes before an
// update are before. A plan's log names no customer.
func (s *Service) logPlan(ctx context.Context, r port.Repos, t auditlog.ActivityType, before map[string]any, p *catalog.Plan) error {
	return s.d.LogChange(ctx, r, kernel.Change{
		Type: t, OrganizationID: p.OrganizationID, ResourceType: auditlog.ResourcePlan, ResourceID: p.ID.UUID(),
		Object: p, Before: before,
	})
}
