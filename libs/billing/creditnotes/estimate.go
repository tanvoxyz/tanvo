package creditnotes

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Estimate computes, without storing anything, the credit note the items
// would make on the invoice: its taxes and coupons adjustment, the amount
// it may credit and the part of it that may be refunded (RefundAmount).
// Its split is not checked: the caller picks one up to these amounts.
func (s *Service) Estimate(ctx context.Context, org core.OrganizationID, invoiceID core.InvoiceID, items []ItemInput) (*creditnote.CreditNote, error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*creditnote.CreditNote, error) {
		st, err := loadInvoice(ctx, r, org, invoiceID)
		if err != nil {
			return nil, err
		}
		return s.estimate(st, items)
	})
}

func (s *Service) estimate(st *invoiceState, items []ItemInput) (*creditnote.CreditNote, error) {
	inv := st.inv
	if inv.Type == invoice.TypeCredit && (inv.PaymentStatus != core.PaymentStatusSucceeded || st.wallet == nil) {
		return nil, rule(CodeInvalidTypeOrStatus, "invoice cannot be credited in its type or status")
	}
	cn := &creditnote.CreditNote{
		OrganizationID: inv.OrganizationID, InvoiceID: inv.ID, CustomerID: inv.CustomerID,
		Status: creditnote.StatusFinalized, Reason: creditnote.ReasonOther, Currency: inv.Currency,
	}
	var v core.Validator
	v.Check(len(items) > 0, "items", core.CodeRequired)
	if err := v.Err(); err != nil {
		return nil, err
	}
	precise := money.Zero
	for i, it := range items {
		f, _, ok := st.fee(it.FeeID)
		if !ok {
			return nil, notFound("fee", it.FeeID)
		}
		// Estimates take whole minor units.
		amount, _ := it.Amount.Truncate(0).Int64()
		if err := st.validItem(i, amount, f); err != nil {
			return nil, err
		}
		cn.Items = append(cn.Items, creditnote.Item{FeeID: f.ID, Amount: amount, PreciseAmount: money.NewFromInt(amount)})
		precise = precise.Add(money.NewFromInt(amount))
	}
	var creditable int64
	for _, f := range st.fees {
		creditable += st.feeCreditable(f)
	}
	res, err := st.applyTaxes(s.d.IDs, cn, precise.Equal(money.NewFromInt(creditable)))
	if err != nil {
		return nil, err
	}
	cn.CreditAmount = kernel.RoundCents(money.NewFromInt(cn.ItemsAmount()).Sub(res.PreciseCouponsAdjustmentAmountCents).Add(cn.PreciseTaxesAmount))
	cn.RefundAmount = min(cn.CreditAmount, st.refundable())
	if inv.Type == invoice.TypeCredit {
		cn.CreditAmount = 0
	}
	cn.TotalAmount = cn.CreditAmount
	if sub := cn.TotalAmount - cn.TaxesAmount; sub != cn.SubTotalExcludingTaxesAmount {
		switch {
		case sub > cn.SubTotalExcludingTaxesAmount:
			cn.TotalAmount--
		case cn.TaxesAmount > 0:
			cn.TaxesAmount--
		}
		cn.CreditAmount = cn.TotalAmount
		cn.BalanceAmount = cn.CreditAmount
	}
	return cn, nil
}
