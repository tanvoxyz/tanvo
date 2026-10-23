package billingtest

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// StoredInvoice creates a USD subscription invoice of the customer in
// status generating, without fees; options run before it is stored.
func (h *Harness) StoredInvoice(c *customer.Customer, opts ...func(*invoice.Invoice)) *invoice.Invoice {
	h.T.Helper()
	id := core.NewID[core.InvoiceID](h.IDs)
	inv := &invoice.Invoice{
		ID: id, OrganizationID: h.OrgID(), CustomerID: c.ID, BillingEntityID: c.BillingEntityID,
		Type: invoice.TypeSubscription, Status: invoice.StatusGenerating, PaymentStatus: core.PaymentStatusPending,
		TaxStatus: invoice.TaxStatusSucceeded, Number: "INV-" + id.String()[24:], Currency: USD, TaxesRate: money.Zero,
		IssuingDate: core.DateOf(h.Now(), time.UTC), CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	apply(inv, opts)
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Invoices.Create(ctx, inv) })
	return inv
}

// SaveInvoice stores the changes of an invoice.
func (h *Harness) SaveInvoice(inv *invoice.Invoice) {
	h.T.Helper()
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Invoices.Update(ctx, inv) })
}

// StoredFee creates a fee of amount (minor units) on the invoice; it is a
// subscription fee unless options say otherwise.
func (h *Harness) StoredFee(inv *invoice.Invoice, amount int64, opts ...func(*invoice.Fee)) *invoice.Fee {
	h.T.Helper()
	f := &invoice.Fee{
		ID: core.NewID[core.FeeID](h.IDs), OrganizationID: h.OrgID(), InvoiceID: inv.ID, Type: core.FeeTypeSubscription,
		Amount: amount, PreciseAmount: money.NewFromInt(amount), Units: money.One, Currency: inv.Currency,
		TaxesPreciseAmount: money.Zero, TaxesRate: money.Zero, TaxesBaseRate: money.One,
		PreciseCouponsAmount: money.Zero, PreciseCreditNotesAmount: money.Zero,
		Invoiceable: true, CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	apply(f, opts)
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Fees.Create(ctx, f) })
	return f
}

// SaveFees stores the changes of fees.
func (h *Harness) SaveFees(fees ...*invoice.Fee) {
	h.T.Helper()
	h.Do(func(ctx context.Context, r port.Repos) error {
		for _, f := range fees {
			if err := r.Fees.Update(ctx, f); err != nil {
				return err
			}
		}
		return nil
	})
}

// ChargeFee is a fee option making it a charge fee of the subscription
// and charge.
func ChargeFee(s *subscription.Subscription, chargeID core.ChargeID) func(*invoice.Fee) {
	return func(f *invoice.Fee) {
		f.Type, f.SubscriptionID, f.ChargeID = core.FeeTypeCharge, s.ID, chargeID
	}
}

// SubscriptionFee is a fee option making it the subscription fee of s.
func SubscriptionFee(s *subscription.Subscription) func(*invoice.Fee) {
	return func(f *invoice.Fee) { f.Type, f.SubscriptionID = core.FeeTypeSubscription, s.ID }
}

// LinkSubscription links an invoice to a subscription for the charges
// period [from, to) (also its billing period) with reason.
func (h *Harness) LinkSubscription(inv *invoice.Invoice, s *subscription.Subscription, reason invoice.InvoicingReason, from, to time.Time) *invoice.Subscription {
	h.T.Helper()
	p := period.Period{From: from.UTC(), To: to.UTC()}
	is := &invoice.Subscription{
		ID: core.NewID[core.InvoiceSubscriptionID](h.IDs), OrganizationID: h.OrgID(), InvoiceID: inv.ID,
		SubscriptionID: s.ID, Reason: reason, Period: p, ChargesPeriod: p, FixedChargesPeriod: p,
		Timestamp: p.To, Recurring: reason == invoice.InvoicingReasonSubscriptionPeriodic, CreatedAt: h.Now(),
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.InvoiceSubscriptions.Create(ctx, is) })
	return is
}

// StoredCreditNote creates a finalized credit note of the invoice with an
// available credit of amount (minor units); options run before it is
// stored.
func (h *Harness) StoredCreditNote(inv *invoice.Invoice, amount int64, opts ...func(*creditnote.CreditNote)) *creditnote.CreditNote {
	h.T.Helper()
	id := core.NewID[core.CreditNoteID](h.IDs)
	cn := &creditnote.CreditNote{
		ID: id, OrganizationID: h.OrgID(), InvoiceID: inv.ID, CustomerID: inv.CustomerID,
		Number: fmt.Sprintf("CN-%s", id.String()[24:]), SequentialID: 1, Status: creditnote.StatusFinalized,
		CreditStatus: creditnote.CreditStatusAvailable, Reason: creditnote.ReasonOther,
		CreditAmount: amount, BalanceAmount: amount, TotalAmount: amount, Currency: inv.Currency,
		PreciseCouponsAdjustmentAmount: money.Zero, PreciseTaxesAmount: money.Zero, TaxesRate: money.Zero,
		IssuingDate: core.DateOf(h.Now(), time.UTC), CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	apply(cn, opts)
	h.Do(func(ctx context.Context, r port.Repos) error { return r.CreditNotes.Create(ctx, cn) })
	return cn
}

// ReloadCreditNote reloads a credit note.
func (h *Harness) ReloadCreditNote(id core.CreditNoteID) *creditnote.CreditNote {
	h.T.Helper()
	return Read(h, func(ctx context.Context, r port.Repos) (*creditnote.CreditNote, error) {
		return r.CreditNotes.Get(ctx, h.OrgID(), id)
	})
}

// ReloadAppliedCoupon reloads an applied coupon.
func (h *Harness) ReloadAppliedCoupon(id core.AppliedCouponID) *coupon.AppliedCoupon {
	h.T.Helper()
	return Read(h, func(ctx context.Context, r port.Repos) (*coupon.AppliedCoupon, error) {
		return r.AppliedCoupons.Get(ctx, h.OrgID(), id)
	})
}

// InboundTransaction creates a settled inbound transaction of the wallet
// of credits with status (granted or purchased), with a remaining amount
// when the wallet is traceable. It does not change the wallet's balance.
func (h *Harness) InboundTransaction(w *wallet.Wallet, status wallet.TransactionStatus, credits string, opts ...func(*wallet.Transaction)) *wallet.Transaction {
	h.T.Helper()
	cr := money.MustParse(credits)
	amount, err := wallet.AmountFromCredits(cr, w.Currency, w.RateAmount)
	if err != nil {
		h.T.Fatalf("wallet credits: %v", err)
	}
	t := &wallet.Transaction{
		ID: core.NewID[core.WalletTransactionID](h.IDs), OrganizationID: h.OrgID(), WalletID: w.ID,
		TransactionType: wallet.TransactionTypeInbound, TransactionStatus: status, Status: wallet.SettlementStatusSettled,
		Source: wallet.TransactionSourceManual, Currency: w.Currency, Amount: amount, CreditAmount: cr,
		Priority: w.Priority, CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	if w.Traceable {
		rem := amount
		t.RemainingAmount = &rem
	}
	now := h.Now()
	t.SettledAt = &now
	apply(t, opts)
	h.Do(func(ctx context.Context, r port.Repos) error { return r.WalletTransactions.Create(ctx, t) })
	return t
}

// WalletTransactions returns the transactions of an invoice.
func (h *Harness) WalletTransactions(id core.InvoiceID) []*wallet.Transaction {
	h.T.Helper()
	return Read(h, func(ctx context.Context, r port.Repos) ([]*wallet.Transaction, error) {
		return r.WalletTransactions.ListByInvoice(ctx, h.OrgID(), id)
	})
}

// ReloadWalletTransaction reloads a wallet transaction.
func (h *Harness) ReloadWalletTransaction(id core.WalletTransactionID) *wallet.Transaction {
	h.T.Helper()
	return Read(h, func(ctx context.Context, r port.Repos) (*wallet.Transaction, error) {
		return r.WalletTransactions.Get(ctx, h.OrgID(), id)
	})
}

// InvoiceCredits returns the credits of an invoice.
func (h *Harness) InvoiceCredits(id core.InvoiceID) []*invoice.Credit {
	h.T.Helper()
	return Read(h, func(ctx context.Context, r port.Repos) ([]*invoice.Credit, error) {
		return r.Credits.ListByInvoice(ctx, h.OrgID(), id)
	})
}

// CreateCredit stores an invoice credit.
func (h *Harness) CreateCredit(c *invoice.Credit) {
	h.T.Helper()
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Credits.Create(ctx, c) })
}
