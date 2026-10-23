package subscriptions

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Rule codes of Update.
const (
	// CodeSubscriptionIncomplete: an incomplete subscription cannot be
	// updated.
	CodeSubscriptionIncomplete = "subscription_incomplete"
	// CodePurchaseOrderNumberNotEditable: the purchase order number of a
	// subscription that is neither pending nor active cannot change.
	CodePurchaseOrderNumberNotEditable = "purchase_order_number_not_editable"
)

// UpdateInput is the request of [Service.Update]: the fields set are the
// ones updated (lago Api::V1::SubscriptionsController#update).
type UpdateInput struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// Subscription is the updated subscription.
	Subscription SubscriptionRef
	// Name is the display name; a blank one clears it.
	Name core.Optional[string]
	// SubscriptionAt is the new start of a subscription starting in the
	// future.
	SubscriptionAt core.Optional[time.Time]
	// EndingAt is the planned termination; nil clears it.
	EndingAt core.Optional[*time.Time]
	// PurchaseOrderNumber is the purchase order number; nil clears it.
	PurchaseOrderNumber core.Optional[*string]
	// ProgressiveBillingDisabled turns the usage thresholds off.
	ProgressiveBillingDisabled core.Optional[bool]
	// ConsolidateInvoice is the invoice grouping setting.
	ConsolidateInvoice core.Optional[bool]
	// OnTerminationCreditNote is the termination credit note setting,
	// applied on plans paid in advance only.
	OnTerminationCreditNote core.Optional[subscription.TerminationCreditNote]
	// OnTerminationInvoice is the termination invoice setting.
	OnTerminationInvoice core.Optional[subscription.TerminationInvoice]
	// PaymentMethod, when not nil, changes the payment method.
	PaymentMethod *PaymentMethod
	// BillingEntity is the subscription's own billing entity; a zero ref
	// clears it.
	BillingEntity core.Optional[refs.Ref]
	// ActivationRules, when set, replace the activation rules of a pending
	// subscription.
	ActivationRules core.Optional[[]ActivationRule]
	// InvoiceCustomSections, when not nil, selects the custom sections.
	InvoiceCustomSections *CustomSections
	// PlanOverrides, when set, update the subscription's override plan,
	// or move a subscription of an original plan onto a new override
	// plan; fixed charge units only (and no other value) on an original
	// plan set the subscription's own units instead.
	PlanOverrides core.Optional[plans.OverrideInput]
	// UsageThresholds, when set, replace the subscription's own usage
	// thresholds; exclusive with PlanOverrides.UsageThresholds.
	UsageThresholds core.Optional[[]plans.ThresholdInput]
}

// Update changes a subscription's editable fields (lago
// Subscriptions::UpdateService). Moving the start of a subscription
// starting in the future to today or the past activates it.
//
// source: app/services/subscriptions/update_service.rb
func (s *Service) Update(ctx context.Context, in UpdateInput) (*View, error) {
	org := in.OrganizationID
	var out *View
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		sub, err := findSubscription(ctx, r, org, in.Subscription)
		if err != nil {
			return err
		}
		// The lifecycle's lock order: the customer's billing schedule, then
		// the subscription; reloaded under the locks.
		if err := kernel.LockCustomer(ctx, r, org, sub.CustomerID, customer.LockScopeBillingSchedule); err != nil {
			return err
		}
		if err := kernel.LockSubscription(ctx, r, org, sub.ID); err != nil {
			return err
		}
		id := sub.ID
		if sub, err = r.Subscriptions.Get(ctx, org, id); err != nil {
			return fmt.Errorf("load subscription %s: %w", id, err)
		}
		if sub.Status == subscription.StatusIncomplete {
			return core.NewRuleError(CodeSubscriptionIncomplete, "an incomplete subscription cannot be updated")
		}
		if in.PurchaseOrderNumber.Set && !equalString(core.NormalizePurchaseOrderNumber(in.PurchaseOrderNumber.Value), sub.PurchaseOrderNumber) &&
			sub.Status != subscription.StatusPending && sub.Status != subscription.StatusActive {
			return core.NewRuleError(CodePurchaseOrderNumberNotEditable, "the purchase order number of this subscription cannot change")
		}
		c, err := findCustomer(ctx, r, org, CustomerRef{ID: sub.CustomerID})
		if err != nil {
			return err
		}
		plan, err := r.Plans.Get(ctx, org, sub.PlanID)
		if err != nil {
			return fmt.Errorf("load plan %s: %w", sub.PlanID, err)
		}
		now := s.d.Now()
		at := sub.SubscriptionAt
		if in.SubscriptionAt.Set {
			at = in.SubscriptionAt.Value.UTC()
		}

		var v core.Validator
		if in.EndingAt.Set {
			checkEndingAt(&v, in.EndingAt.Value, at, now)
		}
		if err := checkPaymentMethod(ctx, r, &v, org, in.PaymentMethod); err != nil {
			return err
		}
		if in.ActivationRules.Set {
			if err := checkActivationRules(ctx, r, &v, c, sub, in.PaymentMethod, in.ActivationRules.Value, true); err != nil {
				return err
			}
		}
		if err := v.Err(); err != nil {
			return err
		}
		if in.UsageThresholds.Set && in.PlanOverrides.Set {
			if err := checkThresholdsCompatible(in.UsageThresholds.Value, &in.PlanOverrides.Value); err != nil {
				return err
			}
		}

		if in.Name.Set {
			sub.Name = nil
			if name := strings.TrimSpace(in.Name.Value); name != "" {
				sub.Name = &name
			}
		}
		if in.EndingAt.Set {
			sub.EndingAt = utcPtr(in.EndingAt.Value)
		}
		if in.PurchaseOrderNumber.Set {
			sub.PurchaseOrderNumber = core.NormalizePurchaseOrderNumber(in.PurchaseOrderNumber.Value)
		}
		in.ProgressiveBillingDisabled.Apply(&sub.ProgressiveBillingDisabled)
		in.ConsolidateInvoice.Apply(&sub.ConsolidateInvoice)
		if plan.PayInAdvance {
			in.OnTerminationCreditNote.Apply(&sub.OnTerminationCreditNote)
		}
		in.OnTerminationInvoice.Apply(&sub.OnTerminationInvoice)
		applyPaymentMethod(sub, in.PaymentMethod)
		if in.BillingEntity.Set {
			be, err := findBillingEntity(ctx, r, org, in.BillingEntity.Value)
			if err != nil {
				return err
			}
			sub.BillingEntityID = core.BillingEntityID{}
			if be != nil {
				sub.BillingEntityID = be.ID
			}
		}

		future := sub.StartingInTheFuture()
		today := core.DateOf(now, time.UTC)
		toPast := future && in.SubscriptionAt.Set && core.DateOf(at, time.UTC).Before(today)
		var rules []*subscription.ActivationRule
		if in.ActivationRules.Set && !toPast {
			rules = make([]*subscription.ActivationRule, len(in.ActivationRules.Value))
			for i, a := range in.ActivationRules.Value {
				rules[i] = subscription.NewActivationRule(org, core.NewID[core.ActivationRuleID](s.d.IDs), sub.ID, a.Type, a.TimeoutHours, now)
			}
			if err := r.ActivationRules.ReplaceForSubscription(ctx, org, sub.ID, rules); err != nil {
				return fmt.Errorf("replace activation rules of %s: %w", sub.ID, err)
			}
		} else if rules, err = r.ActivationRules.ListBySubscription(ctx, org, sub.ID); err != nil {
			return fmt.Errorf("list activation rules of %s: %w", sub.ID, err)
		}
		if in.InvoiceCustomSections != nil {
			if err := attachSections(ctx, r, sub, *in.InvoiceCustomSections); err != nil {
				return err
			}
		}
		units := false
		if in.PlanOverrides.Set {
			if units, err = s.applyPlanOverrides(ctx, r, sub, plan, in.PlanOverrides.Value); err != nil {
				return err
			}
		}
		if err := sub.Validate(); err != nil {
			return err
		}
		sub.UpdatedAt = now

		if future && in.SubscriptionAt.Set {
			// update_service.rb:262 process_subscription_at_change.
			sub.SubscriptionAt = at
			if sub, err = s.moveStart(ctx, r, sub, rules, now); err != nil {
				return err
			}
		} else {
			if err := r.Subscriptions.Update(ctx, sub); err != nil {
				return fmt.Errorf("update subscription %s: %w", sub.ID, err)
			}
			if err := s.emit(ctx, webhook.EventTypeSubscriptionUpdated, sub); err != nil {
				return err
			}
		}
		if units {
			if err := s.applyUnits(ctx, sub, in.PlanOverrides.Value); err != nil {
				return err
			}
		}
		// update_service.rb:98-100: after the plan overrides.
		if in.UsageThresholds.Set {
			if err := s.replaceThresholds(ctx, r, sub, in.UsageThresholds.Value); err != nil {
				return err
			}
		}
		out, err = view(ctx, r, sub, now)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("update subscription: %w", err)
	}
	return out, nil
}

// moveStart stores the new start of a subscription starting in the
// future: still in the future, or today with activation rules (left to
// the activation sweep), it stays pending; in the past it becomes active
// from that date without starting invoice; today it activates now.
//
// source: app/services/subscriptions/update_service.rb:262
func (s *Service) moveStart(ctx context.Context, r port.Repos, sub *subscription.Subscription, rules []*subscription.ActivationRule, now time.Time) (*subscription.Subscription, error) {
	day, today := core.DateOf(sub.SubscriptionAt, time.UTC), core.DateOf(now, time.UTC)
	if sub.SubscriptionAt.After(now) || (day == today && len(rules) > 0) {
		if err := r.Subscriptions.Update(ctx, sub); err != nil {
			return nil, fmt.Errorf("update subscription %s: %w", sub.ID, err)
		}
		return sub, nil
	}
	if day.Before(today) {
		if len(rules) > 0 {
			if err := r.ActivationRules.ReplaceForSubscription(ctx, sub.OrganizationID, sub.ID, nil); err != nil {
				return nil, fmt.Errorf("clear activation rules of %s: %w", sub.ID, err)
			}
		}
		if err := sub.Activate(sub.SubscriptionAt, now); err != nil {
			return nil, fmt.Errorf("activate subscription %s: %w", sub.ID, err)
		}
		if err := r.Subscriptions.Update(ctx, sub); err != nil {
			return nil, fmt.Errorf("update subscription %s: %w", sub.ID, err)
		}
		if err := s.emit(ctx, webhook.EventTypeSubscriptionStarted, sub); err != nil {
			return nil, err
		}
	} else {
		if err := r.Subscriptions.Update(ctx, sub); err != nil {
			return nil, fmt.Errorf("update subscription %s: %w", sub.ID, err)
		}
		var err error
		if sub, err = s.life.Activate(ctx, sub.OrganizationID, sub.ID, sub.SubscriptionAt); err != nil {
			return nil, err
		}
	}
	return sub, s.emit(ctx, webhook.EventTypeSubscriptionUpdated, sub)
}

// emit records the domain event t on sub.
func (s *Service) emit(ctx context.Context, t webhook.EventType, sub *subscription.Subscription) error {
	return kernel.Emit(ctx, s.d, sub.OrganizationID, t, Resource, sub.ID.UUID(), sub)
}

func equalString(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
