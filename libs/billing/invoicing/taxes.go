package invoicing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
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

// Retry finishes an invoice whose taxes failed or are pending (lago
// Invoices::RetryService and ProviderTaxes::PullTaxesAndApplyService): it
// gives back the deductions applied so far, taxes and deducts the stored
// fees again and finalizes the invoice (a draft stays a draft). It
// returns the invoice, still failed or pending when the provider fails
// again.
//
// The provider is called between two transactions, never inside one
// (lago PullTaxesAndApplyService): a first run records what the provider
// is asked and rolls back, the provider is called, and a second run
// applies its answer, or starts over when the invoice changed meanwhile.
func (s *Service) Retry(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*invoice.Invoice, error) {
	call := &kernel.TaxCall{}
	tctx := kernel.WithTaxCall(ctx, call)
	for range maxTaxCalls {
		inv, err := s.onInvoice(tctx, org, id, s.retry)
		if !errors.Is(err, kernel.ErrTaxCall) {
			return inv, err
		}
		call.Call(ctx, s.d.Taxes)
	}
	return nil, fmt.Errorf("retry invoice %s: changed during %d tax provider calls: %w", id, maxTaxCalls, kernel.ErrTaxCall)
}

// maxTaxCalls bounds the provider calls of one [Service.Retry].
const maxTaxCalls = 3

// retry is [Service.Retry] in the transaction of r.
func (s *Service) retry(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
	switch {
	case inv.Status == invoice.StatusFailed,
		inv.TaxStatus == invoice.TaxStatusPending && (inv.Status == invoice.StatusOpen || inv.Status == invoice.StatusDraft),
		inv.TaxStatus == invoice.TaxStatusFailed && inv.Status == invoice.StatusDraft:
	default:
		return fmt.Errorf("retry invoice %s: %w", inv.ID, core.NewRuleError("not_retryable", "the invoice has no tax to retry"))
	}
	org := inv.OrganizationID
	cenv, err := kernel.LoadCustomerEnv(ctx, r, org, inv.CustomerID, inv.BillingEntityID)
	if err != nil {
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
	draft := inv.Status == invoice.StatusDraft
	if draft {
		err = s.deduct.ResetDraft(ctx, inv)
	} else {
		err = s.giveBack(ctx, r, inv)
	}
	if err != nil {
		return err
	}
	resetFeeTaxes(fs)
	resolver, err := s.planResolver(ctx, r, cenv, iss)
	if err != nil {
		return err
	}
	k := kindOf(inv, iss)
	st, err := s.settle(ctx, r, cenv, inv, fs, settleOptions{draft: draft, aggregate: inv.Type == invoice.TypeAdvanceCharges, resolver: resolver})
	if err != nil {
		return err
	}
	mode := modeFinalize
	if draft {
		mode = modeRefresh
	}
	return s.finish(ctx, r, cenv, inv, k, st, mode)
}

// giveBack returns what the deductions of a non-draft invoice consumed
// before it is deducted again, and deletes its credits.
func (s *Service) giveBack(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
	if _, err := s.deduct.Restore(ctx, r, inv, deduct.RestoreOptions{}); err != nil {
		return err
	}
	if err := s.d.Credits.DeleteByInvoice(ctx, inv.OrganizationID, inv.ID); err != nil {
		return fmt.Errorf("delete credits of invoice %s: %w", inv.ID, err)
	}
	inv.ProgressiveBillingCreditAmount, inv.CouponsAmount = 0, 0
	inv.CreditNotesAmount, inv.PrepaidCreditAmount = 0, 0
	inv.PrepaidGrantedCreditAmount, inv.PrepaidPurchasedCreditAmount = 0, 0
	return nil
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
