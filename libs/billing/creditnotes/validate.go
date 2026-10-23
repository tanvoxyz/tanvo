package creditnotes

import (
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
)

// tolerance is the rounding difference, in minor units, the amount checks
// accept between a requested amount and the amount computed from the
// items.
const tolerance = 1

// validItem checks the item amount on fee f (domain §13.2): not negative,
// within what is left to credit on the fee, and for a credit invoice within
// the wallet balance. An unpaid credit invoice is not capped: its offset
// voids the purchase whatever was consumed.
func (st *invoiceState) validItem(i int, amount int64, f *invoice.Fee) error {
	if amount >= 0 && st.inv.Type == invoice.TypeCredit &&
		(st.inv.PaymentStatus == core.PaymentStatusPending || st.inv.PaymentStatus == core.PaymentStatusFailed) {
		return nil
	}
	wallet := int64(-1)
	if st.inv.Type == invoice.TypeCredit {
		wallet = st.creditableFromWallet()
	}
	creditable := f.Amount - st.credited[f.ID]
	if err := creditnote.CheckItem(amount, creditable, wallet); err != nil {
		var v core.Validator
		v.Merge(fmt.Sprintf("items[%d]", i), err)
		return v.Err()
	}
	return nil
}

// validAmounts checks the requested split of cn against the invoice
// (domain §13.2) with [creditnote.Check], adding the rules of credit
// invoices and the rounding tolerance of the computed amounts.
func (st *invoiceState) validAmounts(cn *creditnote.CreditNote) error {
	inv := st.inv
	a := creditnote.Amounts{Credit: cn.CreditAmount, Refund: cn.RefundAmount, Offset: cn.OffsetAmount}
	total := a.Total()
	credited, refunded, offset := st.sums()
	items := kernel.RoundCents(cn.PreciseTotal())
	if abs(total-items) <= tolerance {
		items = total
	}
	remaining := st.feeTotal() - credited - refunded - offset
	creditable := remaining
	if total > remaining && total-remaining <= tolerance {
		creditable = total
	}
	creditInvoice := inv.Type == invoice.TypeCredit
	paid := inv.TotalPaidAmount
	creditableAmount := st.feeTotal() - credited - offset

	var extra core.Validator
	offsettable := min(inv.TotalAmount-inv.TotalPaidAmount-offset, creditableAmount)
	if a.Offset > 0 && creditInvoice {
		switch {
		case inv.TotalPaidAmount > 0:
			extra.Add("offset_amount", CodeCannotApplyToPaidInvoice)
			offsettable = a.Offset
		case a.Offset != inv.TotalAmount:
			extra.Add("offset_amount", CodeNotEqualToTotalAmount)
			offsettable = a.Offset
		}
	}
	err := creditnote.Check(a, creditnote.Facts{
		CreditInvoice: creditInvoice, ItemsTotal: items, Creditable: creditable,
		Paid: paid, AlreadyRefunded: refunded, Offsettable: offsettable,
	})
	if total <= 0 {
		return err
	}
	if a.Refund > 0 && inv.PaymentStatus != core.PaymentStatusSucceeded && inv.TotalPaidAmount == inv.TotalAmount && inv.TotalAmount > 0 {
		extra.Add("refund_amount", creditnote.CodeCannotRefundUnpaidInvoice)
	}
	if !creditInvoice && a.Credit > creditableAmount+tolerance {
		extra.Add("credit_amount", creditnote.CodeHigherThanRemainingInvoiceAmount)
	}
	if creditInvoice && total > remaining+tolerance {
		extra.Add("base", creditnote.CodeHigherThanRemainingInvoiceAmount)
	}
	return mergeErrors(err, extra.Err())
}

// mergeErrors joins the field errors of a and b without repeating one.
func mergeErrors(a, b error) error {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	fe := core.FieldErrors(a)
	if fe == nil {
		return a
	}
	out := slices.Clone(fe)
	for _, e := range core.FieldErrors(b) {
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return &core.ValidationError{Errors: out}
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
