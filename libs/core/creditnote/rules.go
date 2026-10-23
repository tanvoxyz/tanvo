package creditnote

import "github.com/tanvoxyz/tanvo/libs/core"

// Field error codes of the credit note checks (domain §13.2).
const (
	// CodeTotalAmountMustBePositive: credit + refund + offset is 0.
	CodeTotalAmountMustBePositive = "total_amount_must_be_positive"
	// CodeDoesNotMatchItemAmounts: credit + refund + offset differs from the
	// total computed from the items.
	CodeDoesNotMatchItemAmounts = "does_not_match_item_amounts"
	// CodeHigherThanRemainingInvoiceAmount: an amount exceeds what is left
	// to credit, refund or offset on the invoice.
	CodeHigherThanRemainingInvoiceAmount = "higher_than_remaining_invoice_amount"
	// CodeHigherThanRemainingFeeAmount: an item exceeds the fee's creditable
	// amount.
	CodeHigherThanRemainingFeeAmount = "higher_than_remaining_fee_amount"
	// CodeHigherThanWalletBalance: an item of a credit invoice exceeds what
	// is left in the wallet.
	CodeHigherThanWalletBalance = "higher_than_wallet_balance"
	// CodeCannotRefundUnpaidInvoice: a refund on an invoice nothing was paid
	// on.
	CodeCannotRefundUnpaidInvoice = "cannot_refund_unpaid_invoice"
	// CodeCannotCreditInvoice: a credit on a credit invoice, or a note on an
	// invoice that cannot be credited.
	CodeCannotCreditInvoice = "cannot_credit_invoice"
)

// Amounts are the requested split of a credit note, in minor units.
type Amounts struct {
	// Credit goes to the customer's balance.
	Credit int64
	// Refund goes back through the payment provider.
	Refund int64
	// Offset settles the invoice's due amount.
	Offset int64
}

// Total returns credit + refund + offset.
func (a Amounts) Total() int64 { return a.Credit + a.Refund + a.Offset }

// Facts are the facts about the invoice that [Check] needs; they are
// computed from the invoice (invoice.CreditableAmount,
// invoice.OffsettableAmount) and its other credit notes.
type Facts struct {
	// CreditInvoice is true for a wallet credit invoice, which can only be
	// refunded or offset.
	CreditInvoice bool
	// ItemsTotal is Σ items − coupons adjustment + taxes, rounded.
	ItemsTotal int64
	// Creditable is what non-voided credit notes may still credit on the
	// invoice.
	Creditable int64
	// Paid is the amount paid on the invoice.
	Paid int64
	// AlreadyRefunded is Σ refunds of the invoice's other credit notes.
	AlreadyRefunded int64
	// Offsettable is the amount an offset may still settle.
	Offsettable int64
}

// Check applies the amount rules of §13.2 to a requested split: a positive
// total equal to the item total, a refund within what was paid, a credit
// only on regular invoices, an offset within the offsettable amount, and a
// total within the creditable amount.
func Check(a Amounts, f Facts) error {
	var v core.Validator
	v.NonNegative("credit_amount", a.Credit)
	v.NonNegative("refund_amount", a.Refund)
	v.NonNegative("offset_amount", a.Offset)
	if err := v.Err(); err != nil {
		return err
	}
	total := a.Total()
	if !v.Check(total > 0, "base", CodeTotalAmountMustBePositive) {
		return v.Err()
	}
	v.Check(total == f.ItemsTotal, "base", CodeDoesNotMatchItemAmounts)
	if a.Refund > 0 {
		if v.Check(f.Paid > 0, "refund_amount", CodeCannotRefundUnpaidInvoice) {
			v.Check(a.Refund <= f.Paid-f.AlreadyRefunded, "refund_amount", CodeHigherThanRemainingInvoiceAmount)
		}
	}
	if a.Credit > 0 {
		v.Check(!f.CreditInvoice, "credit_amount", CodeCannotCreditInvoice)
	}
	if a.Offset > 0 {
		v.Check(a.Offset <= f.Offsettable, "offset_amount", CodeHigherThanRemainingInvoiceAmount)
	}
	if !f.CreditInvoice {
		v.Check(total <= f.Creditable, "base", CodeHigherThanRemainingInvoiceAmount)
	}
	return v.Err()
}

// CheckItem checks one item: a non-negative amount within the fee's
// creditable amount (fee.CreditableAmount), and for a credit fee within the
// wallet balance left (pass a negative walletBalance when there is no
// wallet cap).
func CheckItem(amount, feeCreditable, walletBalance int64) error {
	var v core.Validator
	if !v.Check(amount >= 0, "amount", core.CodeInvalid) {
		return v.Err()
	}
	if walletBalance >= 0 && amount > walletBalance {
		v.Add("amount", CodeHigherThanWalletBalance)
		return v.Err()
	}
	v.Check(amount <= feeCreditable, "amount", CodeHigherThanRemainingFeeAmount)
	return v.Err()
}
