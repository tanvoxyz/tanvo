package subscriptions

import (
	"context"
	"fmt"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// ChangePlanInput is the request of [Service.ChangePlan].
type ChangePlanInput struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// Subscription is the active or future subscription.
	Subscription SubscriptionRef
	// Plan is the new original plan.
	Plan refs.Ref
	// Name, when set and not blank, is the new subscription's name; the
	// current one otherwise.
	Name *string
	// PlanOverrides, when not nil, move the subscription onto an override
	// plan of Plan with these values; a subscription starting in the
	// future moves to Plan itself.
	PlanOverrides *plans.OverrideInput
	// Connections, keyed by category, choose the connections of the
	// subscription on the new plan (domain §15.5).
	Connections map[string]kernel.ConnectionChoice
}

// PlanChange is the result of a plan change.
type PlanChange struct {
	// Subscription is the subscription on the new plan: active for an
	// upgrade, pending for a downgrade, or the current one changed in
	// place when it starts in the future.
	Subscription *View
	// Previous is the subscription it replaces, reloaded; nil when the
	// plan changed in place.
	Previous *View
}

// ChangePlan moves a subscription to another plan (lago
// Subscriptions::CreateService with an existing external ID, then
// PlanUpgradeService or PlanDowngradeService).
//
// source: app/services/subscriptions/create_service.rb:59, app/services/subscriptions/plan_upgrade_service.rb, app/services/subscriptions/plan_downgrade_service.rb
func (s *Service) ChangePlan(ctx context.Context, in ChangePlanInput) (*PlanChange, error) {
	org := in.OrganizationID
	var out PlanChange
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		cur, err := findSubscriptionIn(ctx, r, org, in.Subscription, subscription.StatusActive, subscription.StatusPending)
		if err != nil {
			return err
		}
		plan, err := findParentPlan(ctx, r, org, in.Plan)
		if err != nil {
			return err
		}
		// validate_service.rb:105-113.
		var v core.Validator
		kernel.ValidateConnections(&v, in.Connections)
		if err := v.Err(); err != nil {
			return err
		}
		if in.PlanOverrides != nil && cur.PlanID != plan.ID && !cur.StartingInTheFuture() {
			// plan_upgrade_service.rb:57, plan_downgrade_service.rb:36; a
			// subscription starting in the future moves to plan itself
			// (plan_upgrade_service.rb:77-80, plan_downgrade_service.rb:82-84).
			if plan, err = s.plans.Override(ctx, r, plan, *in.PlanOverrides, nil); err != nil {
				return err
			}
		}
		ci := lifecycle.ChangePlanInput{OrganizationID: org, SubscriptionID: cur.ID, PlanID: plan.ID}
		if name := strings.TrimSpace(ptrString(in.Name)); name != "" {
			ci.Name = &name
		}
		next, err := s.life.ChangePlan(ctx, ci)
		if err != nil {
			return err
		}
		// create_service.rb:89 on the upgraded subscription or the one
		// changed in place, plan_downgrade_service.rb:64 on the pending
		// downgrade: the subscription on the new plan.
		if err := kernel.AttachConnections(ctx, s.d, r, org, next.CustomerID, integration.ObjectOwner{SubscriptionID: next.ID}, in.Connections); err != nil {
			return err
		}
		now := s.d.Now()
		if out.Subscription, err = view(ctx, r, next, now); err != nil {
			return err
		}
		if next.ID == cur.ID {
			return nil
		}
		prev, err := r.Subscriptions.Get(ctx, org, cur.ID)
		if err != nil {
			return fmt.Errorf("load subscription %s: %w", cur.ID, err)
		}
		out.Previous, err = view(ctx, r, prev, now)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("change subscription plan: %w", err)
	}
	return &out, nil
}

// TerminateInput is the request of [Service.Terminate].
type TerminateInput struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// Subscription is the terminated subscription; an external ID selects
	// the active one.
	Subscription SubscriptionRef
	// OnTerminationInvoice, when set, overrides the subscription's
	// setting.
	OnTerminationInvoice subscription.TerminationInvoice
	// OnTerminationCreditNote, when set, overrides the subscription's
	// setting.
	OnTerminationCreditNote subscription.TerminationCreditNote
}

// Terminate terminates a subscription now (lago
// Subscriptions::TerminateService); a pending or incomplete one
// referenced by ID is canceled.
//
// source: app/controllers/api/v1/subscriptions_controller.rb:98, app/services/subscriptions/terminate_service.rb
func (s *Service) Terminate(ctx context.Context, in TerminateInput) (*View, error) {
	return s.end(ctx, in.OrganizationID, in.Subscription, lifecycle.TerminateInput{
		OnTerminationInvoice: in.OnTerminationInvoice, OnTerminationCreditNote: in.OnTerminationCreditNote,
	}, true, subscription.StatusActive)
}

// Cancel cancels a pending or incomplete subscription (lago
// Subscriptions::TerminateService on a pending subscription, selected with
// status=pending); an external ID selects the pending one, else the
// incomplete one.
//
// source: app/controllers/api/v1/subscriptions_controller.rb:151, app/services/subscriptions/terminate_service.rb:20
func (s *Service) Cancel(ctx context.Context, org core.OrganizationID, ref SubscriptionRef) (*View, error) {
	return s.end(ctx, org, ref, lifecycle.TerminateInput{}, false, subscription.StatusPending, subscription.StatusIncomplete)
}

// end runs the termination of the subscription ref designates among the
// statuses, or in any status for an ID when anyByID.
func (s *Service) end(ctx context.Context, org core.OrganizationID, ref SubscriptionRef, ti lifecycle.TerminateInput, anyByID bool, statuses ...subscription.Status) (*View, error) {
	var out *View
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var (
			sub *subscription.Subscription
			err error
		)
		if ref.ExternalID == "" && anyByID {
			sub, err = findSubscription(ctx, r, org, ref)
		} else {
			sub, err = findSubscriptionIn(ctx, r, org, ref, statuses...)
		}
		if err != nil {
			return err
		}
		ti.OrganizationID, ti.SubscriptionID = org, sub.ID
		if sub, err = s.life.Terminate(ctx, ti); err != nil {
			return err
		}
		out, err = view(ctx, r, sub, s.d.Now())
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("terminate subscription: %w", err)
	}
	return out, nil
}
