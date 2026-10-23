package invoicing

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// CodeNotVoided rejects the regeneration of an invoice that is not voided.
const CodeNotVoided = "not_voided"

// VoidInput are the options of [Service.Void].
type VoidInput struct {
	// GenerateCreditNote credits the voided invoice with credit notes
	// instead of giving its wallet and credit note credits back.
	GenerateCreditNote bool
	// CreditAmount and RefundAmount, in minor units, split the requested
	// credit note of GenerateCreditNote; both zero issues none.
	CreditAmount int64
	RefundAmount int64
}

// Void voids the finalized invoice id (lago Invoices::VoidService): coupon
// credits are given back, and so are, without in.GenerateCreditNote, its
// credit note and wallet credits (domain §11.4). With it, the requested
// credit note of in's amounts is issued over a share of every fee, and
// what stays creditable goes on a second note voided at once, so the
// credits the invoice consumed stay consumed. The purchase of a credit
// invoice fails, invoice.voided is emitted after the notes'
// credit_note.created and the tax provider is told. An invoice that
// cannot be voided fails with a *core.RuleError "not_voidable"; amounts
// above what the invoice may credit or refund fail with a
// *core.ValidationError on credit_refund_amount.
func (s *Service) Void(ctx context.Context, org core.OrganizationID, id core.InvoiceID, in VoidInput) (*invoice.Invoice, error) {
	return s.onInvoice(ctx, org, id, func(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
		return s.void(ctx, r, inv, in)
	})
}

// void is [Service.Void] in the transaction of r.
func (s *Service) void(ctx context.Context, r port.Repos, inv *invoice.Invoice, in VoidInput) error {
	org := inv.OrganizationID
	// Credit notes and voids of the customer's invoices are serialized,
	// then the invoice is read again (lago invoice.with_lock). The void
	// restores coupons and wallet credits too, so it takes every balance
	// lock up front (kernel lock order).
	if err := kernel.LockCustomerBalances(ctx, r, org, inv.CustomerID); err != nil {
		return err
	}
	fresh, err := r.Invoices.Get(ctx, org, inv.ID)
	if err != nil {
		return fmt.Errorf("load invoice %s: %w", inv.ID, err)
	}
	*inv = *fresh
	// The amounts are checked first, as in lago, but under the lock.
	if in.GenerateCreditNote {
		if err := s.creditNotes.CheckVoidAmounts(ctx, r, org, inv.ID, in.CreditAmount, in.RefundAmount); err != nil {
			return err
		}
	}
	notes, err := r.CreditNotes.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return fmt.Errorf("load credit notes of invoice %s: %w", inv.ID, err)
	}
	var facts invoice.VoidFacts
	for _, cn := range notes {
		if cn.Status == creditnote.StatusFinalized && cn.CreditStatus != creditnote.CreditStatusVoided {
			facts.NonVoidedCreditNotes++
		}
	}
	if err := inv.Void(facts, s.d.Now()); err != nil {
		return fmt.Errorf("void invoice %s: %w", inv.ID, err)
	}
	if err := s.flagInvoiceUsage(ctx, r, inv); err != nil {
		return err
	}
	if _, err := s.deduct.Restore(ctx, r, inv, deduct.RestoreOptions{IssuesCreditNote: in.GenerateCreditNote}); err != nil {
		return err
	}
	if err := r.Invoices.Update(ctx, inv); err != nil {
		return fmt.Errorf("save invoice %s: %w", inv.ID, err)
	}
	if in.GenerateCreditNote {
		if _, err := s.creditNotes.IssueVoidNotes(ctx, r, org, inv.ID, in.CreditAmount, in.RefundAmount); err != nil {
			return err
		}
		fresh, err := r.Invoices.Get(ctx, org, inv.ID)
		if err != nil {
			return fmt.Errorf("load invoice %s: %w", inv.ID, err)
		}
		*inv = *fresh
	}
	if inv.Type == invoice.TypeCredit && s.creditInvoices != nil {
		if err := s.creditInvoices.HandleCreditInvoiceVoided(ctx, org, inv.ID); err != nil {
			return err
		}
	}
	if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeInvoiceVoided, "invoice", kernel.ObjectID(inv.ID), inv); err != nil {
		return err
	}
	return kernel.Enqueue(ctx, r, org, kernel.JobVoidProviderTaxes, kernel.InvoiceArgs{InvoiceID: inv.ID}, "invoice:"+inv.ID.String(), s.d.Now())
}

// flagInvoiceUsage flags the lifetime usage of the subscriptions of inv.
func (s *Service) flagInvoiceUsage(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
	iss, err := r.InvoiceSubscriptions.ListByInvoice(ctx, inv.OrganizationID, inv.ID)
	if err != nil {
		return fmt.Errorf("load subscriptions of invoice %s: %w", inv.ID, err)
	}
	subs := make([]core.SubscriptionID, len(iss))
	for i, is := range iss {
		subs[i] = is.SubscriptionID
	}
	return s.flagLifetimeUsage(ctx, r, inv.OrganizationID, subs)
}

// VoidProviderTaxes tells the tax provider that the invoice id was voided
// (lago Invoices::ProviderTaxes::VoidService); a provider failure is
// stored as a tax_voiding_error detail.
func (s *Service) VoidProviderTaxes(ctx context.Context, org core.OrganizationID, id core.InvoiceID) error {
	if s.d.Taxes == nil {
		return nil
	}
	_, err := s.onInvoice(ctx, org, id, func(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
		c, err := r.Customers.Get(ctx, org, inv.CustomerID)
		if err != nil {
			return fmt.Errorf("load customer %s: %w", inv.CustomerID, err)
		}
		on, err := s.d.Taxes.Enabled(ctx, c)
		if err != nil || !on {
			return err
		}
		if err := r.ErrorDetails.DeleteByInvoice(ctx, org, inv.ID, invoice.ErrorCodeTaxVoidingError); err != nil {
			return fmt.Errorf("clear tax voiding errors of invoice %s: %w", inv.ID, err)
		}
		if err := s.d.Taxes.VoidInvoice(ctx, c, inv); err != nil {
			if errors.Is(err, kernel.ErrTaxContention) {
				// Retried by the job (lago
				// app/services/integrations/aggregator/taxes/invoices/base_service.rb:87).
				return fmt.Errorf("void taxes of invoice %s: %w", inv.ID, err)
			}
			return s.saveError(ctx, r, inv, invoice.ErrorCodeTaxVoidingError, err)
		}
		return nil
	})
	return err
}

// Regenerate creates a new invoice from the voided invoice id (lago
// Invoices::RegenerateFromVoidedService): it bills the subscriptions of
// the voided invoice, whose invoice subscriptions it takes over, with the
// fees of in.Fees ([RegeneratedFee]) and the voided invoice's applied
// usage thresholds, then deducts, taxes and finalizes them at once,
// ignoring the grace period. The new invoice records the voided one in
// VoidedInvoiceID.
func (s *Service) Regenerate(ctx context.Context, org core.OrganizationID, id core.InvoiceID, in RegenerateInput) (*invoice.Invoice, error) {
	var out *invoice.Invoice
	err := s.inRegeneration(ctx, org, id, in, func(_ context.Context, _ port.Repos, inv *invoice.Invoice) error {
		out = inv
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// inRegeneration validates in, regenerates the voided invoice id in a
// transaction and runs then on the new invoice in it.
func (s *Service) inRegeneration(ctx context.Context, org core.OrganizationID, id core.InvoiceID, in RegenerateInput, then func(context.Context, port.Repos, *invoice.Invoice) error) error {
	po := in.PurchaseOrderNumber
	if po != nil {
		po = core.NormalizePurchaseOrderNumber(po)
		var v core.Validator
		if !v.OptionalString("purchase_order_number", po, core.MaxPurchaseOrderNumberLength) {
			return v.Err()
		}
	}
	if err := validateRegeneratedFees(in.Fees); err != nil {
		return err
	}
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		old, err := r.Invoices.Get(ctx, org, id)
		if err == nil && !(invoice.Filter{}).Lists(old.Status) {
			err = core.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load invoice %s: %w", id, asNotFound(err, "invoice"))
		}
		if old.Status != invoice.StatusVoided {
			return core.NewRuleError(CodeNotVoided, "only voided invoices are regenerated")
		}
		inv, err := s.regenerateVoided(ctx, r, old, in.PurchaseOrderNumber != nil, po, in.Fees)
		if err != nil {
			return err
		}
		return then(ctx, r, inv)
	})
}

// errDryRun rolls back the transaction of a preview.
var errDryRun = errors.New("invoicing: dry run")

// RegenerateInput are the options of [Service.Regenerate].
type RegenerateInput struct {
	// PurchaseOrderNumber replaces the voided invoice's purchase order
	// number on the new invoice (trimmed, ≤ 255 characters); blank clears
	// it, nil inherits it (regenerate_from_voided_service.rb:7, :295-299).
	PurchaseOrderNumber *string
	// Fees are the fees of the new invoice; the voided invoice's fees not
	// listed are left out.
	Fees []RegeneratedFee
}

// regenerateVoided is [Service.Regenerate] in the transaction of r; with
// replacePO, the new invoice takes po instead of the voided invoice's
// purchase order number; params are its fees.
func (s *Service) regenerateVoided(ctx context.Context, r port.Repos, old *invoice.Invoice, replacePO bool, po *string, params []RegeneratedFee) (*invoice.Invoice, error) {
	org := old.OrganizationID
	cenv, err := kernel.LoadCustomerEnv(ctx, r, org, old.CustomerID, old.BillingEntityID)
	if err != nil {
		return nil, err
	}
	oldSubs, err := r.InvoiceSubscriptions.ListByInvoice(ctx, org, old.ID)
	if err != nil {
		return nil, fmt.Errorf("load subscriptions of invoice %s: %w", old.ID, err)
	}
	oldFees, err := r.Fees.ListByInvoice(ctx, org, old.ID)
	if err != nil {
		return nil, fmt.Errorf("load fees of invoice %s: %w", old.ID, err)
	}
	// The generating dates, kept while taxes are pending, come from the
	// voided invoice's creation as a non-recurring invoice, with the grace
	// period for subscription invoices only (lago
	// app/services/invoices/regenerate_from_voided_service.rb:278-284,
	// app/services/invoices/create_generating_service.rb:15,64-81).
	dating := kernel.InvoiceKind{Type: old.Type, UsesGracePeriod: old.Type == invoice.TypeSubscription}
	inv := kernel.NewInvoice(s.d, cenv, dating, old.Currency, old.CreatedAt)
	inv.Type = old.Type
	inv.VoidedInvoiceID = old.ID
	inv.PaymentMethodType, inv.PaymentMethodID = old.PaymentMethodType, old.PaymentMethodID
	inv.SkipAutomaticPayment = old.SkipAutomaticPayment
	inv.PurchaseOrderNumber = old.PurchaseOrderNumber
	if replacePO {
		inv.PurchaseOrderNumber = po
	}
	if err := r.Invoices.Create(ctx, inv); err != nil {
		return nil, fmt.Errorf("create invoice: %w", err)
	}
	iss, err := s.copyRegeneratedSubscriptions(ctx, r, old, inv, oldSubs)
	if err != nil {
		return nil, err
	}
	k := kindOf(old, iss)
	fs, err := s.regeneratedFees(ctx, r, inv, oldFees, params)
	if err != nil {
		return nil, err
	}
	if err := s.copyAppliedUsageThresholds(ctx, r, old, inv); err != nil {
		return nil, err
	}
	if err := s.applyCustomSections(ctx, r, cenv, inv, nil); err != nil {
		return nil, err
	}
	resolver, err := s.planResolver(ctx, r, cenv, iss)
	if err != nil {
		return nil, err
	}
	st, err := s.settle(ctx, r, cenv, inv, fs, settleOptions{aggregate: inv.Type == invoice.TypeAdvanceCharges, resolver: resolver, vies: true})
	if err != nil {
		return nil, err
	}
	if inv.Type == invoice.TypeAdvanceCharges {
		inv.PaymentStatus = core.PaymentStatusSucceeded
	}
	// Taxes still pending keep the generating dates until they are pulled
	// (lago regenerate_from_voided_service.rb:43-47).
	if !st.pending {
		kernel.RedateRegenerated(inv, cenv, s.d.Now())
	}
	if err := s.finish(ctx, r, cenv, inv, k, st, modeFinalize); err != nil {
		return nil, err
	}
	return inv, nil
}

// copyRegeneratedSubscriptions gives the invoice subscriptions old of the
// voided invoice to inv, regenerated from it (lago
// Invoices::RegenerateFromVoidedService#create_invoice_subscriptions):
// the voided rows record inv, releasing their keys, and inv gets copies.
func (s *Service) copyRegeneratedSubscriptions(ctx context.Context, r port.Repos, voided, inv *invoice.Invoice, old []*invoice.Subscription) ([]*invoice.Subscription, error) {
	if len(old) == 0 {
		return nil, nil
	}
	if err := r.InvoiceSubscriptions.MarkRegenerated(ctx, inv.OrganizationID, voided.ID, inv.ID); err != nil {
		return nil, fmt.Errorf("release subscriptions of invoice %s: %w", voided.ID, err)
	}
	out := make([]*invoice.Subscription, 0, len(old))
	for _, o := range old {
		c := *o
		c.ID = core.NewID[core.InvoiceSubscriptionID](s.d.IDs)
		c.InvoiceID = inv.ID
		c.RegeneratedInvoiceID = core.InvoiceID{}
		c.CreatedAt = s.d.Now()
		if err := r.InvoiceSubscriptions.Create(ctx, &c); err != nil {
			return nil, fmt.Errorf("copy invoice subscription %s: %w", o.ID, err)
		}
		out = append(out, &c)
	}
	return out, nil
}

// copyAppliedUsageThresholds copies the applied usage thresholds of the
// voided progressive billing invoice onto inv, regenerated from it (lago
// Invoices::RegenerateFromVoidedService#assign_applied_usage_thresholds).
func (s *Service) copyAppliedUsageThresholds(ctx context.Context, r port.Repos, voided, inv *invoice.Invoice) error {
	if voided.Type != invoice.TypeProgressiveBilling {
		return nil
	}
	as, err := r.AppliedUsageThresholds.ListByInvoice(ctx, inv.OrganizationID, voided.ID)
	if err != nil {
		return fmt.Errorf("list applied usage thresholds of invoice %s: %w", voided.ID, err)
	}
	now := s.d.Now()
	for _, a := range as {
		c := *a
		c.ID = core.NewID[core.AppliedUsageThresholdID](s.d.IDs)
		c.InvoiceID = inv.ID
		c.CreatedAt, c.UpdatedAt = now, now
		if err := r.AppliedUsageThresholds.Create(ctx, &c); err != nil {
			return fmt.Errorf("copy applied usage threshold %s: %w", a.ID, err)
		}
	}
	return nil
}
