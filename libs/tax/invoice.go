package tax

import (
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// InvoiceTaxes are the tax fields of an invoice.
type InvoiceTaxes struct {
	// AppliedTaxes are the invoice's taxes, in order of first appearance on
	// its fees.
	AppliedTaxes []InvoiceAppliedTax
	// TaxesAmountCents is the invoice's tax total.
	TaxesAmountCents int64
	// TaxesRate is the invoice's effective rate in percent, rounded to five
	// places.
	TaxesRate money.Decimal
}

// ApplyInvoiceTaxes sums the internal taxes of an invoice's fees (see
// [ApplyFeeTaxes]) into invoice taxes, one per tax code. Each tax is charged
// on the sum of its fees' amounts after coupons, so its amount may differ
// by rounding from the sum of the fees' tax lines; the invoice total rounds
// the sum of the unrounded amounts once. subTotal is the invoice's amount
// after coupons and progressive billing credits. The rate of each tax weighs
// by the share of the subtotal its fees make up or, when the subtotal is not
// positive, by the share of the invoice's fees it applies to. Provider tax
// rows are ignored.
func ApplyInvoiceTaxes(subTotal int64, fees []Fee) InvoiceTaxes {
	type entry struct {
		row  FeeAppliedTax
		fees []int
	}
	var keys []string
	entries := map[string]*entry{}
	for i, f := range fees {
		for _, row := range f.AppliedTaxes {
			if row.Provider {
				continue
			}
			e, ok := entries[row.TaxCode]
			if !ok {
				e = &entry{row: row}
				entries[row.TaxCode] = e
				keys = append(keys, row.TaxCode)
			}
			e.fees = append(e.fees, i)
		}
	}
	out := InvoiceTaxes{TaxesRate: money.Zero}
	total, rate := money.Zero, money.Zero
	for _, code := range keys {
		e := entries[code]
		feesAmount := money.Zero
		for _, i := range e.fees {
			feesAmount = feesAmount.Add(fees[i].SubTotalExcludingTaxesAmountCents())
		}
		amount := percentOf(feesAmount, e.row.TaxRate)
		out.AppliedTaxes = append(out.AppliedTaxes, InvoiceAppliedTax{
			TaxCode:         e.row.TaxCode,
			TaxName:         e.row.TaxName,
			TaxDescription:  e.row.TaxDescription,
			TaxRate:         e.row.TaxRate,
			AmountCents:     roundCents(amount),
			FeesAmountCents: roundCents(feesAmount),
		})
		total = total.Add(amount)
		var share money.Decimal
		if subTotal > 0 {
			share = feesAmount.Div(money.NewFromInt(subTotal))
		} else {
			share = money.NewFromInt(int64(len(e.fees))).Div(money.NewFromInt(int64(len(fees))))
		}
		rate = rate.Add(share.Mul(e.row.TaxRate))
	}
	out.TaxesAmountCents = roundCents(total)
	out.TaxesRate = rate.Round(5, money.HalfUp)
	return out
}

// InvoiceFee is a fee of an invoice being computed, with what its taxes
// come from.
type InvoiceFee struct {
	Fee
	// Taxes are the taxes that apply to the fee (see [ResolveFeeTaxes]),
	// for internal taxes.
	Taxes []Tax
	// ProviderResult is the tax provider's result for the fee, for provider
	// taxes; nil when the provider did not report it.
	ProviderResult *ProviderTaxResult
}

// InvoiceAmountsInput is the input of [ComputeInvoiceAmounts].
type InvoiceAmountsInput struct {
	// Fees are the invoice's fees. A fee that already has applied taxes
	// keeps them.
	Fees []InvoiceFee
	// UseProviderTaxes selects provider taxes (the customer is taxed by a
	// tax provider and the provider answered) over internal taxes.
	UseProviderTaxes bool
	// SkipFeeTaxes leaves the fees' taxes as they are (advance charges
	// invoices, whose fees were taxed when billed).
	SkipFeeTaxes bool
	// CouponsAmountCents is the sum of the invoice's coupon credits.
	CouponsAmountCents int64
	// ProgressiveBillingCreditAmountCents is the amount already billed by
	// progressive billing invoices in the period.
	ProgressiveBillingCreditAmountCents int64
	// CreditNotesAmountCents is the sum of the credit notes applied to the
	// invoice.
	CreditNotesAmountCents int64
}

// InvoiceAmounts are the amounts of an invoice.
type InvoiceAmounts struct {
	// Fees are the fees with their taxes.
	Fees []Fee
	// FeesAmountCents is the sum of the fees' amounts.
	FeesAmountCents int64
	// CouponsAmountCents is the coupons total.
	CouponsAmountCents int64
	// SubTotalExcludingTaxesAmountCents is the fees less progressive
	// billing credits and coupons.
	SubTotalExcludingTaxesAmountCents int64
	InvoiceTaxes
	// SubTotalIncludingTaxesAmountCents is the subtotal plus taxes.
	SubTotalIncludingTaxesAmountCents int64
	// TotalAmountCents is the subtotal including taxes less credit notes.
	TotalAmountCents int64
}

// ComputeInvoiceAmounts computes the taxes of an invoice's fees and the
// invoice's amounts: fees, coupons, subtotal before taxes, taxes (internal
// or provider), subtotal including taxes and total.
func ComputeInvoiceAmounts(in InvoiceAmountsInput) (InvoiceAmounts, error) {
	out := InvoiceAmounts{Fees: make([]Fee, len(in.Fees))}
	for i, f := range in.Fees {
		fee := f.Fee
		if !in.SkipFeeTaxes && len(fee.AppliedTaxes) == 0 {
			if in.UseProviderTaxes {
				taxes, err := ApplyProviderFeeTaxes(fee, f.ProviderResult)
				if err != nil {
					return InvoiceAmounts{}, fmt.Errorf("fee %d: %w", i, err)
				}
				fee.FeeTaxes = taxes
			} else {
				fee.FeeTaxes = ApplyFeeTaxes(fee, f.Taxes)
			}
		}
		out.Fees[i] = fee
		out.FeesAmountCents += fee.AmountCents
	}
	out.CouponsAmountCents = in.CouponsAmountCents
	out.SubTotalExcludingTaxesAmountCents = out.FeesAmountCents - in.ProgressiveBillingCreditAmountCents - in.CouponsAmountCents
	if in.UseProviderTaxes {
		out.InvoiceTaxes = ApplyProviderInvoiceTaxes(out.SubTotalExcludingTaxesAmountCents, out.Fees)
	} else {
		out.InvoiceTaxes = ApplyInvoiceTaxes(out.SubTotalExcludingTaxesAmountCents, out.Fees)
	}
	out.SubTotalIncludingTaxesAmountCents = out.SubTotalExcludingTaxesAmountCents + out.TaxesAmountCents
	out.TotalAmountCents = out.SubTotalIncludingTaxesAmountCents - in.CreditNotesAmountCents
	return out, nil
}

// AggregateInvoiceTaxes computes the amounts of an advance charges invoice
// from fees already taxed when they were billed: the taxes are the sum of
// the fees' taxes, grouped by tax code (internal taxes) or by code, rate and
// description (provider taxes), and the rate is the taxes over the fees,
// rounded to two places. Coupons, progressive billing and credit notes do
// not apply.
func AggregateInvoiceTaxes(fees []Fee) InvoiceAmounts {
	out := InvoiceAmounts{Fees: fees, InvoiceTaxes: InvoiceTaxes{TaxesRate: money.Zero}}
	if len(fees) == 0 {
		return out
	}
	type entry struct {
		row                    FeeAppliedTax
		amount                 int64
		feesAmount, taxableSum money.Decimal
	}
	var keys []string
	entries := map[string]*entry{}
	for _, f := range fees {
		out.FeesAmountCents += f.AmountCents
		out.TaxesAmountCents += f.TaxesAmountCents
		for _, row := range f.AppliedTaxes {
			key := "tax:" + row.TaxCode
			if row.Provider {
				key = "provider:" + row.TaxCode + "|" + rateKey(row.TaxRate) + "|" + row.TaxDescription
			}
			e, ok := entries[key]
			if !ok {
				e = &entry{row: row, feesAmount: money.Zero, taxableSum: money.Zero}
				entries[key] = e
				keys = append(keys, key)
			}
			sub := f.SubTotalExcludingTaxesAmountCents()
			e.amount += row.AmountCents
			e.feesAmount = e.feesAmount.Add(sub)
			e.taxableSum = e.taxableSum.Add(f.TaxesBaseRate.Mul(sub))
		}
	}
	for _, key := range keys {
		e := entries[key]
		out.AppliedTaxes = append(out.AppliedTaxes, InvoiceAppliedTax{
			TaxCode:                e.row.TaxCode,
			TaxName:                e.row.TaxName,
			TaxDescription:         e.row.TaxDescription,
			TaxRate:                e.row.TaxRate,
			Provider:               e.row.Provider,
			AmountCents:            e.amount,
			FeesAmountCents:        roundCents(e.feesAmount),
			TaxableBaseAmountCents: roundCents(e.taxableSum),
		})
	}
	out.SubTotalExcludingTaxesAmountCents = out.FeesAmountCents
	out.SubTotalIncludingTaxesAmountCents = out.FeesAmountCents + out.TaxesAmountCents
	out.TotalAmountCents = out.SubTotalIncludingTaxesAmountCents
	if out.FeesAmountCents != 0 {
		out.TaxesRate = money.NewFromInt(out.TaxesAmountCents*100).Div(money.NewFromInt(out.FeesAmountCents)).Round(2, money.HalfUp)
	}
	return out
}
