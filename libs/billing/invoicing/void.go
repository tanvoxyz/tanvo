package invoicing

import (
	"context"
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
			return s.saveError(ctx, r, inv, invoice.ErrorCodeTaxVoidingError, err)
		}
		return nil
	})
	return err
}

// Regenerate creates a new invoice from the voided invoice id (lago
// Invoices::RegenerateFromVoidedService): the fees are copied without
// their taxes and deductions, then deducted, taxed and finalized again
// at once, ignoring the grace period. The new invoice records the voided
// one in VoidedInvoiceID; the invoice subscriptions are not copied (their
// idempotency keys would clash with the voided invoice's).
func (s *Service) Regenerate(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*invoice.Invoice, error) {
	var out *invoice.Invoice
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		old, err := r.Invoices.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load invoice %s: %w", id, err)
		}
		if old.Status != invoice.StatusVoided {
			return core.NewRuleError(CodeNotVoided, "only voided invoices are regenerated")
		}
		out, err = s.regenerateVoided(ctx, r, old)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// regenerateVoided is [Service.Regenerate] in the transaction of r.
func (s *Service) regenerateVoided(ctx context.Context, r port.Repos, old *invoice.Invoice) (*invoice.Invoice, error) {
	org := old.OrganizationID
	cenv, err := kernel.LoadCustomerEnv(ctx, r, org, old.CustomerID, old.BillingEntityID)
	if err != nil {
		return nil, err
	}
	iss, err := r.InvoiceSubscriptions.ListByInvoice(ctx, org, old.ID)
	if err != nil {
		return nil, fmt.Errorf("load subscriptions of invoice %s: %w", old.ID, err)
	}
	oldFees, err := r.Fees.ListByInvoice(ctx, org, old.ID)
	if err != nil {
		return nil, fmt.Errorf("load fees of invoice %s: %w", old.ID, err)
	}
	k := kindOf(old, iss)
	inv := kernel.NewInvoice(s.d, cenv, k, old.Currency, s.d.Now())
	inv.Type = old.Type
	inv.VoidedInvoiceID = old.ID
	inv.PaymentMethodType, inv.PaymentMethodID = old.PaymentMethodType, old.PaymentMethodID
	inv.SkipAutomaticPayment = old.SkipAutomaticPayment
	if err := r.Invoices.Create(ctx, inv); err != nil {
		return nil, fmt.Errorf("create invoice: %w", err)
	}
	now := s.d.Now()
	fs := make([]*invoice.Fee, 0, len(oldFees))
	for _, of := range oldFees {
		f := *of
		f.ID = core.NewID[core.FeeID](s.d.IDs)
		f.InvoiceID = inv.ID
		f.CreatedAt, f.UpdatedAt = now, now
		resetFeeTaxes([]*invoice.Fee{&f})
		if err := r.Fees.Create(ctx, &f); err != nil {
			return nil, fmt.Errorf("create fee: %w", err)
		}
		fs = append(fs, &f)
	}
	resolver, err := s.planResolver(ctx, r, cenv, iss)
	if err != nil {
		return nil, err
	}
	st, err := s.settle(ctx, r, cenv, inv, fs, settleOptions{aggregate: inv.Type == invoice.TypeAdvanceCharges, resolver: resolver})
	if err != nil {
		return nil, err
	}
	if inv.Type == invoice.TypeAdvanceCharges {
		inv.PaymentStatus = core.PaymentStatusSucceeded
	}
	if err := s.finish(ctx, r, cenv, inv, k, st, modeFinalize); err != nil {
		return nil, err
	}
	return inv, nil
}
