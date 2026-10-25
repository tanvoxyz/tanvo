package wallets

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// TransactionInput describes the movements of one top-up or void (lago
// WalletTransactions::CreateFromParamsService): paid credits are bought
// through a credit invoice, granted credits settle at once, voided credits
// are removed.
type TransactionInput struct {
	// WalletID is the wallet.
	WalletID core.WalletID
	// PaidCredits are bought; floored to 5 places, zero is skipped.
	PaidCredits *money.Decimal
	// GrantedCredits are granted; floored to 5 places, zero is skipped.
	GrantedCredits *money.Decimal
	// VoidedCredits are removed.
	VoidedCredits *money.Decimal
	// VoidedTransactionID selects the inbound transaction to void from
	// (traceable wallets); without VoidedCredits its whole remaining
	// amount is voided.
	VoidedTransactionID core.WalletTransactionID
	// Source defaults to manual.
	Source wallet.TransactionSource
	// Priority defaults to wallet.MaxPriority (create_from_params_service.rb:28).
	Priority *int
	// Name names the transactions; blank is no name.
	Name *string
	// Metadata is copied to the transactions.
	Metadata core.Metadata
	// InvoiceRequiresSuccessfulPayment defaults to the wallet's.
	InvoiceRequiresSuccessfulPayment *bool
	// IgnorePaidTopUpLimits lets the purchase ignore the wallet's limits.
	IgnorePaidTopUpLimits bool
	// ResetConsumedCredits lowers the consumed credits by the granted ones.
	ResetConsumedCredits bool
	// PaymentMethod collects the purchase.
	PaymentMethod *PaymentMethod
	// RecurringTransactionRuleID is the rule a top-up runs for.
	RecurringTransactionRuleID core.RecurringTransactionRuleID
	// CreditNoteID is the credit note a void refunds.
	CreditNoteID core.CreditNoteID
	// PurchaseOrderNumber is stored on the transactions (blank is none);
	// their credit invoice falls back to the wallet's.
	PurchaseOrderNumber *string
	// InvoiceCustomSections, when not nil, selects the custom sections of
	// the transactions (create_from_params_service.rb:59-63).
	InvoiceCustomSections *CustomSections
	// VoidedInvoiceID is the voided invoice a recredit's granted
	// transaction gives credits back for (recredit_service.rb:31).
	VoidedInvoiceID core.InvoiceID

	// rule is the rule a top-up runs for, whose custom sections the
	// transactions take (create_interval_wallet_transactions_service.rb:27,
	// threshold_top_up_service.rb:46).
	rule *wallet.RecurringTransactionRule
}

// CreateTransactions validates in and records its transactions under the
// customer's prepaid credits lock; it emits wallet_transaction.created for
// each of them.
func (s *Service) CreateTransactions(ctx context.Context, org core.OrganizationID, in TransactionInput) ([]*wallet.Transaction, error) {
	var out []*wallet.Transaction
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		ts, err := s.createTransactions(ctx, r, org, &in)
		out = ts
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) createTransactions(ctx context.Context, r port.Repos, org core.OrganizationID, in *TransactionInput) ([]*wallet.Transaction, error) {
	// Validate under the lock, so that the checks (active wallet, balance,
	// remaining amount) hold when the transactions are written (lago
	// re-runs its validation on a stale-object retry).
	if w, err := r.Wallets.Get(ctx, org, in.WalletID); err == nil {
		if err := lockCustomerCredits(ctx, r, org, w.CustomerID); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, core.ErrNotFound) {
		return nil, fmt.Errorf("load wallet %s: %w", in.WalletID, err)
	}
	w, inbound, err := s.validateTransactions(ctx, r, org, in)
	if err != nil {
		return nil, err
	}
	p := s.transactionParams(w, in)
	if p.InvoiceCustomSections, err = s.transactionSections(ctx, r, org, in); err != nil {
		return nil, err
	}
	// The billing entity snapshot (create_service.rb:64-66).
	if p.BillingEntityID, err = kernel.WalletBillingEntity(ctx, r, w); err != nil {
		return nil, err
	}
	var out []*wallet.Transaction
	if in.PaidCredits != nil {
		t, err := s.purchase(ctx, r, w, wallet.NormalizeCredits(*in.PaidCredits), in, p)
		if err != nil {
			return nil, err
		}
		out = appendNonNil(out, t)
	}
	if in.GrantedCredits != nil {
		g := *p
		g.VoidedInvoiceID = in.VoidedInvoiceID
		t, err := s.grant(ctx, r, w, wallet.NormalizeCredits(*in.GrantedCredits), in.ResetConsumedCredits, &g)
		if err != nil {
			return nil, err
		}
		out = appendNonNil(out, t)
	}
	if in.VoidedCredits != nil || !in.VoidedTransactionID.IsZero() {
		var credit *wallet.Credit
		if in.VoidedCredits != nil {
			c, err := wallet.NewCredit(*in.VoidedCredits, w.Currency, w.RateAmount, false)
			if err != nil {
				return nil, fmt.Errorf("voided credits: %w", err)
			}
			credit = &c
		}
		t, err := s.void(ctx, r, w, credit, inbound, p)
		if err != nil {
			return nil, err
		}
		out = appendNonNil(out, t)
	}
	for _, t := range out {
		if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeWalletTransactionCreated, "wallet_transaction", kernel.ObjectID(t.ID), t); err != nil {
			return nil, err
		}
		// source: app/services/wallet_transactions/create_from_params_service.rb:71
		if err := s.d.LogWalletTransaction(ctx, r, auditlog.ActivityWalletTransactionCreated, nil, t, w); err != nil {
			return nil, err
		}
	}
	// Purchases are invoiced once their creation is recorded; the invoice
	// may settle them.
	for _, t := range out {
		if t.TransactionStatus == wallet.TransactionStatusPurchased {
			if err := s.billPaidCredit(ctx, r, w, t); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// transactionSections returns the custom section selection of the
// transactions of in: the rule's for a rule top-up, then the request's
// applied to it.
func (s *Service) transactionSections(ctx context.Context, r port.Repos, org core.OrganizationID, in *TransactionInput) (wallet.CustomSections, error) {
	var cs wallet.CustomSections
	if in.rule != nil {
		var err error
		if cs, err = ruleSections(ctx, r, in.rule); err != nil {
			return wallet.CustomSections{}, err
		}
	}
	return attachSections(ctx, r, org, cs, in.InvoiceCustomSections)
}

func appendNonNil(ts []*wallet.Transaction, t *wallet.Transaction) []*wallet.Transaction {
	if t == nil {
		return ts
	}
	return append(ts, t)
}

// validateTransactions checks in (lago WalletTransactions::ValidateService)
// and returns the wallet and the inbound transaction to void from.
func (s *Service) validateTransactions(ctx context.Context, r port.Repos, org core.OrganizationID, in *TransactionInput) (*wallet.Wallet, *wallet.Transaction, error) {
	var v core.Validator
	w, err := r.Wallets.Get(ctx, org, in.WalletID)
	switch {
	case errors.Is(err, core.ErrNotFound):
		v.Add("wallet_id", CodeWalletNotFound)
		w = nil
	case err != nil:
		return nil, nil, fmt.Errorf("load wallet %s: %w", in.WalletID, err)
	case !w.Active():
		v.Add("wallet_id", CodeWalletIsTerminated)
	}
	for _, c := range []struct {
		field, code string
		credits     *money.Decimal
	}{
		{"paid_credits", CodeInvalidPaidCredits, in.PaidCredits},
		{"granted_credits", CodeInvalidGrantedCredits, in.GrantedCredits},
	} {
		if checkCredits(&v, c.field, c.code, c.credits) && c.credits != nil && w != nil {
			v.Check(!wallet.RoundsToZero(*c.credits, w.Currency, w.RateAmount), c.field, wallet.CodeRoundsToZero)
		}
	}
	if in.VoidedCredits != nil && w != nil {
		if checkCredits(&v, "voided_credits", CodeInvalidVoidedCredits, in.VoidedCredits) {
			v.Check(in.VoidedCredits.Cmp(w.CreditsBalance) <= 0, "voided_credits", CodeInsufficientCredits)
		}
	}
	var inbound *wallet.Transaction
	if !in.VoidedTransactionID.IsZero() && w != nil {
		inbound, err = voidedInbound(ctx, r, w, in.VoidedTransactionID, &v)
		if err != nil {
			return nil, nil, err
		}
	}
	if in.Metadata != nil {
		v.Merge("metadata", in.Metadata.Validate(core.WalletTransactionMetadataLimits()))
	}
	if in.Name != nil {
		v.MaxLen("name", *in.Name, 255)
	}
	v.OptionalString("purchase_order_number", in.PurchaseOrderNumber, core.MaxPurchaseOrderNumberLength)
	if w != nil {
		if err := validatePaymentMethod(ctx, r, org, w.CustomerID, &v, in.PaymentMethod); err != nil {
			return nil, nil, err
		}
	}
	if err := v.Err(); err != nil {
		return nil, nil, err
	}
	return w, inbound, nil
}

func voidedInbound(ctx context.Context, r port.Repos, w *wallet.Wallet, id core.WalletTransactionID, v *core.Validator) (*wallet.Transaction, error) {
	if !w.Traceable {
		v.Add("voided_transaction_id", CodeWalletNotTraceable)
		return nil, nil
	}
	t, err := r.WalletTransactions.Get(ctx, w.OrganizationID, id)
	if errors.Is(err, core.ErrNotFound) || err == nil && (t.WalletID != w.ID || !t.Inbound()) {
		v.Add("voided_transaction_id", CodeWalletTransactionNotFound)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load wallet transaction %s: %w", id, err)
	}
	if t.RemainingAmount == nil || *t.RemainingAmount <= 0 {
		v.Add("voided_transaction_id", CodeNoRemainingAmount)
		return nil, nil
	}
	return t, nil
}

// transactionParams returns the attributes shared by the transactions of
// one input.
func (s *Service) transactionParams(w *wallet.Wallet, in *TransactionInput) *wallet.Transaction {
	now := s.d.Now()
	t := &wallet.Transaction{
		OrganizationID:                   w.OrganizationID,
		WalletID:                         w.ID,
		Source:                           in.Source,
		Currency:                         w.Currency,
		Priority:                         wallet.MaxPriority,
		Name:                             blankToNil(in.Name),
		Metadata:                         in.Metadata,
		RecurringTransactionRuleID:       in.RecurringTransactionRuleID,
		CreditNoteID:                     in.CreditNoteID,
		PurchaseOrderNumber:              core.NormalizePurchaseOrderNumber(in.PurchaseOrderNumber),
		InvoiceRequiresSuccessfulPayment: w.InvoiceRequiresSuccessfulPayment,
		PaymentMethodType:                w.PaymentMethodType,
		PaymentMethodID:                  w.PaymentMethodID,
		CreatedAt:                        now,
		UpdatedAt:                        now,
	}
	if t.Source == "" {
		t.Source = wallet.TransactionSourceManual
	}
	if in.Priority != nil {
		t.Priority = *in.Priority
	}
	if in.InvoiceRequiresSuccessfulPayment != nil {
		t.InvoiceRequiresSuccessfulPayment = *in.InvoiceRequiresSuccessfulPayment
	}
	if in.PaymentMethod != nil {
		if in.PaymentMethod.Type != "" {
			t.PaymentMethodType = in.PaymentMethod.Type
		}
		t.PaymentMethodID = in.PaymentMethod.ID
	}
	return t
}

// newTransaction returns a copy of the shared attributes p for one
// movement.
func (s *Service) newTransaction(p *wallet.Transaction, typ wallet.TransactionType, status wallet.TransactionStatus, c wallet.Credit) *wallet.Transaction {
	t := *p
	t.InvoiceCustomSections.IDs = slices.Clone(p.InvoiceCustomSections.IDs)
	t.ID = core.NewID[core.WalletTransactionID](s.d.IDs)
	t.TransactionType = typ
	t.TransactionStatus = status
	t.Status = wallet.InitialStatus(status)
	t.Amount, t.CreditAmount = c.Amount, c.Credits
	if t.Status == wallet.SettlementStatusSettled {
		now := s.d.Now()
		t.SettledAt = &now
	}
	return &t
}

func (s *Service) createTransaction(ctx context.Context, r port.Repos, t *wallet.Transaction) error {
	if err := t.Validate(); err != nil {
		return fmt.Errorf("wallet transaction: %w", err)
	}
	if err := r.WalletTransactions.Create(ctx, t); err != nil {
		return fmt.Errorf("create wallet transaction: %w", err)
	}
	return nil
}

// purchase records a pending purchase of credits; its credit invoice is
// built by the caller.
func (s *Service) purchase(ctx context.Context, r port.Repos, w *wallet.Wallet, credits money.Decimal, in *TransactionInput, p *wallet.Transaction) (*wallet.Transaction, error) {
	if credits.IsZero() {
		return nil, nil
	}
	if err := w.ValidatePaidTopUp("paid_credits", credits, in.IgnorePaidTopUpLimits); err != nil {
		return nil, err
	}
	c, err := wallet.NewCredit(credits, w.Currency, w.RateAmount, true)
	if err != nil {
		return nil, fmt.Errorf("paid credits: %w", err)
	}
	t := s.newTransaction(p, wallet.TransactionTypeInbound, wallet.TransactionStatusPurchased, c)
	if err := s.createTransaction(ctx, r, t); err != nil {
		return nil, err
	}
	return t, nil
}

// grant records settled granted credits and adds them to the balance.
func (s *Service) grant(ctx context.Context, r port.Repos, w *wallet.Wallet, credits money.Decimal, resetConsumed bool, p *wallet.Transaction) (*wallet.Transaction, error) {
	if credits.IsZero() {
		return nil, nil
	}
	// Granted credits are valued like paid ones: whole minor units only
	// (lago WalletCredit invoiceable).
	c, err := wallet.NewCredit(credits, w.Currency, w.RateAmount, true)
	if err != nil {
		return nil, fmt.Errorf("granted credits: %w", err)
	}
	t := s.newTransaction(p, wallet.TransactionTypeInbound, wallet.TransactionStatusGranted, c)
	if w.Traceable {
		remaining := t.Amount
		t.RemainingAmount = &remaining
	}
	if err := s.createTransaction(ctx, r, t); err != nil {
		return nil, err
	}
	if err := kernel.IncreaseWalletBalance(ctx, s.d, r, w, t, resetConsumed); err != nil {
		return nil, err
	}
	return t, nil
}

// void removes credit (or, when nil, the whole remaining amount of inbound)
// from the wallet (lago WalletTransactions::VoidService).
func (s *Service) void(ctx context.Context, r port.Repos, w *wallet.Wallet, credit *wallet.Credit, inbound *wallet.Transaction, p *wallet.Transaction) (*wallet.Transaction, error) {
	if credit == nil {
		if inbound == nil {
			return nil, nil
		}
		in, err := r.WalletTransactions.Get(ctx, w.OrganizationID, inbound.ID)
		if err != nil {
			return nil, fmt.Errorf("load wallet transaction %s: %w", inbound.ID, err)
		}
		c, err := wallet.CreditFromMinor(kernel.Cents(*in.RemainingAmount), w.Currency, w.RateAmount)
		if err != nil {
			return nil, fmt.Errorf("voided credits: %w", err)
		}
		credit = &c
	} else if credit.Credits.IsZero() {
		return nil, nil
	} else if w.Traceable && inbound != nil && credit.Amount > *inbound.RemainingAmount {
		return nil, fieldError("amount", kernel.CodeExceedsRemainingTransactionAmount)
	}
	if credit.Credits.IsZero() {
		return nil, nil
	}
	t := s.newTransaction(p, wallet.TransactionTypeOutbound, wallet.TransactionStatusVoided, *credit)
	t.RecurringTransactionRuleID = core.RecurringTransactionRuleID{}
	// A void takes the entity of the inbound transaction it targets
	// (void_service.rb:41).
	if inbound != nil && !inbound.BillingEntityID.IsZero() {
		t.BillingEntityID = inbound.BillingEntityID
	}
	if err := s.createTransaction(ctx, r, t); err != nil {
		return nil, err
	}
	if w.Traceable {
		var id core.WalletTransactionID
		if inbound != nil {
			id = inbound.ID
		}
		if err := kernel.TrackWalletConsumption(ctx, s.d, r, t, id); err != nil {
			return nil, err
		}
	}
	if err := kernel.DecreaseWalletBalance(ctx, s.d, r, w, t); err != nil {
		return nil, err
	}
	return t, nil
}

// Settle settles a pending transaction (lago WalletTransactions::
// SettleService): a traceable wallet's inbound transaction becomes
// consumable. It emits wallet_transaction.updated; the balance is not
// changed (see [Service.ApplyPaidCredits]).
func (s *Service) Settle(ctx context.Context, org core.OrganizationID, id core.WalletTransactionID) (*wallet.Transaction, error) {
	var out *wallet.Transaction
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		t, err := r.WalletTransactions.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load wallet transaction %s: %w", id, err)
		}
		w, err := lockTransaction(ctx, r, t)
		if err != nil {
			return err
		}
		out = t
		return s.settle(ctx, r, w, t)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) settle(ctx context.Context, r port.Repos, w *wallet.Wallet, t *wallet.Transaction) error {
	now := s.d.Now()
	before, err := auditlog.ObjectOf(t)
	if err != nil {
		return err
	}
	if t.Status == wallet.SettlementStatusFailed {
		// A failed purchase whose credit invoice is paid later (a retried
		// payment) still delivers its credits, as lago's SettleService does
		// (it has no status guard). core forbids failed → settled, so the
		// fields are set here; FailedAt is kept as history.
		t.Status, t.SettledAt, t.UpdatedAt = wallet.SettlementStatusSettled, &now, now
	} else if err := t.Settle(now); err != nil {
		return fmt.Errorf("settle wallet transaction %s: %w", t.ID, err)
	}
	if t.Inbound() && w.Traceable {
		remaining := t.Amount
		t.RemainingAmount = &remaining
	}
	if err := r.WalletTransactions.Update(ctx, t); err != nil {
		return fmt.Errorf("update wallet transaction %s: %w", t.ID, err)
	}
	if err := kernel.Emit(ctx, s.d, t.OrganizationID, webhook.EventTypeWalletTransactionUpdated, "wallet_transaction", kernel.ObjectID(t.ID), t); err != nil {
		return err
	}
	// source: app/services/wallet_transactions/settle_service.rb:13
	return s.d.LogWalletTransaction(ctx, r, auditlog.ActivityWalletTransactionUpdated, before, t, w)
}

// ApplyPaidCredits settles a pending purchase and adds its credits to the
// balance (lago Wallets::ApplyPaidCreditsService); a settled purchase is
// left as is.
func (s *Service) ApplyPaidCredits(ctx context.Context, org core.OrganizationID, id core.WalletTransactionID) (*wallet.Transaction, error) {
	var out *wallet.Transaction
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		t, err := r.WalletTransactions.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load wallet transaction %s: %w", id, err)
		}
		out = t
		return s.applyPaidCredits(ctx, r, t)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// applyPaidCredits settles t and credits its wallet once: t is reloaded
// under the lock, so a concurrent or repeated payment success finds it
// settled (lago relies on lock_version for this).
func (s *Service) applyPaidCredits(ctx context.Context, r port.Repos, t *wallet.Transaction) error {
	w, err := lockTransaction(ctx, r, t)
	if err != nil {
		return err
	}
	if t.Status == wallet.SettlementStatusSettled {
		return nil
	}
	if err := s.settle(ctx, r, w, t); err != nil {
		return err
	}
	return kernel.IncreaseWalletBalance(ctx, s.d, r, w, t, false)
}

// MarkAsFailed fails a pending transaction (lago WalletTransactions::
// MarkAsFailedService) and emits wallet_transaction.updated; failed and
// settled transactions are left as they are.
func (s *Service) MarkAsFailed(ctx context.Context, org core.OrganizationID, id core.WalletTransactionID) (*wallet.Transaction, error) {
	var out *wallet.Transaction
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		t, err := r.WalletTransactions.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load wallet transaction %s: %w", id, err)
		}
		out = t
		return s.markAsFailed(ctx, r, t, true)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// markAsFailed fails t when pending. Called as lago's
// WalletTransactions::MarkAsFailedService (always), it records
// wallet_transaction.updated even when t is left unchanged, as the
// service's activity_loggable does on its successful no-op
// (mark_as_failed_service.rb:12,18-20); Tanvo's own callers record it only
// for a change.
func (s *Service) markAsFailed(ctx context.Context, r port.Repos, t *wallet.Transaction, always bool) error {
	w, err := lockTransaction(ctx, r, t)
	if err != nil {
		return err
	}
	before, err := auditlog.ObjectOf(t)
	if err != nil {
		return err
	}
	if t.Status != wallet.SettlementStatusPending {
		if always {
			return s.d.LogWalletTransaction(ctx, r, auditlog.ActivityWalletTransactionUpdated, before, t, w)
		}
		return nil
	}
	if err := t.Fail(s.d.Now()); err != nil {
		return fmt.Errorf("fail wallet transaction %s: %w", t.ID, err)
	}
	if err := r.WalletTransactions.Update(ctx, t); err != nil {
		return fmt.Errorf("update wallet transaction %s: %w", t.ID, err)
	}
	if err := kernel.Emit(ctx, s.d, t.OrganizationID, webhook.EventTypeWalletTransactionUpdated, "wallet_transaction", kernel.ObjectID(t.ID), t); err != nil {
		return err
	}
	// source: app/services/wallet_transactions/mark_as_failed_service.rb:12
	return s.d.LogWalletTransaction(ctx, r, auditlog.ActivityWalletTransactionUpdated, before, t, w)
}

// Recredit grants back the credits of a transaction whose invoice was
// voided (lago WalletTransactions::RecreditService), resetting the consumed
// credits; credits worth less than a minor unit are skipped. The wallet
// must be active.
func (s *Service) Recredit(ctx context.Context, org core.OrganizationID, id core.WalletTransactionID) (*wallet.Transaction, error) {
	var out *wallet.Transaction
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		t, err := r.WalletTransactions.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load wallet transaction %s: %w", id, err)
		}
		w, err := lockTransaction(ctx, r, t)
		if err != nil {
			return err
		}
		if !w.Active() {
			return core.NewRuleError(CodeWalletNotActive, "the wallet is terminated")
		}
		if wallet.RoundsToZero(t.CreditAmount, w.Currency, w.RateAmount) {
			return nil
		}
		credits := t.CreditAmount
		ts, err := s.createTransactions(ctx, r, org, &TransactionInput{
			WalletID: w.ID, GrantedCredits: &credits, ResetConsumedCredits: true,
			PurchaseOrderNumber: t.PurchaseOrderNumber, VoidedInvoiceID: t.InvoiceID,
		})
		if err != nil {
			return err
		}
		if len(ts) > 0 {
			out = ts[0]
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
