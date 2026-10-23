package invoicing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// pullTaxesDelay is the wait before asking a tax provider again for
// pending taxes.
const pullTaxesDelay = time.Minute

// taxStandalone applies the taxes of fees attached to no invoice (lago
// Fees::ApplyTaxesService and ApplyProviderTaxesToStandaloneFeesService).
// A provider failure leaves the fee untaxed and emits
// fee.tax_provider_error.
func (s *Service) taxStandalone(ctx context.Context, r port.Repos, cenv *kernel.CustomerEnv, env *kernel.SubscriptionEnv, fs []*invoice.Fee) error {
	resolver := kernel.NewTaxResolver(r, cenv)
	if env != nil {
		resolver.SetPlan(env.Subscription.ID, env.Plan)
	}
	for _, f := range fs {
		stub := &invoice.Invoice{OrganizationID: f.OrganizationID, CustomerID: cenv.Customer.ID, Currency: f.Currency, Type: invoice.TypeSubscription}
		_, err := resolver.Compute(ctx, s.d, kernel.TaxInput{Invoice: stub, Fees: []*invoice.Fee{f}, Inline: true})
		switch {
		case err == nil:
		case errors.Is(err, kernel.ErrTaxProvider):
			if err := kernel.Emit(ctx, s.d, f.OrganizationID, webhook.EventTypeFeeTaxProviderError, "fee", kernel.ObjectID(f.ID), f); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

// resetFeeTaxes forgets the taxes and deduction shares of fees before they
// are taxed again.
func resetFeeTaxes(fs []*invoice.Fee) {
	for _, f := range fs {
		f.AppliedTaxes = nil
		f.TaxesAmount = 0
		f.TaxesPreciseAmount = money.Zero
		f.TaxesRate = money.Zero
		f.TaxesBaseRate = money.Zero
		f.PreciseCouponsAmount = money.Zero
		f.PreciseCreditNotesAmount = money.Zero
	}
}

// Retry reopens the failed invoice id and queues the pull of its taxes
// (lago Invoices::RetryService): the invoice is open again with its taxes
// pending, under its lock, and [kernel.JobPullProviderTaxes] runs
// [Service.PullTaxes]. Another status fails with [CodeInvalidStatus].
//
// source: app/services/invoices/retry_service.rb:13-41
func (s *Service) Retry(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*invoice.Invoice, error) {
	return s.onInvoice(ctx, org, id, func(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
		if inv.Status != invoice.StatusFailed {
			return fmt.Errorf("retry invoice %s: %w", inv.ID, core.NewRuleError(CodeInvalidStatus, "only failed invoices are retried"))
		}
		return s.reopen(ctx, r, inv)
	})
}

// reopen is [Service.Retry] of the failed inv in the transaction of r.
// Tanvo has no pending status: the invoice waits open, gated or not
// (retry_service.rb:37-38).
func (s *Service) reopen(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
	now := s.d.Now()
	if err := inv.Transition(invoice.StatusOpen, now); err != nil {
		return fmt.Errorf("retry invoice %s: %w", inv.ID, err)
	}
	inv.TaxStatus = invoice.TaxStatusPending
	inv.UpdatedAt = now
	if err := r.Invoices.Update(ctx, inv); err != nil {
		return fmt.Errorf("save invoice %s: %w", inv.ID, err)
	}
	return kernel.Enqueue(ctx, r, inv.OrganizationID, kernel.JobPullProviderTaxes, kernel.InvoiceArgs{InvoiceID: inv.ID}, "invoice:"+inv.ID.String(), now)
}

// PullTaxes applies the provider taxes of the invoice id waiting for them
// (lago Invoices::ProviderTaxes::PullTaxesAndApplyService): the
// deductions already applied are kept and only the taxes and totals are
// computed, then credit notes and prepaid credits are applied (not on a
// draft or one-off invoice) and the invoice is finalized (a draft stays a
// draft and is announced ready to finalize). An invoice that is not open
// or a draft with pending taxes is left as it is; a customer the provider
// does not tax fails with a [core.NotFoundError] of integration_customer. When the provider fails, the
// invoice fails (a draft keeps its status) with the taxes failed.
//
// The provider is called between two transactions, never inside one
// (lago PullTaxesAndApplyService): a first run records what the provider
// is asked and rolls back, the provider is called, and a second run
// applies its answer, or starts over when the invoice changed meanwhile.
func (s *Service) PullTaxes(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*invoice.Invoice, error) {
	call := &kernel.TaxCall{}
	tctx := kernel.WithTaxCall(ctx, call)
	for range maxTaxCalls {
		inv, err := s.onInvoice(tctx, org, id, s.pullTaxes)
		if !errors.Is(err, kernel.ErrTaxCall) {
			return inv, err
		}
		call.Call(ctx, s.d.Taxes)
	}
	return nil, fmt.Errorf("pull taxes of invoice %s: changed during %d tax provider calls: %w", id, maxTaxCalls, kernel.ErrTaxCall)
}

// maxTaxCalls bounds the provider calls of one [Service.PullTaxes].
const maxTaxCalls = 3

// pullTaxes is [Service.PullTaxes] in the transaction of r.
//
// source: app/services/invoices/provider_taxes/pull_taxes_and_apply_service.rb:14-90
func (s *Service) pullTaxes(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
	org := inv.OrganizationID
	cenv, err := kernel.LoadCustomerEnv(ctx, r, org, inv.CustomerID, inv.BillingEntityID)
	if err != nil {
		return err
	}
	// pull_taxes_and_apply_service.rb:16.
	on := false
	if s.d.Taxes != nil {
		if on, err = s.d.Taxes.Enabled(ctx, cenv.Customer); err != nil {
			return fmt.Errorf("pull taxes of invoice %s: %w: %w", inv.ID, kernel.ErrTaxProvider, err)
		}
	}
	if !on {
		return fmt.Errorf("pull taxes of invoice %s: %w", inv.ID, core.NotFound("integration_customer"))
	}
	// pull_taxes_and_apply_service.rb:17-18; Tanvo has no pending status.
	draft := inv.Status == invoice.StatusDraft
	if (!draft && inv.Status != invoice.StatusOpen) || inv.TaxStatus != invoice.TaxStatusPending {
		return nil
	}
	iss, err := r.InvoiceSubscriptions.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return fmt.Errorf("load subscriptions of invoice %s: %w", inv.ID, err)
	}
	fs, err := r.Fees.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return fmt.Errorf("load fees of invoice %s: %w", inv.ID, err)
	}
	// The provider taxes every fee again (lago Fees::ApplyProviderTaxesService);
	// the progressive billing credits and coupons applied are kept
	// (pull_taxes_and_apply_service.rb:48, lago Invoices::ComputeAmountsFromFees).
	for _, f := range fs {
		resetTaxesOnly(f)
	}
	resolver, err := s.planResolver(ctx, r, cenv, iss)
	if err != nil {
		return err
	}
	// A gated invoice asks for draft taxes (pull_taxes_and_apply_service.rb:21-25).
	gated := false
	if !draft {
		gate, _, err := gatingSubscription(ctx, r, org, iss)
		if err != nil {
			return err
		}
		gated = gate != nil
	}
	st, err := s.settle(ctx, r, cenv, inv, fs, settleOptions{draft: draft, aggregate: inv.Type == invoice.TypeAdvanceCharges, resolver: resolver, keep: true, draftTaxes: gated})
	if err != nil {
		return err
	}
	if draft {
		if err := s.finish(ctx, r, cenv, inv, kindOf(inv, iss), st, modeRefresh); err != nil {
			return err
		}
		if st.pending {
			return nil
		}
		// pull_taxes_and_apply_service.rb:32,76-77,94-97.
		return kernel.Emit(ctx, s.d, org, webhook.EventTypeInvoiceReadyToFinalize, "invoice", kernel.ObjectID(inv.ID), inv)
	}
	return s.finishHeld(ctx, r, cenv, inv, iss, st)
}

// planResolver returns a tax resolver knowing the plans of the invoice's
// subscriptions.
func (s *Service) planResolver(ctx context.Context, r port.Repos, cenv *kernel.CustomerEnv, iss []*invoice.Subscription) (*kernel.TaxResolver, error) {
	resolver := kernel.NewTaxResolver(r, cenv)
	for _, is := range iss {
		sub, err := r.Subscriptions.Get(ctx, cenv.OrganizationID(), is.SubscriptionID)
		if err != nil {
			return nil, fmt.Errorf("load subscription %s: %w", is.SubscriptionID, err)
		}
		plan, err := r.Plans.Get(ctx, cenv.OrganizationID(), sub.PlanID)
		if err != nil {
			return nil, fmt.Errorf("load plan %s: %w", sub.PlanID, err)
		}
		resolver.SetPlan(sub.ID, plan)
	}
	return resolver, nil
}

// createdEvent returns the webhook announcing a finalized invoice.
func createdEvent(inv *invoice.Invoice) webhook.EventType {
	if inv.Type == invoice.TypeOneOff {
		return webhook.EventTypeInvoiceOneOffCreated
	}
	return webhook.EventTypeInvoiceCreated
}
