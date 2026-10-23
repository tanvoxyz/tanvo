package creditnotes

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

// WalletVoider voids the wallet credits a credit note refunds or offsets
// on a credit invoice (domain §14.2); libs/billing/wallets implements it.
type WalletVoider interface {
	// VoidCreditNoteCredits voids in w the credits of cn's refunded amount,
	// from the purchase transaction when the wallet is traceable, and fails
	// the purchase while pending when cn offsets the invoice. It runs in
	// the transaction of r.
	VoidCreditNoteCredits(ctx context.Context, r port.Repos, w *wallet.Wallet, purchase *wallet.Transaction, cn *creditnote.CreditNote) error
}

// Service is the creditnotes service.
type Service struct {
	d       *kernel.Deps
	wallets WalletVoider
}

// New returns the service over the checked dependencies d.
func New(d *kernel.Deps) *Service { return &Service{d: d} }

// SetWalletVoider sets the voider of the wallet credits of credit
// invoices; without one, a credit note on a credit invoice leaves the
// wallet unchanged.
func (s *Service) SetWalletVoider(v WalletVoider) { s.wallets = v }
