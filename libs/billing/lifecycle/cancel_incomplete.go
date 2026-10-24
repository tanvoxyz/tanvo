package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Field error codes of the cancellation of an incomplete subscription, on
// field "subscription".
const (
	// CodeSubscriptionAlreadyResolved: the subscription is no longer
	// incomplete.
	CodeSubscriptionAlreadyResolved = "subscription_already_resolved"
	// CodeActivationInvoiceNotReady: the subscription has no open or failed
	// gating invoice yet.
	CodeActivationInvoiceNotReady = "activation_invoice_not_ready"
)

// ExpireIncomplete cancels the incomplete subscription id whose activation
// rule timed out (domain §9.3): its payment rule expires, its gating
// invoice is closed and given back what it consumed, and the subscription
// is canceled with reason timeout. A subscription no longer incomplete
// (resolved concurrently) is returned unchanged.
//
// source: app/services/subscriptions/activation_rules/expire_service.rb
func (s *Service) ExpireIncomplete(ctx context.Context, org core.OrganizationID, id core.SubscriptionID) (*subscription.Subscription, error) {
	var out *subscription.Subscription
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		env, err := s.lockedEnv(ctx, r, org, id)
		if err != nil {
			return err
		}
		out = env.Subscription
		err = s.cancelIncomplete(ctx, r, env.Subscription, subscription.ActivationRuleStatusExpired,
			subscription.CancellationReasonTimeout, s.d.Now())
		if ve, ok := errors.AsType[*core.ValidationError](err); ok && ve.Has("subscription", CodeSubscriptionAlreadyResolved) {
			return nil
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("expire incomplete subscription %s: %w", id, err)
	}
	return out, nil
}

// cancelIncomplete cancels the incomplete subscription sub, locked by the
// caller, at at: its payment rule moves to ruleStatus (expired or
// declined), its gating invoice (open, or failed on a tax provider
// failure) is closed, the subscription is canceled with reason and
// announced as subscription.canceled, and the coupon credits, credit-note
// credits and outbound wallet transactions of the invoice are given back.
// A subscription no longer incomplete fails with
// [CodeSubscriptionAlreadyResolved], one without a gating invoice with
// [CodeActivationInvoiceNotReady]. A pending or processing payment of the
// invoice is cancelled at its provider ([kernel.JobCancelPayment]).
//
// source: app/services/subscriptions/activation_rules/cancel_service.rb
func (s *Service) cancelIncomplete(ctx context.Context, r port.Repos, sub *subscription.Subscription,
	ruleStatus subscription.ActivationRuleStatus, reason subscription.CancellationReason, at time.Time,
) error {
	if sub.Status != subscription.StatusIncomplete {
		return subscriptionFieldError(CodeSubscriptionAlreadyResolved)
	}
	org := sub.OrganizationID
	inv, err := gatingInvoice(ctx, r, sub)
	if err != nil {
		return err
	}
	if inv == nil {
		return subscriptionFieldError(CodeActivationInvoiceNotReady)
	}
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
		transition := rule.Decline
		if ruleStatus == subscription.ActivationRuleStatusExpired {
			transition = rule.Expire
		}
		if err := transition(at); err != nil {
			return fmt.Errorf("resolve activation rule %s: %w", rule.ID, err)
		}
		if err := r.ActivationRules.Update(ctx, rule); err != nil {
			return fmt.Errorf("update activation rule %s: %w", rule.ID, err)
		}
	}
	if err := kernel.LockInvoice(ctx, r, org, inv.ID); err != nil {
		return err
	}
	if err := inv.Transition(invoice.StatusClosed, at); err != nil {
		return fmt.Errorf("close invoice %s: %w", inv.ID, err)
	}
	if err := r.Invoices.Update(ctx, inv); err != nil {
		return fmt.Errorf("update invoice %s: %w", inv.ID, err)
	}
	if err := cancelPendingPayment(ctx, r, inv); err != nil {
		return err
	}
	// ResolveSubscriptionStatusService cancels the subscription once a rule
	// is rejected (resolve_subscription_status_service.rb:19-22); the only
	// rule just was.
	if !rule.Rejected() {
		return fmt.Errorf("cancel incomplete subscription %s: payment rule %s is %s", sub.ID, rule.ID, rule.Status)
	}
	if err := sub.Cancel(at, reason); err != nil {
		return fmt.Errorf("cancel subscription %s: %w", sub.ID, err)
	}
	if err := update(ctx, r, sub); err != nil {
		return err
	}
	if err := s.notify(ctx, r, webhook.EventTypeSubscriptionCanceled, sub); err != nil {
		return err
	}
	// The recredit jobs run after commit in lago (cancel_service.rb:67-79);
	// Tanvo gives the credits back in the same transaction, taking the
	// customer balance locks up front (kernel lock order).
	if err := kernel.LockCustomerBalances(ctx, r, org, inv.CustomerID); err != nil {
		return err
	}
	if _, err := deduct.New(s.d).Restore(ctx, r, inv, deduct.RestoreOptions{}); err != nil {
		return fmt.Errorf("restore credits of invoice %s: %w", inv.ID, err)
	}
	return nil
}

// cancelPendingPayment enqueues the cancellation at its provider of the
// pending or processing payment of the closed gating invoice inv, at most
// one (cancel_service.rb:81-88).
func cancelPendingPayment(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
	ps, err := r.Payments.ListByPayable(ctx, inv.OrganizationID, payment.InvoicePayable(inv.ID))
	if err != nil {
		return fmt.Errorf("list payments of invoice %s: %w", inv.ID, err)
	}
	for _, p := range ps {
		if p.Status == payment.StatePending || p.Status == payment.StateProcessing {
			return kernel.EnqueueCancelPayment(ctx, r, inv.OrganizationID, p.ID)
		}
	}
	return nil
}

// gatingInvoice returns the first subscription invoice of sub that is open
// or failed, or nil (cancel_service.rb:60-65).
func gatingInvoice(ctx context.Context, r port.Repos, sub *subscription.Subscription) (*invoice.Invoice, error) {
	org := sub.OrganizationID
	links, err := r.InvoiceSubscriptions.ListBySubscription(ctx, org, sub.ID)
	if err != nil {
		return nil, fmt.Errorf("list invoices of subscription %s: %w", sub.ID, err)
	}
	ids := make([]core.InvoiceID, len(links))
	for i, l := range links {
		ids[i] = l.InvoiceID
	}
	invs, err := r.Invoices.ListByIDs(ctx, org, ids)
	if err != nil {
		return nil, fmt.Errorf("load invoices of subscription %s: %w", sub.ID, err)
	}
	for _, inv := range invs {
		if inv.Type == invoice.TypeSubscription && (inv.Status == invoice.StatusOpen || inv.Status == invoice.StatusFailed) {
			return inv, nil
		}
	}
	return nil, nil
}

// paymentRule returns the sole payment rule of sub, as
// activation_rules.payment.sole does (cancel_service.rb:45).
func paymentRule(sub *subscription.Subscription, rules []*subscription.ActivationRule) (*subscription.ActivationRule, error) {
	var out *subscription.ActivationRule
	for _, rule := range rules {
		if rule.RuleType != subscription.ActivationRuleTypePayment {
			continue
		}
		if out != nil {
			return nil, fmt.Errorf("subscription %s has several payment rules", sub.ID)
		}
		out = rule
	}
	if out == nil {
		return nil, fmt.Errorf("subscription %s has no payment rule", sub.ID)
	}
	return out, nil
}

// subscriptionFieldError is a validation failure of code on field
// "subscription".
func subscriptionFieldError(code string) error {
	return &core.ValidationError{Errors: []core.FieldError{{Field: "subscription", Code: code}}}
}
