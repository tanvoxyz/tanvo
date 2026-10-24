package wallets

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

// VoidCreditNoteCredits voids, in the transaction of r, the wallet credits
// bought by a credit invoice that a credit note refunds (lago
// CreditNotes::CreateService on credit invoices): the refunded amount is
// converted back to credits and voided, from the purchase transaction when
// the wallet is traceable. An offset settles the whole credit invoice
// without a payment, so its pending purchase fails instead of granting
// credits (lago Invoices::PrepaidCreditJob paid_by_credit_note?). It
// implements creditnotes.WalletVoider.
func (s *Service) VoidCreditNoteCredits(ctx context.Context, r port.Repos, w *wallet.Wallet, purchase *wallet.Transaction, cn *creditnote.CreditNote) error {
	amount := cn.RefundAmount
	if amount <= 0 && (cn.OffsetAmount <= 0 || purchase == nil) {
		return nil
	}
	// The wallet and the purchase are reread under the lock (lago
	// VoidService locks prepaid_credit and reloads the wallet).
	w, err := lockWallet(ctx, r, w.OrganizationID, w.ID)
	if err != nil {
		return err
	}
	if purchase != nil {
		if purchase, err = r.WalletTransactions.Get(ctx, w.OrganizationID, purchase.ID); err != nil {
			return fmt.Errorf("load wallet transaction: %w", err)
		}
	}
	if cn.OffsetAmount > 0 && purchase != nil {
		if err := s.markAsFailed(ctx, r, purchase, false); err != nil {
			return err
		}
	}
	if amount <= 0 {
		return nil
	}
	c, err := wallet.CreditFromMinor(kernel.Cents(amount), w.Currency, w.RateAmount)
	if err != nil {
		return fmt.Errorf("voided credits of credit note %s: %w", cn.ID, err)
	}
	p := s.transactionParams(w, &TransactionInput{CreditNoteID: cn.ID})
	var inbound *wallet.Transaction
	if w.Traceable {
		inbound = purchase
	}
	if _, err := s.void(ctx, r, w, &c, inbound, p); err != nil {
		return fmt.Errorf("void credits of credit note %s: %w", cn.ID, err)
	}
	return nil
}
