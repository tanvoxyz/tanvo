package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Activate activates the pending or incomplete subscription id at at
// (domain §9.2, §9.3): its activation rules are evaluated and an
// applicable one gates it (incomplete, its starting invoice billed and
// held); otherwise it becomes active, replacing its previous subscription
// on an upgrade or a downgrade, and its starting fees are billed. An
// active or gated subscription is left as is.
func (s *Service) Activate(ctx context.Context, org core.OrganizationID, id core.SubscriptionID, at time.Time) (*subscription.Subscription, error) {
	var out *subscription.Subscription
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		env, err := s.lockedEnv(ctx, r, org, id)
		if err != nil {
			return err
		}
		out = env.Subscription
		return s.activate(ctx, r, env, at)
	})
	if err != nil {
		return nil, fmt.Errorf("activate subscription %s: %w", id, err)
	}
	return out, nil
}

// lockedEnv locks the subscription id (and its previous one) under the
// customer's billing schedule lock and loads its environment.
func (s *Service) lockedEnv(ctx context.Context, r port.Repos, org core.OrganizationID, id core.SubscriptionID) (*kernel.SubscriptionEnv, error) {
	sub, err := r.Subscriptions.Get(ctx, org, id)
	if err != nil {
		return nil, fmt.Errorf("load subscription %s: %w", id, err)
	}
	ids := []core.SubscriptionID{sub.ID}
	if !sub.PreviousSubscriptionID.IsZero() {
		ids = append(ids, sub.PreviousSubscriptionID)
	}
	if err := lock(ctx, r, org, sub.CustomerID, ids...); err != nil {
		return nil, err
	}
	return kernel.LoadSubscriptionEnv(ctx, r, org, id)
}

// activation is what an activation needs to know about the subscription.
type activation struct {
	env            *kernel.SubscriptionEnv
	rules          []*subscription.ActivationRule
	previous       *subscription.Subscription
	previousPlan   *catalog.Plan
	change         period.Change
	fromIncomplete bool
	inAdvanceFixed bool
}

// paymentRule reports whether the subscription has a payment rule: its
// starting invoice was then billed while it was gated.
func (a *activation) paymentRule() bool {
	return slices.ContainsFunc(a.rules, func(r *subscription.ActivationRule) bool {
		return r.RuleType == subscription.ActivationRuleTypePayment
	})
}

// billedWhileGated reports whether the starting invoice was billed while
// the subscription waited for its payment rule.
func (a *activation) billedWhileGated() bool { return a.fromIncomplete && a.paymentRule() }

// facts returns the facts deciding whether a payment rule applies at at.
func (a *activation) facts(at time.Time) subscription.ActivationFacts {
	ps := a.env.Period()
	if !ps.Started() {
		ps.StartedAt = at
	}
	return subscription.ActivationFacts{
		PlanPayInAdvance:         a.env.Plan.PayInAdvance,
		InTrial:                  ps.InTrial(at),
		HasInAdvanceFixedCharges: a.inAdvanceFixed,
	}
}

// billsStart reports whether the activation at at bills starting fees: an
// in-advance base fee outside the trial or in-advance fixed charges.
func (a *activation) billsStart(at time.Time) bool {
	return subscription.PaymentRuleApplicable(a.facts(at))
}

// loadActivation loads the rules, the previous subscription and the fixed
// charges of env.
func (s *Service) loadActivation(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv) (*activation, error) {
	sub := env.Subscription
	rules, err := r.ActivationRules.ListBySubscription(ctx, sub.OrganizationID, sub.ID)
	if err != nil {
		return nil, fmt.Errorf("load activation rules of %s: %w", sub.ID, err)
	}
	fixed, err := env.FixedCharges(ctx, r)
	if err != nil {
		return nil, err
	}
	a := &activation{
		env: env, rules: rules, fromIncomplete: sub.Status == subscription.StatusIncomplete,
		inAdvanceFixed: slices.ContainsFunc(fixed, func(f *catalog.FixedCharge) bool { return f.PayInAdvance }),
		change:         period.NoChange,
	}
	if env.Previous != nil {
		a.previous = env.Previous
		if a.previousPlan, err = r.Plans.Get(ctx, sub.OrganizationID, env.Previous.PlanID); err != nil {
			return nil, fmt.Errorf("load plan of subscription %s: %w", env.Previous.ID, err)
		}
		a.change = kernel.PlanChange(a.previousPlan, env.Plan)
	}
	return a, nil
}

// activate activates env's subscription at at, in the transaction of r.
//
// source: app/services/subscriptions/activate_service.rb
func (s *Service) activate(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, at time.Time) error {
	sub := env.Subscription
	if sub.Status != subscription.StatusPending && sub.Status != subscription.StatusIncomplete {
		return nil
	}
	a, err := s.loadActivation(ctx, r, env)
	if err != nil {
		return err
	}
	if subscription.Gated(sub, a.rules) {
		return nil
	}
	if sub.Status == subscription.StatusPending {
		if err := s.evaluateRules(ctx, r, a, at); err != nil {
			return err
		}
		if subscription.HasPendingRules(a.rules) {
			return s.gate(ctx, r, a, at)
		}
	}
	if a.fromIncomplete && slices.ContainsFunc(a.rules, (*subscription.ActivationRule).Rejected) {
		return nil
	}
	switch a.change {
	case period.Upgrade:
		return s.activateUpgrade(ctx, r, a, at)
	case period.Downgrade:
		return s.activateDowngrade(ctx, r, a, at)
	default:
		return s.activateStandalone(ctx, r, a, at)
	}
}

// evaluateRules makes the inactive rules pending when they apply and not
// applicable otherwise (domain §9.3).
//
// source: app/services/subscriptions/activation_rules/evaluate_service.rb
func (s *Service) evaluateRules(ctx context.Context, r port.Repos, a *activation, at time.Time) error {
	applicable := subscription.PaymentRuleApplicable(a.facts(at))
	for _, rule := range a.rules {
		if rule.Status != subscription.ActivationRuleStatusInactive {
			continue
		}
		var err error
		if rule.RuleType == subscription.ActivationRuleTypePayment && applicable {
			err = rule.Start(at)
		} else {
			err = rule.MarkNotApplicable(at)
		}
		if err != nil {
			return fmt.Errorf("evaluate activation rule %s: %w", rule.ID, err)
		}
		if err := r.ActivationRules.Update(ctx, rule); err != nil {
			return fmt.Errorf("update activation rule %s: %w", rule.ID, err)
		}
	}
	return nil
}

// gate makes the subscription incomplete at at; a payment rule bills its
// starting invoice, which invoicing holds until the rule resolves.
func (s *Service) gate(ctx context.Context, r port.Repos, a *activation, at time.Time) error {
	sub := a.env.Subscription
	if err := sub.MarkIncomplete(at); err != nil {
		return fmt.Errorf("gate subscription %s: %w", sub.ID, err)
	}
	if err := update(ctx, r, sub); err != nil {
		return err
	}
	if err := s.emitFixedChargeEvents(ctx, r, a.env); err != nil {
		return err
	}
	if subscription.PaymentGated(sub, a.rules) {
		if err := s.bill(ctx, r, sub.OrganizationID, at, true, item(sub, invoice.InvoicingReasonSubscriptionStarting)); err != nil {
			return err
		}
	}
	return s.notify(ctx, r, webhook.EventTypeSubscriptionIncomplete, sub)
}

// activateStandalone activates a subscription that replaces none and bills
// its starting fees: skipping charges on a direct activation, and only
// when no payment rule billed them already from incomplete.
func (s *Service) activateStandalone(ctx context.Context, r port.Repos, a *activation, at time.Time) error {
	sub := a.env.Subscription
	if err := sub.Activate(at, s.d.Now()); err != nil {
		return fmt.Errorf("activate subscription %s: %w", sub.ID, err)
	}
	if err := update(ctx, r, sub); err != nil {
		return err
	}
	if err := s.emitActivationEvents(ctx, r, a); err != nil {
		return err
	}
	if !a.billedWhileGated() && a.billsStart(at) {
		if err := s.bill(ctx, r, sub.OrganizationID, at, !a.fromIncomplete, item(sub, invoice.InvoicingReasonSubscriptionStarting)); err != nil {
			return err
		}
	}
	return s.notify(ctx, r, webhook.EventTypeSubscriptionStarted, sub)
}

// activateUpgrade terminates the previous subscription without its own
// invoice, activates the new one and bills both on one invoice: the old
// subscription's ended period and the new one's starting fees (domain
// §22.13, §24 item 17).
func (s *Service) activateUpgrade(ctx context.Context, r port.Repos, a *activation, at time.Time) error {
	sub := a.env.Subscription
	prevEnv, err := kernel.LoadSubscriptionEnvOf(ctx, r, a.previous)
	if err != nil {
		return err
	}
	if _, err := s.terminate(ctx, r, prevEnv, terminateOptions{at: at, upgrade: true}); err != nil {
		return err
	}
	if err := sub.Activate(at, s.d.Now()); err != nil {
		return fmt.Errorf("activate subscription %s: %w", sub.ID, err)
	}
	if err := update(ctx, r, sub); err != nil {
		return err
	}
	if err := s.emitActivationEvents(ctx, r, a); err != nil {
		return err
	}
	subs := []*subscription.Subscription{a.previous}
	items := []invoicing.BillItem{item(a.previous, invoice.InvoicingReasonSubscriptionTerminating)}
	if !a.billedWhileGated() && a.billsStart(at) {
		subs = append(subs, sub)
		items = append(items, item(sub, invoice.InvoicingReasonSubscriptionStarting))
	}
	if err := s.billRotation(ctx, r, sub.OrganizationID, at.Add(time.Second), subs, items); err != nil {
		return err
	}
	return s.notify(ctx, r, webhook.EventTypeSubscriptionStarted, sub)
}

// activateDowngrade terminates the previous subscription at the end of its
// period, activates the next one and bills both: the previous one in
// arrears and the next one's starting fees (domain §22.13).
func (s *Service) activateDowngrade(ctx context.Context, r port.Repos, a *activation, at time.Time) error {
	sub, prev := a.env.Subscription, a.previous
	if prev.Status == subscription.StatusActive {
		if err := prev.Terminate(at); err != nil {
			return fmt.Errorf("terminate subscription %s: %w", prev.ID, err)
		}
		if err := update(ctx, r, prev); err != nil {
			return err
		}
	}
	if err := sub.Activate(at, s.d.Now()); err != nil {
		return fmt.Errorf("activate subscription %s: %w", sub.ID, err)
	}
	if err := update(ctx, r, sub); err != nil {
		return err
	}
	if err := s.emitActivationEvents(ctx, r, a); err != nil {
		return err
	}
	subs := []*subscription.Subscription{prev}
	items := []invoicing.BillItem{item(prev, invoice.InvoicingReasonSubscriptionTerminating)}
	if !a.billedWhileGated() && (a.env.Plan.PayInAdvance || a.inAdvanceFixed) {
		subs = append(subs, sub)
		items = append(items, item(sub, invoice.InvoicingReasonSubscriptionStarting))
	}
	if err := s.billRotation(ctx, r, sub.OrganizationID, at, subs, items); err != nil {
		return err
	}
	if err := s.billAdvanceCharges(ctx, r, prev, at); err != nil {
		return err
	}
	if err := s.notify(ctx, r, webhook.EventTypeSubscriptionTerminated, prev); err != nil {
		return err
	}
	return s.notify(ctx, r, webhook.EventTypeSubscriptionStarted, sub)
}

// SwapDowngrade terminates, in the transaction of r, the active
// subscription id at the billing instant at and activates its pending
// downgrade, billing both (lago's terminate_and_start_next). It reports
// false, doing nothing, when the subscription has no pending downgrade;
// the periodic run then bills it normally.
//
// source: app/services/subscriptions/terminate_service.rb (terminate_and_start_next)
func (s *Service) SwapDowngrade(ctx context.Context, r port.Repos, org core.OrganizationID, id core.SubscriptionID, at time.Time) (bool, error) {
	next, err := r.Subscriptions.GetNext(ctx, org, id)
	switch {
	case errors.Is(err, core.ErrNotFound):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("load next subscription of %s: %w", id, err)
	}
	// lockedEnv locks next and its previous subscription, cur; both are
	// read again under the locks, so that a concurrent swap, termination
	// or plan change of cur is seen.
	env, err := s.lockedEnv(ctx, r, org, next.ID)
	if err != nil {
		return false, err
	}
	cur, err := r.Subscriptions.Get(ctx, org, id)
	if err != nil {
		return false, fmt.Errorf("load subscription %s: %w", id, err)
	}
	if env.Subscription.Status != subscription.StatusPending || cur.Status != subscription.StatusActive || env.Subscription.PreviousSubscriptionID != cur.ID {
		return false, nil
	}
	if err := s.activate(ctx, r, env, at); err != nil {
		return false, fmt.Errorf("swap downgrade of subscription %s: %w", id, err)
	}
	return true, nil
}
