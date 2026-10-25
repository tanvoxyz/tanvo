package wallets

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

// BillPaidCredit issues the credit invoice of the purchase t of w, for the
// tests of a purchase billed after its wallet changed.
func (s *Service) BillPaidCredit(ctx context.Context, w *wallet.Wallet, t *wallet.Transaction) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error { return s.billPaidCredit(ctx, r, w, t) })
}
