package deduct

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// RestoreOptions tune [Service.Restore].
type RestoreOptions struct {
	// IssuesCreditNote marks a void that issues a credit note over the
	// invoice's creditable amount (lago Invoices::VoidService with
	// generate_credit_note). That amount already counts what wallets and
	// credit notes paid ([invoice.Fee.CreditableAmount] deducts neither),
	// so their credits stay consumed and only coupons are given back;
	// giving them back too would credit the customer twice.
	IssuesCreditNote bool
}

// Restored is the outcome of [Service.Restore].
type Restored struct {
	// AppliedCoupons are the applied coupons given back their credit.
	AppliedCoupons []*coupon.AppliedCoupon
	// CreditNotes are the credit notes given back their consumed credit.
	CreditNotes []*creditnote.CreditNote
	// WalletTransactions are the granted inbound transactions returning
	// the consumed wallet credits.
	WalletTransactions []*wallet.Transaction
}

// Restore gives back what a voided invoice consumed (domain §11.4), in
// the caller's transaction r: each coupon credit returns to its applied
// coupon (lago AppliedCoupons::RecreditService), each credit-note credit
// to its credit note unless voided (CreditNotes::RecreditService), and the
// credits of each outbound wallet transaction return to its wallet, when
// active, as granted credits (WalletTransactions::RecreditService). With
// opts.IssuesCreditNote only the coupons are given back.
//
// Lago no longer gives credit-note credits back on void (void_service.rb
// since 4a64ba268), so that a credit note issued on void cannot credit
// them twice; Tanvo restores them on a plain void (domain §11.4), where
// no credit note is issued, and keeps them consumed otherwise.
// Progressive billing credits need nothing: credits of voided invoices are
// no longer active.
func (s *Service) Restore(ctx context.Context, r port.Repos, inv *invoice.Invoice, opts RestoreOptions) (Restored, error) {
	var out Restored
	org := inv.OrganizationID
	credits, err := r.Credits.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return out, fmt.Errorf("list credits of invoice %s: %w", inv.ID, err)
	}
	now := s.d.Now()
	locked := map[customer.LockScope]bool{}
	lock := func(sc customer.LockScope) error {
		if locked[sc] {
			return nil
		}
		locked[sc] = true
		return kernel.LockCustomer(ctx, r, org, inv.CustomerID, sc)
	}
	for _, c := range credits {
		switch c.Kind() {
		case invoice.CreditKindCoupon:
			if err := lock(customer.LockScopeCoupons); err != nil {
				return out, err
			}
			a, err := r.AppliedCoupons.Get(ctx, org, c.AppliedCouponID)
			if err != nil {
				return out, fmt.Errorf("get applied coupon %s: %w", c.AppliedCouponID, err)
			}
			cp, err := r.Coupons.Get(ctx, org, a.CouponID)
			if err != nil {
				return out, fmt.Errorf("get coupon %s: %w", a.CouponID, err)
			}
			a.Restore(c.Amount, cp.Status == coupon.StatusTerminated, now)
			if err := r.AppliedCoupons.Update(ctx, a); err != nil {
				return out, fmt.Errorf("update applied coupon %s: %w", a.ID, err)
			}
			out.AppliedCoupons = append(out.AppliedCoupons, a)
		case invoice.CreditKindCreditNote:
			if opts.IssuesCreditNote {
				continue
			}
			if err := lock(customer.LockScopeCreditNotes); err != nil {
				return out, err
			}
			cn, err := r.CreditNotes.Get(ctx, org, c.CreditNoteID)
			if err != nil {
				return out, fmt.Errorf("get credit note %s: %w", c.CreditNoteID, err)
			}
			if cn.CreditStatus == creditnote.CreditStatusVoided {
				continue
			}
			if err := cn.Recredit(c.Amount, now); err != nil {
				return out, fmt.Errorf("recredit credit note %s: %w", cn.ID, err)
			}
			if err := r.CreditNotes.Update(ctx, cn); err != nil {
				return out, fmt.Errorf("update credit note %s: %w", cn.ID, err)
			}
			out.CreditNotes = append(out.CreditNotes, cn)
		case invoice.CreditKindProgressiveBilling:
		}
	}
	if opts.IssuesCreditNote {
		return out, nil
	}
	txs, err := r.WalletTransactions.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return out, fmt.Errorf("list wallet transactions of invoice %s: %w", inv.ID, err)
	}
	for _, t := range txs {
		if !t.Outbound() || t.TransactionStatus != wallet.TransactionStatusInvoiced {
			continue
		}
		if err := lock(customer.LockScopePrepaidCredits); err != nil {
			return out, err
		}
		in, err := s.recreditWallet(ctx, r, t)
		if err != nil {
			return out, err
		}
		if in != nil {
			out.WalletTransactions = append(out.WalletTransactions, in)
		}
	}
	return out, nil
}

// recreditWallet returns the credits of the outbound transaction t to its
// wallet as a settled granted transaction, resetting the consumed credits.
// It returns nil when the wallet is no longer active or the credits are
// worth less than a minor unit.
func (s *Service) recreditWallet(ctx context.Context, r port.Repos, t *wallet.Transaction) (*wallet.Transaction, error) {
	org := t.OrganizationID
	w, err := r.Wallets.Get(ctx, org, t.WalletID)
	if err != nil {
		return nil, fmt.Errorf("get wallet %s: %w", t.WalletID, err)
	}
	if !w.Active() || wallet.RoundsToZero(t.CreditAmount, w.Currency, w.RateAmount) {
		return nil, nil
	}
	credit, err := wallet.NewCredit(t.CreditAmount, w.Currency, w.RateAmount, false)
	if err != nil {
		return nil, err
	}
	now := s.d.Now()
	in := &wallet.Transaction{
		ID: core.NewID[core.WalletTransactionID](s.d.IDs), OrganizationID: org, WalletID: w.ID,
		TransactionType: wallet.TransactionTypeInbound, TransactionStatus: wallet.TransactionStatusGranted,
		Status: wallet.SettlementStatusSettled, Source: wallet.TransactionSourceManual, Currency: w.Currency,
		Amount: credit.Amount, CreditAmount: credit.Credits, Priority: w.Priority,
		SettledAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if w.Traceable {
		rem := credit.Amount
		in.RemainingAmount = &rem
	}
	if err := r.WalletTransactions.Create(ctx, in); err != nil {
		return nil, fmt.Errorf("create wallet transaction: %w", err)
	}
	if err := kernel.IncreaseWalletBalance(ctx, s.d, r, w, in, true); err != nil {
		return nil, err
	}
	if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeWalletTransactionCreated, "wallet_transaction", kernel.ObjectID(in.ID), in); err != nil {
		return nil, err
	}
	return in, nil
}

// ResetDraft forgets the deductions of a draft before it is refreshed
// (lago Invoices::RefreshDraftService): a draft holds only progressive
// billing credits, which are deleted, and its deduction amounts are
// zeroed.
func (s *Service) ResetDraft(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
	if inv.Status != invoice.StatusDraft && inv.Status != invoice.StatusGenerating {
		return fmt.Errorf("reset invoice %s: %w", inv.ID, &core.TransitionError{Entity: "invoice", From: string(inv.Status), To: string(invoice.StatusDraft)})
	}
	if err := r.Credits.DeleteByInvoice(ctx, inv.OrganizationID, inv.ID); err != nil {
		return fmt.Errorf("delete credits of invoice %s: %w", inv.ID, err)
	}
	inv.ProgressiveBillingCreditAmount = 0
	inv.CouponsAmount = 0
	inv.CreditNotesAmount = 0
	inv.PrepaidCreditAmount = 0
	inv.PrepaidGrantedCreditAmount = 0
	inv.PrepaidPurchasedCreditAmount = 0
	return nil
}
