package memstore

import (
	"slices"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

// The checks of this file stand in for the foreign keys of the store on
// deletes that span several aggregates: a row still referenced by a
// restricting key is not deleted (core.ErrConflict), and the rows of a
// cascading key go with the deleted row.

// set is a set of keys.
type set[K comparable] map[K]bool

// checkCreditNotesDeletable fails with core.ErrConflict when one of the
// credit notes gone is still referenced by a refund, a wallet transaction,
// or a settlement or credit of an invoice that stays (invoicesGone).
func (st *state) checkCreditNotesDeletable(gone set[core.CreditNoteID], invoicesGone set[core.InvoiceID]) error {
	if len(gone) == 0 {
		return nil
	}
	switch {
	case st.payment.refunds.any(func(x *payment.Refund) bool { return gone[x.CreditNoteID] }):
		return conflict("credit_note", "still referenced by a refund")
	case st.wallet.transactions.any(func(x *wallet.Transaction) bool { return gone[x.CreditNoteID] }):
		return conflict("credit_note", "still referenced by a wallet transaction")
	case st.invoice.settlements.any(func(x *invoice.Settlement) bool { return gone[x.CreditNoteID] && !invoicesGone[x.InvoiceID] }):
		return conflict("credit_note", "still referenced by an invoice settlement")
	case st.invoice.credits.any(func(x *invoice.Credit) bool { return gone[x.CreditNoteID] && !invoicesGone[x.InvoiceID] }):
		return conflict("credit_note", "still referenced by an invoice credit")
	}
	return nil
}

// checkFeesDeletable fails with core.ErrConflict when one of the fees gone
// is still referenced by an item of a credit note that stays
// (creditNotesGone).
func (st *state) checkFeesDeletable(gone set[core.FeeID], creditNotesGone set[core.CreditNoteID]) error {
	if st.creditNote.creditNotes.any(func(cn *creditnote.CreditNote) bool {
		return !creditNotesGone[cn.ID] && slices.ContainsFunc(cn.Items, func(i creditnote.Item) bool { return gone[i.FeeID] })
	}) {
		return conflict("fee", "still referenced by a credit note item")
	}
	return nil
}

// feesWithTrueUps returns the IDs of the fees matching pred and of their
// true-up fees, which cascade with them.
func (st *state) feesWithTrueUps(pred func(*invoice.Fee) bool) set[core.FeeID] {
	gone := set[core.FeeID]{}
	for _, f := range st.invoice.fees.find(pred) {
		gone[f.ID] = true
	}
	for grew := true; grew; {
		grew = false
		for _, f := range st.invoice.fees.find(func(x *invoice.Fee) bool { return !gone[x.ID] && gone[x.TrueUpParentFeeID] }) {
			gone[f.ID] = true
			grew = true
		}
	}
	return gone
}

// deleteInvoice removes the invoice id with the rows that cascade with it
// (its subscriptions, fees, applied taxes, adjusted fees, settlements,
// error details, credits, applied usage thresholds and credit notes),
// unless a row that stays still references it, one of its fees or one of
// its credit notes (core.ErrConflict).
func (st *state) deleteInvoice(org core.OrganizationID, id core.InvoiceID) error {
	d := st.invoice
	switch {
	case d.invoices.any(func(x *invoice.Invoice) bool { return x.ID != id && x.VoidedInvoiceID == id }):
		return conflict("invoice", "still referenced by its regenerated invoice")
	case d.credits.any(func(x *invoice.Credit) bool { return x.InvoiceID != id && x.ProgressiveBillingInvoiceID == id }):
		return conflict("invoice", "still referenced by an invoice credit")
	case st.payment.requests.any(func(x *payment.Request) bool { return slices.Contains(x.InvoiceIDs, id) }):
		return conflict("invoice", "still referenced by a payment request")
	case st.payment.payments.any(func(x *payment.Payment) bool { return x.Payable.InvoiceID == id }):
		return conflict("invoice", "still referenced by a payment")
	case st.payment.intents.any(func(x *payment.Intent) bool { return x.InvoiceID == id }):
		return conflict("invoice", "still referenced by a payment intent")
	case st.wallet.transactions.any(func(x *wallet.Transaction) bool { return x.InvoiceID == id }):
		return conflict("invoice", "still referenced by a wallet transaction")
	}
	invoices := set[core.InvoiceID]{id: true}
	creditNotes := set[core.CreditNoteID]{}
	for _, cn := range st.creditNote.creditNotes.find(func(x *creditnote.CreditNote) bool { return x.InvoiceID == id }) {
		creditNotes[cn.ID] = true
	}
	if err := st.checkCreditNotesDeletable(creditNotes, invoices); err != nil {
		return err
	}
	fees := st.feesWithTrueUps(func(x *invoice.Fee) bool { return x.OrganizationID == org && x.InvoiceID == id })
	if err := st.checkFeesDeletable(fees, creditNotes); err != nil {
		return err
	}
	d.invoices.del(id)
	d.subscriptions.deleteWhere(func(x *invoice.Subscription) bool { return x.InvoiceID == id })
	d.fees.deleteWhere(func(x *invoice.Fee) bool { return fees[x.ID] })
	d.appliedTaxes.deleteWhere(func(x *invoice.AppliedTax) bool { return x.InvoiceID == id })
	d.adjustedFees.deleteWhere(func(x *invoice.AdjustedFee) bool { return x.InvoiceID == id })
	d.settlements.deleteWhere(func(x *invoice.Settlement) bool { return x.InvoiceID == id })
	d.errorDetails.deleteWhere(func(x *invoice.ErrorDetail) bool { return x.InvoiceID == id })
	d.credits.deleteWhere(func(x *invoice.Credit) bool { return x.InvoiceID == id })
	st.subscription.appliedUsageThresholds.deleteWhere(func(x *subscription.AppliedUsageThreshold) bool { return x.InvoiceID == id })
	st.creditNote.creditNotes.deleteWhere(func(x *creditnote.CreditNote) bool { return creditNotes[x.ID] })
	return nil
}
