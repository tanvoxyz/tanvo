package wallets

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// creditKind returns the kind of the credit invoices of paid top-ups:
// issued at once, without grace period.
func creditKind() kernel.InvoiceKind { return kernel.InvoiceKind{Type: invoice.TypeCredit} }

// billPaidCredit issues the credit invoice of the pending purchase t (lago
// Invoices::PaidCreditService): one untaxed credit fee. The invoice stays
// open until paid when t requires a successful payment; otherwise it is
// finalized. Either way the credits settle only when the payment succeeds
// ([Service.HandleCreditInvoicePayment], lago Invoices::PrepaidCreditJob,
// app/jobs/invoices/prepaid_credit_job.rb:20); the payment is requested
// here (domain §14.2).
func (s *Service) billPaidCredit(ctx context.Context, r port.Repos, w *wallet.Wallet, t *wallet.Transaction) error {
	org := w.OrganizationID
	now := s.d.Now()
	// The transaction's snapshot, else the wallet's entity, else the
	// customer's (paid_credit_service.rb:75).
	be := t.BillingEntityID
	if be.IsZero() {
		be = w.BillingEntityID
	}
	env, err := kernel.LoadCustomerEnv(ctx, r, org, w.CustomerID, be)
	if err != nil {
		return err
	}
	inv := kernel.NewInvoice(s.d, env, creditKind(), w.Currency, now)
	inv.PaymentMethodType, inv.PaymentMethodID = t.PaymentMethodType, t.PaymentMethodID
	// lago Invoices::PaidCreditService (paid_credit_service.rb:76).
	inv.PurchaseOrderNumber = wallet.ResolvePurchaseOrderNumber(t.PurchaseOrderNumber, w.PurchaseOrderNumber)
	if err := r.Invoices.Create(ctx, inv); err != nil {
		return fmt.Errorf("create credit invoice: %w", err)
	}
	// lago stores rate_amount × subunits into the bigint unit_amount_cents,
	// which drops the fraction of a cent (wallet_transaction.rb:99), and
	// copies that unit amount into precise_unit_amount
	// (fees/paid_credit_service.rb:40).
	unit, err := w.Currency.ToMinor(w.RateAmount, money.Down)
	if err != nil {
		return fmt.Errorf("credit unit amount: %w", err)
	}
	fee := &invoice.Fee{
		ID:                  core.NewID[core.FeeID](s.d.IDs),
		OrganizationID:      org,
		InvoiceID:           inv.ID,
		Type:                core.FeeTypeCredit,
		WalletTransactionID: t.ID,
		Amount:              t.Amount,
		PreciseAmount:       kernel.Cents(t.Amount),
		UnitAmount:          unit,
		// In currency units, as the fee's unit amount (lago
		// Fees::PaidCreditService, precise_unit_amount = unit_amount).
		PreciseUnitAmount:  w.Currency.FromMinor(unit),
		Units:              t.CreditAmount,
		TaxesRate:          money.Zero,
		TaxesPreciseAmount: money.Zero,
		Currency:           w.Currency,
		Invoiceable:        true,
		PaymentStatus:      invoice.FeePaymentStatusPending,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := r.Fees.Create(ctx, fee); err != nil {
		return fmt.Errorf("create credit fee: %w", err)
	}
	t.InvoiceID = inv.ID
	t.UpdatedAt = now
	if err := r.WalletTransactions.Update(ctx, t); err != nil {
		return fmt.Errorf("update wallet transaction %s: %w", t.ID, err)
	}

	inv.ApplyTotals(invoice.ComputeTotals([]int64{fee.Amount}, invoice.Deductions{}))
	// paid_credit_service.rb:29-32: the wallet transaction's resource
	// (WalletTransaction#invoice_custom_section_resource).
	res, err := sectionResource(ctx, r, w, t)
	if err != nil {
		return err
	}
	if err := kernel.ApplyCustomSections(ctx, s.d, r, env, inv, nil, []kernel.CustomSectionResource{res}); err != nil {
		return err
	}
	if t.InvoiceRequiresSuccessfulPayment {
		if err := inv.Transition(invoice.StatusOpen, now); err != nil {
			return fmt.Errorf("open credit invoice %s: %w", inv.ID, err)
		}
	} else if err := kernel.Finalize(ctx, r, env, inv, creditKind(), now); err != nil {
		return err
	}
	if err := r.Invoices.Update(ctx, inv); err != nil {
		return fmt.Errorf("update credit invoice %s: %w", inv.ID, err)
	}
	if inv.Status == invoice.StatusFinalized {
		if err := s.creditInvoiceFinalized(ctx, r, inv); err != nil {
			return err
		}
	}
	return kernel.Enqueue(ctx, r, org, kernel.JobCreateInvoicePayment, kernel.InvoiceArgs{InvoiceID: inv.ID}, inv.ID.String(), time.Time{})
}

// creditInvoiceFinalized emits invoice.paid_credit_added and requests the
// document of a finalized credit invoice.
func (s *Service) creditInvoiceFinalized(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
	if err := kernel.Emit(ctx, s.d, inv.OrganizationID, webhook.EventTypeInvoicePaidCreditAdded, "invoice", kernel.ObjectID(inv.ID), inv); err != nil {
		return err
	}
	// source: app/services/invoices/paid_credit_service.rb:44,
	// app/services/invoices/finalize_open_credit_service.rb:25
	if err := s.d.LogInvoice(ctx, r, auditlog.ActivityInvoicePaidCreditAdded, inv); err != nil {
		return err
	}
	return kernel.EnqueueInvoiceDocuments(ctx, r, inv, time.Time{})
}

// HandleCreditInvoicePayment settles or fails the purchase of a credit
// invoice after its payment status changed (lago Invoices::PrepaidCreditJob):
// on success the credits are applied and an open invoice is finalized,
// dated today (lago Invoices::FinalizeOpenCreditService); on failure, or
// when the invoice is voided or settled by an offset credit note, the
// pending purchase is marked failed. A purchase failed by a payment and
// paid later still settles; one of a voided invoice does not (see
// [Service.HandleCreditInvoiceVoided]). Other invoice types are ignored.
//
// It is the entry point the payment-status update (lago
// Invoices::UpdateService#handle_prepaid_credits,
// app/services/invoices/update_service.rb:113-117) calls for a credit
// invoice whose payment status became succeeded or failed; that update
// belongs to the payments orchestration, which does not exist yet, so the
// method has no production caller.
func (s *Service) HandleCreditInvoicePayment(ctx context.Context, org core.OrganizationID, id core.InvoiceID, succeeded bool) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		inv, t, err := lockedCreditPurchase(ctx, r, org, id)
		if err != nil || t == nil {
			return err
		}
		if succeeded && inv.Status != invoice.StatusVoided {
			// An invoice settled by an offset credit note is not paid, so it
			// buys no credits (lago PrepaidCreditJob#paid_by_credit_note?).
			sts, err := r.InvoiceSettlements.ListByInvoice(ctx, org, inv.ID)
			if err != nil {
				return fmt.Errorf("list settlements of invoice %s: %w", inv.ID, err)
			}
			for _, st := range sts {
				if st.Type == invoice.SettlementTypeCreditNote {
					succeeded = false
					break
				}
			}
		} else {
			succeeded = false
		}
		if !succeeded {
			// lago Invoices::PrepaidCreditJob calls MarkAsFailedService
			// (prepaid_credit_job.rb:24).
			return s.markAsFailed(ctx, r, t, true)
		}
		if err := s.applyPaidCredits(ctx, r, t); err != nil {
			return err
		}
		if inv.Status != invoice.StatusOpen {
			return nil
		}
		now := s.d.Now()
		env, err := kernel.LoadCustomerEnv(ctx, r, org, inv.CustomerID, inv.BillingEntityID)
		if err != nil {
			return err
		}
		inv.IssuingDate = env.Today(now)
		inv.PaymentDueDate = inv.IssuingDate
		if err := kernel.Finalize(ctx, r, env, inv, creditKind(), now); err != nil {
			return err
		}
		if err := r.Invoices.Update(ctx, inv); err != nil {
			return fmt.Errorf("update credit invoice %s: %w", inv.ID, err)
		}
		return s.creditInvoiceFinalized(ctx, r, inv)
	})
}

// HandleCreditInvoiceVoided fails the pending purchase of a voided credit
// invoice (domain §14.2), and [Service.HandleCreditInvoicePayment] then
// refuses to settle it. This deviates from lago, whose void leaves the
// wallet transaction pending (app/services/invoices/void_service.rb:30-48)
// and whose PrepaidCreditJob grants the credits of a voided invoice paid
// later (app/jobs/invoices/prepaid_credit_job.rb:20-25): a voided invoice
// is no longer owed, so a payment against it is a refund matter, not a
// purchase.
func (s *Service) HandleCreditInvoiceVoided(ctx context.Context, org core.OrganizationID, id core.InvoiceID) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		_, t, err := lockedCreditPurchase(ctx, r, org, id)
		if err != nil || t == nil {
			return err
		}
		return s.markAsFailed(ctx, r, t, false)
	})
}

// lockedCreditPurchase takes the lock of invoice id and its customer's
// prepaid_credits lock, then loads the invoice and its purchase with
// [creditPurchase], so that concurrent payment notifications settle the
// purchase once.
func lockedCreditPurchase(ctx context.Context, r port.Repos, org core.OrganizationID, id core.InvoiceID) (*invoice.Invoice, *wallet.Transaction, error) {
	if err := kernel.LockInvoice(ctx, r, org, id); err != nil {
		return nil, nil, err
	}
	inv, err := r.Invoices.Get(ctx, org, id)
	if err != nil {
		return nil, nil, fmt.Errorf("load invoice %s: %w", id, err)
	}
	if inv.Type != invoice.TypeCredit {
		return inv, nil, nil
	}
	if err := lockCustomerCredits(ctx, r, org, inv.CustomerID); err != nil {
		return nil, nil, err
	}
	return creditPurchase(ctx, r, org, id)
}

// creditPurchase loads a credit invoice and the purchase its credit fee
// bills; the purchase is nil for other invoices.
func creditPurchase(ctx context.Context, r port.Repos, org core.OrganizationID, id core.InvoiceID) (*invoice.Invoice, *wallet.Transaction, error) {
	inv, err := r.Invoices.Get(ctx, org, id)
	if err != nil {
		return nil, nil, fmt.Errorf("load invoice %s: %w", id, err)
	}
	if inv.Type != invoice.TypeCredit {
		return inv, nil, nil
	}
	fees, err := r.Fees.ListByInvoice(ctx, org, id)
	if err != nil {
		return nil, nil, fmt.Errorf("list fees of invoice %s: %w", id, err)
	}
	for _, f := range fees {
		if f.Type != core.FeeTypeCredit || f.WalletTransactionID.IsZero() {
			continue
		}
		t, err := r.WalletTransactions.Get(ctx, org, f.WalletTransactionID)
		if err != nil {
			return nil, nil, fmt.Errorf("load wallet transaction %s: %w", f.WalletTransactionID, err)
		}
		return inv, t, nil
	}
	return inv, nil, nil
}
