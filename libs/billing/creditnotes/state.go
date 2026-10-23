package creditnotes

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

// invoiceState is a credited invoice with everything the credit note rules
// read: its fees, taxes, credit notes and, for a credit invoice, the wallet
// it bought credits in.
type invoiceState struct {
	inv   *invoice.Invoice
	fees  []*invoice.Fee
	taxes []*invoice.AppliedTax
	// notes are the invoice's credit notes, the one being built excluded.
	notes []*creditnote.CreditNote
	// credited is Σ item amounts per fee of the invoice's credit notes,
	// voided ones included, plus the items of the note being built once
	// added.
	credited map[core.FeeID]int64
	// wallet is the active wallet a credit invoice bought credits in.
	wallet *wallet.Wallet
	// walletTx is the purchase transaction of a credit invoice.
	walletTx *wallet.Transaction
}

// loadInvoice loads the invoice id with its fees, taxes and credit notes.
func loadInvoice(ctx context.Context, r port.Repos, org core.OrganizationID, id core.InvoiceID) (*invoiceState, error) {
	inv, err := r.Invoices.Get(ctx, org, id)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return nil, notFound("invoice", id)
		}
		return nil, fmt.Errorf("load invoice %s: %w", id, err)
	}
	return loadInvoiceOf(ctx, r, inv)
}

// lockAndLoad takes the credit notes lock of the invoice's customer, unless
// preview, then loads the invoice id with its fees, taxes and credit notes,
// so that what the checks read cannot change before the note is stored.
func lockAndLoad(ctx context.Context, r port.Repos, org core.OrganizationID, id core.InvoiceID, preview bool) (*invoiceState, error) {
	if !preview {
		inv, err := r.Invoices.Get(ctx, org, id)
		if err != nil {
			if errors.Is(err, core.ErrNotFound) {
				return nil, notFound("invoice", id)
			}
			return nil, fmt.Errorf("load invoice %s: %w", id, err)
		}
		if err := kernel.LockCustomer(ctx, r, org, inv.CustomerID, customer.LockScopeCreditNotes); err != nil {
			return nil, err
		}
	}
	return loadInvoice(ctx, r, org, id)
}

// loadInvoiceOf loads the fees, taxes and credit notes of inv.
func loadInvoiceOf(ctx context.Context, r port.Repos, inv *invoice.Invoice) (*invoiceState, error) {
	org := inv.OrganizationID
	st := &invoiceState{inv: inv, credited: map[core.FeeID]int64{}}
	var err error
	if st.fees, err = r.Fees.ListByInvoice(ctx, org, inv.ID); err != nil {
		return nil, fmt.Errorf("load fees of invoice %s: %w", inv.ID, err)
	}
	if st.taxes, err = r.InvoiceAppliedTaxes.ListByInvoice(ctx, org, inv.ID); err != nil {
		return nil, fmt.Errorf("load taxes of invoice %s: %w", inv.ID, err)
	}
	if st.notes, err = r.CreditNotes.ListByInvoice(ctx, org, inv.ID); err != nil {
		return nil, fmt.Errorf("load credit notes of invoice %s: %w", inv.ID, err)
	}
	// Every item of the invoice's notes counts, voided ones included: a
	// voided note keeps what it credited (part of it may have been
	// consumed), so its fee amount is never credited a second time.
	for _, cn := range st.notes {
		for _, it := range cn.Items {
			st.credited[it.FeeID] += it.Amount
		}
	}
	if inv.Type == invoice.TypeCredit && len(st.fees) > 0 && !st.fees[0].WalletTransactionID.IsZero() {
		if err := st.loadWallet(ctx, r); err != nil {
			return nil, err
		}
	}
	return st, nil
}

// loadWallet loads the active wallet of a credit invoice's purchase.
func (st *invoiceState) loadWallet(ctx context.Context, r port.Repos) error {
	org := st.inv.OrganizationID
	tx, err := r.WalletTransactions.Get(ctx, org, st.fees[0].WalletTransactionID)
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load wallet transaction of invoice %s: %w", st.inv.ID, err)
	}
	w, err := r.Wallets.Get(ctx, org, tx.WalletID)
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load wallet of invoice %s: %w", st.inv.ID, err)
	}
	st.walletTx = tx
	if w.Status == wallet.StatusActive {
		st.wallet = w
	}
	return nil
}

// fee returns the invoice fee id and its index, or false.
func (st *invoiceState) fee(id core.FeeID) (*invoice.Fee, int, bool) {
	i := slices.IndexFunc(st.fees, func(f *invoice.Fee) bool { return f.ID == id })
	if i < 0 {
		return nil, -1, false
	}
	return st.fees[i], i, true
}

// creditableFromWallet returns what is left of a credit invoice's purchase
// in its wallet: the purchase's remaining amount for a traceable wallet,
// else the wallet balance; 0 without an active wallet.
func (st *invoiceState) creditableFromWallet() int64 {
	if st.wallet == nil {
		return 0
	}
	if st.wallet.Traceable {
		if st.walletTx == nil || st.walletTx.RemainingAmount == nil {
			return 0
		}
		return *st.walletTx.RemainingAmount
	}
	return st.wallet.BalanceAmount
}

// feeCreditable returns what credit notes may still credit on the fee: its
// amount less what was already credited, capped for a credit fee by what is
// left in the wallet.
func (st *invoiceState) feeCreditable(f *invoice.Fee) int64 {
	n := f.Amount - st.credited[f.ID]
	if f.Type == core.FeeTypeCredit {
		n = min(n, st.creditableFromWallet())
	}
	return n
}

// taxInvoice returns the libs/tax view of the invoice.
func (st *invoiceState) taxInvoice() tax.Invoice {
	out := tax.Invoice{TaxesAmountCents: st.inv.TaxesAmount}
	for _, t := range st.taxes {
		out.AppliedTaxes = append(out.AppliedTaxes, tax.InvoiceAppliedTax{
			TaxCode: t.TaxCode, TaxName: t.TaxName, TaxDescription: deref(t.TaxDescription), TaxRate: t.TaxRate,
			Provider: t.TaxID.IsZero(), AmountCents: t.Amount, FeesAmountCents: t.FeesAmount,
		})
	}
	for _, f := range st.fees {
		out.Fees = append(out.Fees, kernel.TaxFeeOf(f))
	}
	return out
}

// sums returns Σ credit, refund and offset amounts of the finalized credit
// notes.
func (st *invoiceState) sums() (credit, refund, offset int64) {
	for _, cn := range st.notes {
		if cn.Status != creditnote.StatusFinalized {
			continue
		}
		credit += cn.CreditAmount
		refund += cn.RefundAmount
		offset += cn.OffsetAmount
	}
	return credit, refund, offset
}

// notesTotal returns Σ totals of every credit note of the invoice.
func (st *invoiceState) notesTotal() int64 {
	var n int64
	for _, cn := range st.notes {
		n += cn.TotalAmount
	}
	return n
}

// feeTotal returns the invoice fees plus taxes.
func (st *invoiceState) feeTotal() int64 { return st.inv.FeesAmount + st.inv.TaxesAmount }

// creditable returns what credit notes may still credit on the invoice:
// the creditable part of each fee with its booked tax, within the sub total
// including taxes less the credit notes' totals. Drafts and credit invoices
// give 0.
func (st *invoiceState) creditable() int64 {
	if st.inv.Type == invoice.TypeCredit {
		return 0
	}
	return st.availableToCredit()
}

func (st *invoiceState) availableToCredit() int64 {
	if st.inv.Status == invoice.StatusDraft {
		return 0
	}
	booked := tax.BookTaxes(st.taxInvoice())
	sum := money.Zero
	for i, f := range st.fees {
		if f.Amount == 0 {
			continue
		}
		share := money.NewFromInt(st.feeCreditable(f)).Div(money.NewFromInt(f.Amount))
		sum = sum.Add(share.Mul(f.SubTotalExcludingTaxesPreciseAmount().Add(money.NewFromInt(booked[i].AmountCents))))
	}
	remaining := max(0, st.inv.SubTotalIncludingTaxesAmount-st.notesTotal())
	return min(kernel.RoundCents(sum), remaining)
}

// refundable returns what credit notes may still refund on the invoice.
func (st *invoiceState) refundable() int64 {
	inv := st.inv
	if inv.Status == invoice.StatusDraft {
		return 0
	}
	if inv.PaymentStatus != core.PaymentStatusSucceeded && inv.TotalPaidAmount == inv.TotalAmount {
		return 0
	}
	var refunded int64
	for _, cn := range st.notes {
		refunded += cn.RefundAmount
	}
	remaining := inv.TotalPaidAmount - refunded
	if inv.Type == invoice.TypeCredit {
		return min(st.creditableFromWallet(), remaining)
	}
	return max(0, min(remaining, st.creditable()))
}

// taxesRoundingAdjustment returns Σ taxes − precise taxes of the invoice's
// credit notes.
func (st *invoiceState) taxesRoundingAdjustment() money.Decimal {
	sum := money.Zero
	for _, cn := range st.notes {
		sum = sum.Add(money.NewFromInt(cn.TaxesAmount).Sub(cn.PreciseTaxesAmount))
	}
	return sum
}

// appliedTaxID returns the tax of the invoice applied tax with code and
// rate.
func (st *invoiceState) appliedTaxID(code string, rate money.Decimal) core.TaxID {
	for _, t := range st.taxes {
		if t.TaxCode == code && t.TaxRate.Equal(rate) {
			return t.TaxID
		}
	}
	return core.TaxID{}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
