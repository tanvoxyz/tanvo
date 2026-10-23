package invoicing

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// ProgressiveBilling issues, in the transaction of r, the progressive
// billing invoice of sub for the usage thresholds it just reached (lago
// Invoices::ProgressiveBillingService): the fees of its invoiceable
// pay-in-arrears charges over the current period so far, less the
// progressive billing credits of the period, coupons, credit notes and
// prepaid credits, taxed and finalized at once (no grace period). Each
// threshold is recorded as an [subscription.AppliedUsageThreshold] at the
// lifetime usage total. A billing entity skipping zero-amount invoices
// closes a zero invoice. It returns the invoice, failed when the taxes
// fail.
func (s *Service) ProgressiveBilling(ctx context.Context, r port.Repos, sub *subscription.Subscription, thresholds []*catalog.UsageThreshold, lifetimeUsage *subscription.LifetimeUsage) (*invoice.Invoice, error) {
	if len(thresholds) == 0 {
		return nil, fmt.Errorf("progressive billing of subscription %s: %w: no threshold", sub.ID, core.ErrInvalidValue)
	}
	env, err := kernel.LoadSubscriptionEnv(ctx, r, sub.OrganizationID, sub.ID)
	if err != nil {
		return nil, err
	}
	now := s.d.Now()
	d, err := env.Dates(now, period.CurrentUsage)
	if err != nil {
		return nil, err
	}
	bounds, ok := d.Boundaries()
	if !ok {
		return nil, fmt.Errorf("progressive billing of subscription %s: %w: not started", sub.ID, core.ErrInvalidValue)
	}
	cenv := env.Customer
	inv := kernel.NewInvoice(s.d, cenv, kindProgressive, env.Plan.Currency, now)
	inv.PaymentMethodType, inv.PaymentMethodID = sub.PaymentMethodType, sub.PaymentMethodID
	if err := r.Invoices.Create(ctx, inv); err != nil {
		return nil, fmt.Errorf("create invoice: %w", err)
	}
	is := &invoice.Subscription{
		ID: core.NewID[core.InvoiceSubscriptionID](s.d.IDs), OrganizationID: inv.OrganizationID, InvoiceID: inv.ID,
		SubscriptionID: sub.ID, Reason: invoice.InvoicingReasonProgressiveBilling,
		Period: bounds.Subscription, ChargesPeriod: bounds.Charges, FixedChargesPeriod: bounds.FixedCharges,
		Timestamp: now.UTC(), CreatedAt: now,
	}
	if err := r.InvoiceSubscriptions.Create(ctx, is); err != nil {
		return nil, fmt.Errorf("create invoice subscription: %w", err)
	}
	charges, err := env.Charges(ctx, r)
	if err != nil {
		return nil, err
	}
	sc := fees.Scope{Env: env, Invoice: inv, InvoiceSubscription: is, Boundaries: bounds, Timestamp: now}
	var fs []*invoice.Fee
	for _, c := range charges {
		if !c.Invoiceable || c.PayInAdvance {
			continue
		}
		res, err := s.fees.ChargeFees(ctx, r, sc, c, fees.Options{})
		if err != nil {
			return nil, err
		}
		if res == nil {
			continue
		}
		for _, f := range res.Fees {
			if err := r.Fees.Create(ctx, f); err != nil {
				return nil, fmt.Errorf("create fee: %w", err)
			}
		}
		fs = append(fs, res.Fees...)
	}
	for _, t := range thresholds {
		a := &subscription.AppliedUsageThreshold{
			ID: core.NewID[core.AppliedUsageThresholdID](s.d.IDs), OrganizationID: inv.OrganizationID,
			UsageThresholdID: t.ID, SubscriptionID: sub.ID, InvoiceID: inv.ID,
			LifetimeUsageAmount: lifetimeUsage.Total(), CreatedAt: now, UpdatedAt: now,
		}
		if err := r.AppliedUsageThresholds.Create(ctx, a); err != nil {
			return nil, fmt.Errorf("apply usage threshold %s: %w", t.ID, err)
		}
	}
	resolver := kernel.NewTaxResolver(r, cenv)
	resolver.SetPlan(sub.ID, env.Plan)
	st, err := s.settle(ctx, r, cenv, inv, fs, settleOptions{resolver: resolver})
	if err != nil {
		return nil, err
	}
	if err := s.flagLifetimeUsage(ctx, r, inv.OrganizationID, []core.SubscriptionID{sub.ID}); err != nil {
		return nil, err
	}
	if err := s.finish(ctx, r, cenv, inv, kindProgressive, st, modeFinalize); err != nil {
		return nil, err
	}
	return inv, nil
}
