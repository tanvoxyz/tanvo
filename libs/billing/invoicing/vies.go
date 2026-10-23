package invoicing

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// JobFinalizePendingVIES finalizes an open invoice whose taxes waited for
// its customer's VIES check (lago Invoices::FinalizePendingViesInvoiceJob);
// its args are [kernel.InvoiceArgs]. The customers VIES check job
// enqueues it once the check completes.
const JobFinalizePendingVIES = "billing.finalize_pending_vies_invoice"

// errVIESPending holds an invoice while its customer's VIES check is in
// progress (lago Invoices::EnsureCompletedViesCheckService fails with
// vies_check_pending); it is a [kernel.ErrTaxesPending].
var errVIESPending = fmt.Errorf("vies check pending: %w", kernel.ErrTaxesPending)

// viesCheckInProgress reports whether the taxes of the invoices of env's
// customer wait for its VIES check: the customer is taxed by no provider,
// its billing entity manages EU taxes and it has a pending check (lago
// Customer#vies_check_in_progress?, app/models/customer.rb:285-289).
//
// source: app/services/invoices/ensure_completed_vies_check_service.rb:18-22
func (s *Service) viesCheckInProgress(ctx context.Context, r port.Repos, env *kernel.CustomerEnv) (bool, error) {
	c := env.Customer
	if s.d.Taxes != nil {
		on, err := s.d.Taxes.Enabled(ctx, c)
		if err != nil {
			return false, fmt.Errorf("%w: %w", kernel.ErrTaxProvider, err)
		}
		if on {
			return false, nil
		}
	}
	be := env.BillingEntity
	if be.ID != c.BillingEntityID {
		var err error
		if be, err = r.BillingEntities.Get(ctx, c.OrganizationID, c.BillingEntityID); err != nil {
			return false, fmt.Errorf("load billing entity %s: %w", c.BillingEntityID, err)
		}
	}
	if !be.EUTaxManagement {
		return false, nil
	}
	_, err := r.PendingVIESChecks.Get(ctx, c.OrganizationID, c.ID)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, core.ErrNotFound):
		return false, nil
	default:
		return false, fmt.Errorf("load pending vies check of customer %s: %w", c.ID, err)
	}
}

// holdForVIES returns [errVIESPending] when o checks the VIES check and
// the customer's is in progress: the invoice then waits with its taxes
// pending. The explicit taxes of one-off fees are applied first, as lago
// applies them when it creates the fees (app/services/fees/one_off_service.rb:55-62).
//
// source: app/services/invoices/compute_taxes_and_totals_service.rb:24-25
func (s *Service) holdForVIES(ctx context.Context, r port.Repos, env *kernel.CustomerEnv, resolver *kernel.TaxResolver, inv *invoice.Invoice, fees []*invoice.Fee, o settleOptions) error {
	if !o.vies || o.preview {
		return nil
	}
	held, err := s.viesCheckInProgress(ctx, r, env)
	if err != nil || !held {
		return err
	}
	var explicit []*invoice.Fee
	for _, f := range fees {
		if taxes, ok := resolver.Explicit(f.ID); ok && len(taxes) > 0 && len(f.AppliedTaxes) == 0 {
			explicit = append(explicit, f)
		}
	}
	if len(explicit) > 0 {
		if _, err := resolver.Compute(ctx, s.d, kernel.TaxInput{Invoice: inv, Fees: explicit}); err != nil {
			return err
		}
	}
	return errVIESPending
}

// EnqueueFinalizePendingVIES queues, in r's transaction, the finalization
// of inv once its customer's VIES check completed (lago
// Customers::ViesCheckJob#enqueue_pending_invoice_finalization). It
// implements customers.VIESInvoiceFinalizer.
//
// source: app/jobs/customers/vies_check_job.rb:44-55
func (s *Service) EnqueueFinalizePendingVIES(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
	return kernel.Enqueue(ctx, r, inv.OrganizationID, JobFinalizePendingVIES, kernel.InvoiceArgs{InvoiceID: inv.ID}, "invoice:"+inv.ID.String(), s.d.Now())
}

// FinalizePendingVIES finalizes the invoice id held for its customer's
// VIES check (lago Invoices::FinalizePendingViesInvoiceService): the
// invoice is taxed with the customer's taxes, now decided, applied its
// customer's custom sections, deducted and finalized, then announced
// (invoice.created or invoice.one_off_created, its document and its
// payment). An invoice gated by a subscription's payment stays open and
// only gets its payment. An invoice that is not open with pending taxes,
// of a customer taxed by a provider, or whose customer's check is in
// progress again, is left as it is.
func (s *Service) FinalizePendingVIES(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*invoice.Invoice, error) {
	return s.onInvoice(ctx, org, id, s.finalizePendingVIES)
}

// finalizePendingVIES is [Service.FinalizePendingVIES] in the transaction
// of r.
//
// source: app/services/invoices/finalize_pending_vies_invoice_service.rb:13-70
func (s *Service) finalizePendingVIES(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
	// Tanvo has no pending status: an invoice held for its taxes is open,
	// gated or not (finalize_pending_vies_invoice_service.rb:15-16).
	if inv.Status != invoice.StatusOpen || inv.TaxStatus != invoice.TaxStatusPending {
		return nil
	}
	org := inv.OrganizationID
	cenv, err := kernel.LoadCustomerEnv(ctx, r, org, inv.CustomerID, inv.BillingEntityID)
	if err != nil {
		return err
	}
	if s.d.Taxes != nil {
		on, err := s.d.Taxes.Enabled(ctx, cenv.Customer)
		if err != nil {
			return fmt.Errorf("finalize pending vies invoice %s: %w: %w", inv.ID, kernel.ErrTaxProvider, err)
		}
		if on {
			return nil
		}
	}
	if held, err := s.viesCheckInProgress(ctx, r, cenv); err != nil || held {
		return err
	}
	iss, err := r.InvoiceSubscriptions.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return fmt.Errorf("load subscriptions of invoice %s: %w", inv.ID, err)
	}
	fs, err := r.Fees.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return fmt.Errorf("load fees of invoice %s: %w", inv.ID, err)
	}
	// The progressive billing credits and coupons applied when the invoice
	// was held are kept; only the taxes and totals are computed
	// (finalize_pending_vies_invoice_service.rb:24, lago
	// Invoices::ComputeAmountsFromFees). Fees taxed when created (one-off
	// explicit taxes) keep their taxes, as lago Fees::ApplyTaxesService
	// keeps them.
	for _, f := range fs {
		if len(f.AppliedTaxes) == 0 {
			resetTaxesOnly(f)
		}
	}
	resolver, err := s.planResolver(ctx, r, cenv, iss)
	if err != nil {
		return err
	}
	// finalize_pending_vies_invoice_service.rb:25.
	if err := s.applyCustomSections(ctx, r, cenv, inv, nil); err != nil {
		return err
	}
	st, err := s.settle(ctx, r, cenv, inv, fs, settleOptions{aggregate: inv.Type == invoice.TypeAdvanceCharges, resolver: resolver, keep: true})
	if err != nil {
		return err
	}
	return s.finishHeld(ctx, r, cenv, inv, iss, st)
}

// finishHeld finishes inv, open until its taxes were decided and now
// settled as st: an invoice gated by a subscription's payment rule stays
// open and only gets its payment, which resolves the gate, unless its
// total is zero, which satisfies the rule; any other invoice is finalized
// and announced.
//
// source: app/services/invoices/finalize_pending_vies_invoice_service.rb:35-47,73-82
// source: app/services/invoices/provider_taxes/pull_taxes_and_apply_service.rb:55-68,99-108
func (s *Service) finishHeld(ctx context.Context, r port.Repos, cenv *kernel.CustomerEnv, inv *invoice.Invoice, iss []*invoice.Subscription, st settled) error {
	org := inv.OrganizationID
	gate, rules, err := gatingSubscription(ctx, r, org, iss)
	if err != nil {
		return err
	}
	settledTaxes := st.taxFailure == nil && !st.pending
	if gate != nil && settledTaxes && inv.TotalAmount == 0 && subscription.PaymentGated(gate.Subscription, rules) {
		if err := s.skipPaymentGate(ctx, r, gate, rules); err != nil {
			return err
		}
	}
	st.gated = gate != nil && subscription.Gated(gate.Subscription, rules)
	if err := s.finish(ctx, r, cenv, inv, kindOf(inv, iss), st, modeFinalize); err != nil {
		return err
	}
	if !st.gated || !settledTaxes || inv.Status != invoice.StatusOpen {
		return nil
	}
	if inv.TotalAmount > 0 && inv.PaymentStatus != core.PaymentStatusSucceeded && !inv.SelfBilled && !inv.SkipAutomaticPayment {
		return kernel.Enqueue(ctx, r, org, kernel.JobCreateInvoicePayment, kernel.InvoiceArgs{InvoiceID: inv.ID}, "invoice:"+inv.ID.String(), s.d.Now())
	}
	return nil
}

// resetTaxesOnly forgets the taxes of f before it is taxed again, keeping
// its coupon share.
func resetTaxesOnly(f *invoice.Fee) {
	f.AppliedTaxes = nil
	f.TaxesAmount = 0
	f.TaxesPreciseAmount = money.Zero
	f.TaxesRate = money.Zero
	f.TaxesBaseRate = money.Zero
	f.PreciseCreditNotesAmount = money.Zero
}

// gatingSubscription returns the first subscription billed by iss that
// activation rules gate, with its rules (lago Invoice#subscription_gated?,
// app/models/invoice.rb:493-499); nil when none is.
func gatingSubscription(ctx context.Context, r port.Repos, org core.OrganizationID, iss []*invoice.Subscription) (*kernel.SubscriptionEnv, []*subscription.ActivationRule, error) {
	for _, is := range iss {
		env, err := kernel.LoadSubscriptionEnv(ctx, r, org, is.SubscriptionID)
		if err != nil {
			return nil, nil, err
		}
		if env.Subscription.Status != subscription.StatusIncomplete {
			continue
		}
		rules, err := r.ActivationRules.ListBySubscription(ctx, org, env.Subscription.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("load activation rules of %s: %w", env.Subscription.ID, err)
		}
		if subscription.Gated(env.Subscription, rules) {
			return env, rules, nil
		}
	}
	return nil, nil, nil
}
