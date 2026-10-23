package payments

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// resolvePaymentGate enqueues the resolution of the payment activation
// rule of the incomplete subscription gated by inv once its payment
// status became succeeded or failed (lago
// Invoices::UpdateService#handle_payment_gated_activation,
// app/services/invoices/update_service.rb:119-129): inv must be open and
// bill a gated subscription (Invoice#subscription_gated?), and the first
// incomplete subscription it bills is resolved.
func resolvePaymentGate(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
	if inv.Status != invoice.StatusOpen {
		return nil
	}
	if inv.PaymentStatus != core.PaymentStatusSucceeded && inv.PaymentStatus != core.PaymentStatusFailed {
		return nil
	}
	org := inv.OrganizationID
	links, err := r.InvoiceSubscriptions.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return wrap(err, "list subscriptions of invoice %s", inv.ID)
	}
	gated := false
	var incomplete core.SubscriptionID
	for _, l := range links {
		sub, err := r.Subscriptions.Get(ctx, org, l.SubscriptionID)
		if err != nil {
			return wrap(err, "load subscription %s", l.SubscriptionID)
		}
		if sub.Status != subscription.StatusIncomplete {
			continue
		}
		if incomplete.IsZero() {
			incomplete = sub.ID
		}
		rules, err := r.ActivationRules.ListBySubscription(ctx, org, sub.ID)
		if err != nil {
			return wrap(err, "load activation rules of %s", sub.ID)
		}
		gated = gated || subscription.Gated(sub, rules)
	}
	if !gated || incomplete.IsZero() {
		return nil
	}
	args := kernel.PaymentGateArgs{SubscriptionID: incomplete, InvoiceID: inv.ID, PaymentStatus: inv.PaymentStatus}
	return kernel.Enqueue(ctx, r, org, kernel.JobResolvePaymentGate, args, "", time.Time{})
}
