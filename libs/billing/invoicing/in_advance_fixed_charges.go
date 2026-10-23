package invoicing

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// StatusResolver moves an incomplete subscription after one of its
// activation rules changed (lago
// Subscriptions::ActivationRules::ResolveSubscriptionStatusService);
// libs/billing/lifecycle implements it.
type StatusResolver interface {
	// ResolveSubscriptionStatus activates the incomplete subscription of
	// env when every rule is fulfilled and cancels it when one is
	// rejected, in the transaction of r.
	ResolveSubscriptionStatus(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv) error
}

// ErrNoStatusResolver is returned when an invoice satisfies a payment
// activation rule and the service has no [StatusResolver].
var ErrNoStatusResolver = errors.New("invoicing: no subscription status resolver")

// WithStatusResolver sets the resolver of the incomplete subscriptions a
// zero-amount invoice ungates, and returns the service.
func (s *Service) WithStatusResolver(sr StatusResolver) *Service {
	s.statuses = sr
	return s
}

// InAdvanceFixedCharges runs a [kernel.JobCreateInAdvanceFixedCharges]
// (lago Invoices::CreatePayInAdvanceFixedChargesService): the unit changes
// of the subscription's pay-in-advance fixed charges applied immediately
// at a.Timestamp are billed, each with the units added over those already
// billed in the fixed charges period, on one subscription invoice issued
// at a.Timestamp, created even when no change gives a fee. Only an active
// or gated subscription with such a change is billed; nil is returned
// otherwise.
//
// The invoice of a gated subscription stays open until its payment
// resolves the payment rule, without invoice.created or document; one of
// zero amount satisfies the rule at once and the subscription is
// activated when every rule is fulfilled.
//
// source: app/services/invoices/create_pay_in_advance_fixed_charges_service.rb:16-86
func (s *Service) InAdvanceFixedCharges(ctx context.Context, org core.OrganizationID, a kernel.InAdvanceFixedChargesArgs) (*invoice.Invoice, error) {
	var out *invoice.Invoice
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := kernel.LockSubscription(ctx, r, org, a.SubscriptionID); err != nil {
			return err
		}
		env, err := kernel.LoadSubscriptionEnv(ctx, r, org, a.SubscriptionID)
		if err != nil {
			return err
		}
		sub := env.Subscription
		rules, err := r.ActivationRules.ListBySubscription(ctx, org, sub.ID)
		if err != nil {
			return fmt.Errorf("load activation rules of %s: %w", sub.ID, err)
		}
		if sub.Status != subscription.StatusActive && !subscription.Gated(sub, rules) {
			return nil
		}
		fcs, err := env.FixedCharges(ctx, r)
		if err != nil {
			return err
		}
		events, err := r.FixedChargeEvents.ListBySubscription(ctx, org, a.SubscriptionID, core.FixedChargeID{})
		if err != nil {
			return fmt.Errorf("load fixed charge events: %w", err)
		}
		type billedEvent struct {
			fc *catalog.FixedCharge
			ev *subscription.FixedChargeEvent
		}
		var billable []billedEvent
		for _, ev := range events {
			if !ev.ApplyUnitsImmediately || !ev.Timestamp.Equal(a.Timestamp) {
				continue
			}
			i := slices.IndexFunc(fcs, func(f *catalog.FixedCharge) bool { return f.ID == ev.FixedChargeID })
			if i < 0 || !fcs[i].PayInAdvance {
				continue
			}
			billable = append(billable, billedEvent{fc: fcs[i], ev: ev})
		}
		if len(billable) == 0 {
			return nil
		}
		var fs []*invoice.Fee
		for _, b := range billable {
			f, err := s.fees.InAdvanceFixedChargeFee(ctx, r, env, b.fc, b.ev)
			if err != nil {
				return err
			}
			if f != nil {
				fs = append(fs, f)
			}
		}
		// The invoice is created without fees too
		// (create_pay_in_advance_fixed_charges_service.rb:21-23).
		out, err = s.inAdvanceFixedChargesInvoice(ctx, r, env, rules, fs, a)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("bill in-advance fixed charges of subscription %s: %w", a.SubscriptionID, err)
	}
	return out, nil
}

// inAdvanceFixedChargesInvoice puts the fees fs on their own subscription
// invoice, issued at a.Timestamp, for env's subscription whose activation
// rules are rules.
//
// source: app/services/invoices/create_pay_in_advance_fixed_charges_service.rb:27-86,135-153
func (s *Service) inAdvanceFixedChargesInvoice(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, rules []*subscription.ActivationRule, fs []*invoice.Fee, a kernel.InAdvanceFixedChargesArgs) (*invoice.Invoice, error) {
	cenv := env.Customer
	sub := env.Subscription
	inv := kernel.NewInvoice(s.d, cenv, kindInAdvance, env.Plan.Currency, a.Timestamp)
	inv.PaymentMethodType, inv.PaymentMethodID = sub.PaymentMethodType, sub.PaymentMethodID
	inv.PurchaseOrderNumber = core.NormalizePurchaseOrderNumber(sub.PurchaseOrderNumber)
	if err := r.Invoices.Create(ctx, inv); err != nil {
		return nil, fmt.Errorf("create invoice: %w", err)
	}
	// The row takes the subscription's billing boundaries at the
	// timestamp (Invoices::CreateInvoiceSubscriptionService).
	b := &billed{item: BillItem{SubscriptionID: sub.ID, Reason: invoice.InvoicingReasonInAdvanceCharge}, env: env}
	if _, err := s.resolveBoundaries(ctx, r, b, a.Timestamp); err != nil {
		return nil, err
	}
	is := b.is
	if is == nil {
		is = &invoice.Subscription{
			ID: core.NewID[core.InvoiceSubscriptionID](s.d.IDs), OrganizationID: inv.OrganizationID,
			SubscriptionID: sub.ID, Reason: invoice.InvoicingReasonInAdvanceCharge,
			Timestamp: a.Timestamp.UTC(), CreatedAt: s.d.Now(),
		}
	}
	is.InvoiceID = inv.ID
	if err := r.InvoiceSubscriptions.Create(ctx, is); err != nil {
		return nil, fmt.Errorf("create invoice subscription: %w", err)
	}
	for _, f := range fs {
		f.InvoiceID = inv.ID
		if err := r.Fees.Create(ctx, f); err != nil {
			return nil, fmt.Errorf("create fee: %w", err)
		}
	}
	// The subscription's selection, applied before the taxes
	// (create_pay_in_advance_fixed_charges_service.rb:39-44).
	if err := s.applyCustomSections(ctx, r, cenv, inv, nil, kernel.SubscriptionSectionResource(sub)); err != nil {
		return nil, err
	}
	resolver := kernel.NewTaxResolver(r, cenv)
	resolver.SetPlan(sub.ID, env.Plan)
	st, err := s.settle(ctx, r, cenv, inv, fs, settleOptions{resolver: resolver, vies: true})
	if err != nil {
		return nil, err
	}
	if st.taxFailure == nil && !st.pending && inv.TotalAmount == 0 && subscription.PaymentGated(sub, rules) {
		if err := s.skipPaymentGate(ctx, r, env, rules); err != nil {
			return nil, err
		}
	}
	// TransitionToFinalStatusService keeps the invoice open while a
	// subscription it bills is gated.
	st.gated = subscription.Gated(sub, rules)
	if err := s.finish(ctx, r, cenv, inv, kindInAdvance, st, modeNew); err != nil {
		return nil, err
	}
	switch {
	case st.taxFailure != nil:
	case st.pending:
		// Taxes deferred to the provider: the fees alone are announced.
		if err := s.emitFees(ctx, fs); err != nil {
			return nil, err
		}
	case st.gated:
		// The payment of a gated invoice resolves its payment rule.
		if inv.TotalAmount > 0 && inv.PaymentStatus != core.PaymentStatusSucceeded && !inv.SelfBilled {
			args := kernel.InvoiceArgs{InvoiceID: inv.ID}
			if err := kernel.Enqueue(ctx, r, inv.OrganizationID, kernel.JobCreateInvoicePayment, args, "invoice:"+inv.ID.String(), s.d.Now()); err != nil {
				return nil, err
			}
		}
	case inv.Status != invoice.StatusClosed:
		if err := s.emitFees(ctx, fs); err != nil {
			return nil, err
		}
	}
	return inv, nil
}

// skipPaymentGate satisfies the pending payment rule among rules of env's
// payment-gated subscription, whose invoice has nothing to pay, and
// resolves the subscription's status.
//
// source: app/services/invoices/create_pay_in_advance_fixed_charges_service.rb:99-105
func (s *Service) skipPaymentGate(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, rules []*subscription.ActivationRule) error {
	if s.statuses == nil {
		return ErrNoStatusResolver
	}
	i := slices.IndexFunc(rules, func(x *subscription.ActivationRule) bool {
		return x.RuleType == subscription.ActivationRuleTypePayment && x.Status == subscription.ActivationRuleStatusPending
	})
	rule := rules[i]
	if err := rule.Satisfy(s.d.Now()); err != nil {
		return fmt.Errorf("satisfy activation rule %s: %w", rule.ID, err)
	}
	if err := r.ActivationRules.Update(ctx, rule); err != nil {
		return fmt.Errorf("update activation rule %s: %w", rule.ID, err)
	}
	return s.statuses.ResolveSubscriptionStatus(ctx, r, env)
}
