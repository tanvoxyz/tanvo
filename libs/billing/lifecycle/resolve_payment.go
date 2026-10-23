package lifecycle

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// ResolvePaymentGate resolves the payment rule of the incomplete
// subscription id gated by the open subscription invoice inv, whose
// payment status became status (domain §9.3; lago
// Subscriptions::ActivationRules::Payment::ResolveService, run by
// Invoices::UpdateService#handle_payment_gated_activation). A succeeded
// payment satisfies the rule, finalizes the invoice (announced by
// invoice.created, its document generated) and activates the
// subscription once every rule is fulfilled; a failed payment fails the
// rule, closes the invoice, cancels the subscription with reason
// payment_failed and gives back the credits the invoice consumed. A
// subscription no longer incomplete, an invoice no longer open or not a
// subscription invoice, and any other status are left as they are.
//
// source: app/services/subscriptions/activation_rules/payment/resolve_service.rb
func (s *Service) ResolvePaymentGate(ctx context.Context, org core.OrganizationID, id core.SubscriptionID, inv core.InvoiceID, status core.PaymentStatus) error {
	if status != core.PaymentStatusSucceeded && status != core.PaymentStatusFailed {
		return nil
	}
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		env, err := s.lockedEnv(ctx, r, org, id)
		if err != nil {
			return err
		}
		sub := env.Subscription
		if sub.Status != subscription.StatusIncomplete {
			return nil
		}
		if err := kernel.LockInvoice(ctx, r, org, inv); err != nil {
			return err
		}
		gating, err := r.Invoices.Get(ctx, org, inv)
		if err != nil {
			return fmt.Errorf("load invoice %s: %w", inv, err)
		}
		if gating.Status != invoice.StatusOpen || gating.Type != invoice.TypeSubscription {
			return nil
		}
		now := s.d.Now()
		if status == core.PaymentStatusFailed {
			rules, err := r.ActivationRules.ListBySubscription(ctx, org, sub.ID)
			if err != nil {
				return fmt.Errorf("load activation rules of %s: %w", sub.ID, err)
			}
			rule, err := paymentRule(sub, rules)
			if err != nil {
				return err
			}
			// Payment::EvaluateService transitions only a pending rule
			// (payment/evaluate_service.rb:15-21).
			if rule.Status == subscription.ActivationRuleStatusPending {
				if err := rule.Fail(now); err != nil {
					return fmt.Errorf("fail activation rule %s: %w", rule.ID, err)
				}
				if err := r.ActivationRules.Update(ctx, rule); err != nil {
					return fmt.Errorf("update activation rule %s: %w", rule.ID, err)
				}
			}
			return s.cancelIncomplete(ctx, r, sub, subscription.ActivationRuleStatusFailed, subscription.CancellationReasonPaymentFailed, now)
		}
		return s.satisfyPaymentGate(ctx, r, env, gating)
	})
	if err != nil {
		return fmt.Errorf("resolve payment gate of subscription %s: %w", id, err)
	}
	return nil
}

// satisfyPaymentGate is the success path of [Service.ResolvePaymentGate]:
// the payment rule is satisfied, the gating invoice finalized
// (Invoices::FinalizeService) and announced, then the subscription
// activated when every rule is fulfilled, canceled when one is rejected
// (ActivationRules::ResolveSubscriptionStatusService).
//
// source: app/services/subscriptions/activation_rules/payment/resolve_service.rb:33-55
func (s *Service) satisfyPaymentGate(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, inv *invoice.Invoice) error {
	sub := env.Subscription
	org := sub.OrganizationID
	now := s.d.Now()
	rules, err := r.ActivationRules.ListBySubscription(ctx, org, sub.ID)
	if err != nil {
		return fmt.Errorf("load activation rules of %s: %w", sub.ID, err)
	}
	rule, err := paymentRule(sub, rules)
	if err != nil {
		return err
	}
	if rule.Status == subscription.ActivationRuleStatusPending {
		if err := rule.Satisfy(now); err != nil {
			return fmt.Errorf("satisfy activation rule %s: %w", rule.ID, err)
		}
		if err := r.ActivationRules.Update(ctx, rule); err != nil {
			return fmt.Errorf("update activation rule %s: %w", rule.ID, err)
		}
	}
	cenv, err := kernel.LoadCustomerEnv(ctx, r, org, inv.CustomerID, inv.BillingEntityID)
	if err != nil {
		return err
	}
	if err := kernel.Finalize(ctx, r, cenv, inv, kernel.InvoiceKind{Type: inv.Type}, now); err != nil {
		return err
	}
	inv.UpdatedAt = now
	if err := r.Invoices.Update(ctx, inv); err != nil {
		return fmt.Errorf("update invoice %s: %w", inv.ID, err)
	}
	if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeInvoiceCreated, "invoice", kernel.ObjectID(inv.ID), inv); err != nil {
		return err
	}
	if err := kernel.Enqueue(ctx, r, org, kernel.JobGenerateInvoiceDocument, kernel.InvoiceArgs{InvoiceID: inv.ID}, inv.ID.String(), time.Time{}); err != nil {
		return err
	}
	switch {
	case !slices.ContainsFunc(rules, func(x *subscription.ActivationRule) bool { return !x.Fulfilled() }):
		return s.activate(ctx, r, env, now)
	case slices.ContainsFunc(rules, (*subscription.ActivationRule).Rejected):
		if err := sub.Cancel(now, ""); err != nil {
			return fmt.Errorf("cancel subscription %s: %w", sub.ID, err)
		}
		if err := update(ctx, r, sub); err != nil {
			return err
		}
		return s.emit(ctx, webhook.EventTypeSubscriptionCanceled, sub)
	}
	return nil
}
