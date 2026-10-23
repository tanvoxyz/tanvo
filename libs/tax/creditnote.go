package tax

import (
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// CreditNoteItem is an amount of a fee credited by a credit note.
type CreditNoteItem struct {
	// Fee is the index of the credited fee in the invoice's fees.
	Fee int
	// PreciseAmountCents is the credited amount, before coupons and taxes.
	PreciseAmountCents money.Decimal
}

// CreditNoteTaxesInput is the input of [ComputeCreditNoteTaxes].
type CreditNoteTaxesInput struct {
	// Invoice is the credited invoice, with its fees and taxes.
	Invoice Invoice
	// Items are the credit note's items.
	Items []CreditNoteItem
	// TaxesRoundingAdjustmentCents, when not zero, is the sum over the
	// invoice's earlier credit notes of their booked less their exact taxes;
	// it is taken off this credit note's exact taxes so that crediting an
	// invoice in several notes credits exactly its tax.
	TaxesRoundingAdjustmentCents money.Decimal
}

// CreditNoteAppliedTax is a tax credited by a credit note.
type CreditNoteAppliedTax struct {
	TaxCode        string
	TaxName        string
	TaxDescription string
	// TaxRate is the rate in percent.
	TaxRate money.Decimal
	// Provider is true for a tax computed by a tax provider.
	Provider bool
	// BaseAmountCents is the credited taxable amount, rounded.
	BaseAmountCents int64
	// AmountCents is the credited tax, allocated so that the credit note's
	// taxes add up to its tax total.
	AmountCents int64
	// PreciseAmountCents is the exact credited tax.
	PreciseAmountCents money.Decimal
}

// CreditNoteTaxes are the tax fields of a credit note.
type CreditNoteTaxes struct {
	// AppliedTaxes are the credited taxes, one per invoice tax.
	AppliedTaxes []CreditNoteAppliedTax
	// PreciseCouponsAdjustmentAmountCents is the coupons share of the
	// credited amounts, which is not credited.
	PreciseCouponsAdjustmentAmountCents money.Decimal
	// CouponsAdjustmentAmountCents is the same, rounded.
	CouponsAdjustmentAmountCents int64
	// PreciseTaxesAmountCents is the exact credited tax, after the rounding
	// adjustment.
	PreciseTaxesAmountCents money.Decimal
	// TaxesAmountCents is the credited tax, rounded.
	TaxesAmountCents int64
	// TaxesRate is the credit note's effective rate in percent, rounded to
	// five places.
	TaxesRate money.Decimal
}

// ComputeCreditNoteTaxes computes the taxes a credit note credits.
//
// Every applied tax of a credited fee resolves to an invoice tax
// ([Invoice.AppliedTaxFor]); an unresolved one fails with
// [ErrInvoiceAppliedTaxNotFound]. Each invoice tax credits, on the items it
// applies to (an item counting once per fee tax resolving to it), their
// amounts less their coupons share, scaled by the invoice tax's taxed share
// of its fees. An internal tax credits that base at its rate; a provider tax
// credits the cents booked on each fee ([BookTaxes]) in proportion to the
// credited part of the fee, so crediting every fee credits exactly what was
// charged. The credit note's tax rounds the sum of the exact amounts once,
// and is allocated back over the taxes.
func ComputeCreditNoteTaxes(in CreditNoteTaxesInput) (CreditNoteTaxes, error) {
	inv := in.Invoice
	for i, it := range in.Items {
		if it.Fee < 0 || it.Fee >= len(inv.Fees) {
			return CreditNoteTaxes{}, fmt.Errorf("item %d: %w", i, ErrFeeOutOfRange)
		}
	}

	coupon := func(it CreditNoteItem) money.Decimal {
		f := inv.Fees[it.Fee]
		if f.AmountCents == 0 {
			return money.Zero
		}
		return f.PreciseCouponsAmountCents.Mul(it.PreciseAmountCents).Div(money.NewFromInt(f.AmountCents))
	}
	out := CreditNoteTaxes{PreciseCouponsAdjustmentAmountCents: money.Zero, TaxesRate: money.Zero}
	items := money.Zero
	for _, it := range in.Items {
		out.PreciseCouponsAdjustmentAmountCents = out.PreciseCouponsAdjustmentAmountCents.Add(coupon(it))
		items = items.Add(it.PreciseAmountCents)
	}
	out.CouponsAdjustmentAmountCents = roundCents(out.PreciseCouponsAdjustmentAmountCents)

	type entry struct {
		tax   int
		items []int // with repeats, one per fee tax
	}
	var order []int
	entries := map[int]*entry{}
	for i, it := range in.Items {
		for _, row := range inv.Fees[it.Fee].AppliedTaxes {
			t, ok := inv.AppliedTaxFor(row)
			if !ok {
				return CreditNoteTaxes{}, fmt.Errorf("fee tax %s at %s: %w", row.TaxCode, row.TaxRate, ErrInvoiceAppliedTaxNotFound)
			}
			e, ok := entries[t]
			if !ok {
				e = &entry{tax: t}
				entries[t] = e
				order = append(order, t)
			}
			e.items = append(e.items, i)
		}
	}

	var booked [][]int64 // per invoice tax, per fee
	if inv.ProviderTaxes() {
		booked = make([][]int64, len(inv.AppliedTaxes))
		for t := range booked {
			booked[t] = make([]int64, len(inv.Fees))
		}
		for f, b := range BookTaxes(inv) {
			for r, amount := range b.RowAmountCents {
				if t, ok := inv.AppliedTaxFor(inv.Fees[f].AppliedTaxes[r]); ok {
					booked[t][f] += amount
				}
			}
		}
	}

	denominator := items.Sub(out.PreciseCouponsAdjustmentAmountCents)
	precise := money.Zero
	rate := money.Zero
	exacts := make([]money.Decimal, 0, len(order))
	for _, t := range order {
		e := entries[t]
		it := inv.AppliedTaxes[t]
		base := money.Zero
		for _, i := range e.items {
			base = base.Add(in.Items[i].PreciseAmountCents.Sub(coupon(in.Items[i])))
		}
		baseRate := money.One
		if it.FeesAmountCents != 0 {
			baseRate = money.NewFromInt(it.TaxableAmountCents()).Div(money.NewFromInt(it.FeesAmountCents))
		}
		preciseBase := base.Mul(baseRate)
		var amount money.Decimal
		if it.IsProviderTax() {
			amount = money.Zero
			seen := map[int]bool{}
			for _, i := range e.items {
				if seen[i] {
					continue
				}
				seen[i] = true
				f := inv.Fees[in.Items[i].Fee]
				if f.AmountCents == 0 {
					continue
				}
				amount = amount.Add(money.NewFromInt(booked[t][in.Items[i].Fee]).Mul(in.Items[i].PreciseAmountCents).Div(money.NewFromInt(f.AmountCents)))
			}
		} else {
			amount = percentOf(preciseBase, it.TaxRate)
		}
		out.AppliedTaxes = append(out.AppliedTaxes, CreditNoteAppliedTax{
			TaxCode:            it.TaxCode,
			TaxName:            it.TaxName,
			TaxDescription:     it.TaxDescription,
			TaxRate:            it.TaxRate,
			Provider:           it.Provider,
			BaseAmountCents:    roundCents(preciseBase),
			PreciseAmountCents: amount,
		})
		exacts = append(exacts, amount)
		precise = precise.Add(amount)
		if !denominator.IsZero() {
			rate = rate.Add(base.Div(denominator).Mul(it.TaxRate))
		}
	}

	out.PreciseTaxesAmountCents = precise.Sub(in.TaxesRoundingAdjustmentCents)
	out.TaxesAmountCents = roundCents(out.PreciseTaxesAmountCents)
	out.TaxesRate = rate.Round(5, money.HalfUp)
	for i, a := range Allocate(money.NewFromInt(out.TaxesAmountCents), exacts) {
		out.AppliedTaxes[i].AmountCents = a
	}
	return out, nil
}

// CreditNoteAmounts are the amounts of a credit note that its rounding
// adjustment changes.
type CreditNoteAmounts struct {
	// SubTotalExcludingTaxesAmountCents is the credited amount less coupons,
	// rounded from the exact amounts.
	SubTotalExcludingTaxesAmountCents int64
	// TaxesAmountCents is the credited tax.
	TaxesAmountCents int64
	// TotalAmountCents is the credit note's total.
	TotalAmountCents int64
	// CreditAmountCents, RefundAmountCents and OffsetAmountCents split the
	// total between credit to the wallet balance, refund and offset against
	// the invoice.
	CreditAmountCents int64
	RefundAmountCents int64
	OffsetAmountCents int64
	// BalanceAmountCents is the credit left to consume.
	BalanceAmountCents int64
}

// AdjustCreditNoteRounding moves the total by a cent when the subtotal plus
// taxes, each rounded on its own, miss it by one, so that the subtotal plus
// the taxes is the total. The cent goes to the credit when there is one
// (resetting the balance to it), else to the refund when there is one, else
// to the offset when there is one, else to the refund.
func AdjustCreditNoteRounding(a CreditNoteAmounts) CreditNoteAmounts {
	sub := a.TotalAmountCents - a.TaxesAmountCents
	if sub == a.SubTotalExcludingTaxesAmountCents {
		return a
	}
	delta := int64(1)
	if sub > a.SubTotalExcludingTaxesAmountCents {
		delta = -1
	}
	a.TotalAmountCents += delta
	switch {
	case a.CreditAmountCents > 0:
		a.CreditAmountCents += delta
	case a.RefundAmountCents > 0:
		a.RefundAmountCents += delta
	case a.OffsetAmountCents > 0:
		a.OffsetAmountCents += delta
	default:
		a.RefundAmountCents += delta
	}
	a.BalanceAmountCents = a.CreditAmountCents
	return a
}

// CreditNoteSubTotal is the credited amount less coupons, rounded: the
// subtotal [AdjustCreditNoteRounding] reconciles the total with.
func CreditNoteSubTotal(items []CreditNoteItem, preciseCouponsAdjustmentAmountCents money.Decimal) int64 {
	sum := money.Zero
	for _, it := range items {
		sum = sum.Add(it.PreciseAmountCents)
	}
	return roundCents(sum.Sub(preciseCouponsAdjustmentAmountCents))
}
