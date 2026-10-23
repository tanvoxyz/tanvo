package kernel

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Wallet balance error codes, reported on the field "amount".
const (
	// CodeExceedsRemainingTransactionAmount rejects consuming more than an
	// inbound transaction has left.
	CodeExceedsRemainingTransactionAmount = "exceeds_remaining_transaction_amount"
	// CodeExceedsAvailableAmount rejects consuming more than a traceable
	// wallet's inbound transactions have left.
	CodeExceedsAvailableAmount = "exceeds_available_amount"
)

// IncreaseWalletBalance adds a settled inbound transaction t to wallet w
// and stores it (domain §14.2). With resetConsumed, the lifetime consumed
// credits and amount go down by the credits, never below zero. It emits
// wallet.updated and enqueues the refresh of the ongoing balances and the
// wallet's alerts.
func IncreaseWalletBalance(ctx context.Context, d *Deps, r port.Repos, w *wallet.Wallet, t *wallet.Transaction, resetConsumed bool) error {
	now := d.Now()
	w.BalanceAmount += t.Amount
	w.CreditsBalance = w.CreditsBalance.Add(t.CreditAmount)
	w.LastBalanceSyncAt = &now
	if resetConsumed {
		left := w.ConsumedCredits.Sub(t.CreditAmount)
		w.ConsumedCredits = money.Max(money.Zero, left)
		amount, err := w.Currency.ToMinor(left.Mul(w.RateAmount), money.Floor)
		if err != nil {
			return fmt.Errorf("reset consumed credits of wallet %s: %w", w.ID, err)
		}
		w.ConsumedAmount = max(0, amount)
	}
	return saveWalletBalance(ctx, d, r, w, now)
}

// DecreaseWalletBalance removes a settled outbound transaction t from
// wallet w and stores it: the balance goes down and the lifetime consumed
// credits and amount go up. It emits wallet.updated and enqueues the
// refresh of the ongoing balances and the wallet's alerts.
func DecreaseWalletBalance(ctx context.Context, d *Deps, r port.Repos, w *wallet.Wallet, t *wallet.Transaction) error {
	now := d.Now()
	w.BalanceAmount -= t.Amount
	w.CreditsBalance = w.CreditsBalance.Sub(t.CreditAmount)
	w.ConsumedCredits = w.ConsumedCredits.Add(t.CreditAmount)
	w.ConsumedAmount += t.Amount
	w.LastBalanceSyncAt = &now
	w.LastConsumedCreditAt = &now
	return saveWalletBalance(ctx, d, r, w, now)
}

func saveWalletBalance(ctx context.Context, d *Deps, r port.Repos, w *wallet.Wallet, now time.Time) error {
	w.UpdatedAt = now
	if err := r.Wallets.Update(ctx, w); err != nil {
		return fmt.Errorf("update wallet %s: %w", w.ID, err)
	}
	if err := EnqueueWalletRefresh(ctx, r, w); err != nil {
		return err
	}
	if err := Emit(ctx, d, w.OrganizationID, webhook.EventTypeWalletUpdated, "wallet", ObjectID(w.ID), w); err != nil {
		return err
	}
	// lago app/services/wallets/balance/increase_service.rb:38 and
	// decrease_service.rb:34.
	return EnqueueWalletAlerts(ctx, r, w)
}

// EnqueueWalletRefresh flags the customer of wallet w as awaiting the
// refresh of its wallets' ongoing balances when it has an active wallet
// (lago Customer#flag_wallets_for_refresh, app/models/customer.rb:385-389)
// and enqueues that refresh, once per customer while one is pending. The
// flag keeps the refresh due when the job is deduplicated against a run in
// progress: the sweep (Engine.RefreshOngoingBalances) picks it up.
func EnqueueWalletRefresh(ctx context.Context, r port.Repos, w *wallet.Wallet) error {
	if err := r.Customers.FlagWalletRefresh(ctx, w.OrganizationID, w.CustomerID); err != nil {
		return fmt.Errorf("flag wallet refresh of customer %s: %w", w.CustomerID, err)
	}
	return EnqueueWalletRefreshJob(ctx, r, w)
}

// EnqueueWalletRefreshJob enqueues the refresh of the ongoing balances of
// the customer of wallet w without flagging it, once per customer while
// one is pending.
func EnqueueWalletRefreshJob(ctx context.Context, r port.Repos, w *wallet.Wallet) error {
	return Enqueue(ctx, r, w.OrganizationID, JobRefreshWalletOngoingBalance, WalletArgs{WalletID: w.ID}, w.CustomerID.String(), time.Time{})
}

// TrackWalletConsumption records which inbound transactions the outbound
// transaction out of a traceable wallet consumes (domain §14.2): the
// inbound transaction inbound when it is set, otherwise the available
// inbound transactions in consumption order. Each consumed inbound
// transaction's remaining amount goes down.
func TrackWalletConsumption(ctx context.Context, d *Deps, r port.Repos, out *wallet.Transaction, inbound core.WalletTransactionID) error {
	org := out.OrganizationID
	var sources []*wallet.Transaction
	if !inbound.IsZero() {
		in, err := r.WalletTransactions.Get(ctx, org, inbound)
		if err != nil {
			return fmt.Errorf("load inbound wallet transaction %s: %w", inbound, err)
		}
		if !in.Inbound() || in.WalletID != out.WalletID {
			return fmt.Errorf("load inbound wallet transaction %s: %w", inbound, core.ErrNotFound)
		}
		if in.RemainingAmount == nil || out.Amount > *in.RemainingAmount {
			return amountError(CodeExceedsRemainingTransactionAmount)
		}
		sources = []*wallet.Transaction{in}
	} else {
		ins, err := r.WalletTransactions.ListAvailableInbound(ctx, org, out.WalletID)
		if err != nil {
			return fmt.Errorf("list inbound transactions of wallet %s: %w", out.WalletID, err)
		}
		var available int64
		for _, in := range ins {
			available += *in.RemainingAmount
		}
		if out.Amount > available {
			return amountError(CodeExceedsAvailableAmount)
		}
		sources = ins
	}
	left := out.Amount
	now := d.Now()
	for _, in := range sources {
		if left <= 0 {
			break
		}
		take := min(*in.RemainingAmount, left)
		c := &wallet.Consumption{
			ID:                    core.NewID[core.WalletTransactionConsumptionID](d.IDs),
			OrganizationID:        org,
			InboundTransactionID:  in.ID,
			OutboundTransactionID: out.ID,
			ConsumedAmount:        take,
			CreatedAt:             now,
			UpdatedAt:             now,
		}
		if err := r.WalletTransactions.CreateConsumption(ctx, c); err != nil {
			return fmt.Errorf("record consumption of wallet transaction %s: %w", in.ID, err)
		}
		rem := *in.RemainingAmount - take
		in.RemainingAmount = &rem
		in.UpdatedAt = now
		if err := r.WalletTransactions.Update(ctx, in); err != nil {
			return fmt.Errorf("update wallet transaction %s: %w", in.ID, err)
		}
		left -= take
	}
	return nil
}

func amountError(code string) error {
	var v core.Validator
	v.Add("amount", code)
	return v.Err()
}
