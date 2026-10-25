package kernel

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

// WalletBillingEntity returns the billing entity a new transaction of w
// snapshots: the wallet's, else its customer's (lago Wallet#billing_entity,
// app/models/wallet.rb:74-76, read by
// WalletTransactions::CreateService#billing_entity_id_for_snapshot).
func WalletBillingEntity(ctx context.Context, r port.Repos, w *wallet.Wallet) (core.BillingEntityID, error) {
	if !w.BillingEntityID.IsZero() {
		return w.BillingEntityID, nil
	}
	c, err := r.Customers.Get(ctx, w.OrganizationID, w.CustomerID)
	if err != nil {
		return core.BillingEntityID{}, fmt.Errorf("load customer %s: %w", w.CustomerID, err)
	}
	return c.BillingEntityID, nil
}
