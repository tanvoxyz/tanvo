package wallets

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

// The repositories store whole rows without a version check, so every
// read-modify-write of a wallet or a wallet transaction holds the
// customer's prepaid_credits lock and reads the row under it (domain
// §14.1). This stands in for lago's optimistic locking (lock_version on
// wallets and wallet_transactions, with StaleObjectError retries): a write
// never overwrites a concurrent balance change, and a transaction settles
// or fails once.

// lockCustomerCredits takes the prepaid_credits lock of customer c.
func lockCustomerCredits(ctx context.Context, r port.Repos, org core.OrganizationID, c core.CustomerID) error {
	return kernel.LockCustomer(ctx, r, org, c, customer.LockScopePrepaidCredits)
}

// lockWallet takes the prepaid_credits lock of the customer of wallet id
// and returns the wallet read under it.
func lockWallet(ctx context.Context, r port.Repos, org core.OrganizationID, id core.WalletID) (*wallet.Wallet, error) {
	w, err := r.Wallets.Get(ctx, org, id)
	if err != nil {
		return nil, fmt.Errorf("load wallet %s: %w", id, err)
	}
	if err := lockCustomerCredits(ctx, r, org, w.CustomerID); err != nil {
		return nil, err
	}
	if w, err = r.Wallets.Get(ctx, org, id); err != nil {
		return nil, fmt.Errorf("load wallet %s: %w", id, err)
	}
	return w, nil
}

// lockTransaction takes the prepaid_credits lock of the customer of the
// wallet of t, then reloads t in place and returns its wallet, both read
// under the lock.
func lockTransaction(ctx context.Context, r port.Repos, t *wallet.Transaction) (*wallet.Wallet, error) {
	w, err := lockWallet(ctx, r, t.OrganizationID, t.WalletID)
	if err != nil {
		return nil, err
	}
	fresh, err := r.WalletTransactions.Get(ctx, t.OrganizationID, t.ID)
	if err != nil {
		return nil, fmt.Errorf("load wallet transaction %s: %w", t.ID, err)
	}
	*t = *fresh
	return w, nil
}
