package tax

import (
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// TaxCategory is a VAT category code of an e-invoice (UNTDID 5305).
type TaxCategory string

// Tax categories.
const (
	// TaxCategoryStandard is the standard rate.
	TaxCategoryStandard TaxCategory = "S"
	// TaxCategoryZero is the zero rate.
	TaxCategoryZero TaxCategory = "Z"
	// TaxCategoryOutOfScope is outside the scope of VAT (prepaid credits).
	TaxCategoryOutOfScope TaxCategory = "O"
)

// TaxCategoryFor is the category of a rate: out of scope for prepaid credit
// purchases, zero rated at 0 %, standard otherwise.
func TaxCategoryFor(rate money.Decimal, credit bool) TaxCategory {
	switch {
	case credit:
		return TaxCategoryOutOfScope
	case rate.IsZero():
		return TaxCategoryZero
	default:
		return TaxCategoryStandard
	}
}

// TaxSubtotal is the VAT breakdown of an e-invoice for one rate.
type TaxSubtotal struct {
	// Category is the VAT category.
	Category TaxCategory
	// Rate is the rate in percent.
	Rate money.Decimal
	// BasisAmountCents is the taxable amount at the rate, unrounded.
	BasisAmountCents money.Decimal
	// TaxAmountCents is the tax at the rate; the subtotals add up to the
	// document's tax.
	TaxAmountCents int64
}

// InvoiceTaxSubtotalsInput is the input of [InvoiceTaxSubtotals].
type InvoiceTaxSubtotalsInput struct {
	// Credit is set for an invoice buying prepaid credits.
	Credit bool
	// Fees are the invoice's fees with their taxes.
	Fees []Fee
	// TaxesAmountCents is the invoice's tax total.
	TaxesAmountCents int64
	// ProviderTaxes is set when the invoice was taxed by a tax provider
	// ([Invoice.ProviderTaxes]).
	ProviderTaxes bool
	// AllowancesAmountCents is the invoice's coupons plus progressive
	// billing credits.
	AllowancesAmountCents int64
}

// InvoiceTaxSubtotals breaks an invoice's tax down by fee rate, ascending.
// The basis at a positive rate is inferred from the fees' exact taxes (never
// more than a fee's amount after coupons, for providers rounding up); at
// 0 % it is the fees' amount less their share of the allowances. The
// invoice's tax is spread over the rates by their booked taxes (provider
// taxes) or exact taxes (internal taxes).
func InvoiceTaxSubtotals(in InvoiceTaxSubtotalsInput) []TaxSubtotal {
	groups := groupByRate(len(in.Fees), func(i int) money.Decimal { return in.Fees[i].TaxesRate })
	feesTotal := money.Zero
	for _, f := range in.Fees {
		feesTotal = feesTotal.Add(f.PreciseAmountCents)
	}
	ag := make([]AllocationGroup, len(groups))
	for g, gr := range groups {
		for _, i := range gr.members {
			f := in.Fees[i]
			if in.ProviderTaxes {
				ag[g].AmountCents = append(ag[g].AmountCents, f.TaxesAmountCents)
			}
			ag[g].PreciseAmountCents = append(ag[g].PreciseAmountCents, f.TaxesPreciseAmountCents)
		}
	}
	var booked []int64
	if in.ProviderTaxes {
		booked = AllocateByGroup(money.NewFromInt(in.TaxesAmountCents), ag)
	} else {
		// Internal taxes weigh by their exact amounts for both passes.
		weights := make([]money.Decimal, len(ag))
		for g := range ag {
			weights[g] = ag[g].precise()
		}
		booked = Allocate(money.NewFromInt(in.TaxesAmountCents), weights)
	}
	out := make([]TaxSubtotal, len(groups))
	for g, gr := range groups {
		total := money.Zero
		for _, i := range gr.members {
			total = total.Add(in.Fees[i].PreciseAmountCents)
		}
		var basis money.Decimal
		if gr.rate.IsPositive() {
			basis = money.Zero
			for _, i := range gr.members {
				f := in.Fees[i]
				inferred := f.TaxesPreciseAmountCents.Mul(money.Hundred).Div(gr.rate)
				basis = basis.Add(money.Min(inferred, f.SubTotalExcludingTaxesPreciseAmountCents()))
			}
		} else {
			share := total.DivOrZero(feesTotal)
			basis = total.Sub(share.Mul(money.NewFromInt(in.AllowancesAmountCents)))
		}
		out[g] = TaxSubtotal{
			Category:         TaxCategoryFor(gr.rate, in.Credit),
			Rate:             gr.rate,
			BasisAmountCents: basis,
			TaxAmountCents:   booked[g],
		}
	}
	return out
}

// CreditNoteTaxSubtotals breaks a credit note's tax down by the rate of the
// credited fees, ascending. The basis is the credited amount less its
// coupons share. The credit note's tax is spread over the rates by the
// fees' taxes prorated to the credited amounts (provider taxes) or by the
// basis at the rate (internal taxes).
func CreditNoteTaxSubtotals(inv Invoice, items []CreditNoteItem, taxesAmountCents int64) ([]TaxSubtotal, error) {
	for i, it := range items {
		if it.Fee < 0 || it.Fee >= len(inv.Fees) {
			return nil, fmt.Errorf("item %d: %w", i, ErrFeeOutOfRange)
		}
	}
	fee := func(i int) Fee { return inv.Fees[items[i].Fee] }
	groups := groupByRate(len(items), func(i int) money.Decimal { return fee(i).TaxesRate })
	prorated := func(i int, amount money.Decimal) money.Decimal {
		f := fee(i)
		if f.AmountCents == 0 {
			return money.Zero
		}
		return amount.Mul(items[i].PreciseAmountCents).Div(money.NewFromInt(f.AmountCents))
	}
	basis := make([]money.Decimal, len(groups))
	for g, gr := range groups {
		basis[g] = money.Zero
		for _, i := range gr.members {
			f := fee(i)
			allowance := money.Zero
			if f.AmountCents != 0 {
				allowance = f.PreciseCouponsAmountCents.Mul(items[i].PreciseAmountCents.DivOrZero(f.PreciseAmountCents))
			}
			basis[g] = basis[g].Add(items[i].PreciseAmountCents.Sub(allowance))
		}
	}
	var booked []int64
	if inv.ProviderTaxes() {
		booked = allocateByGroupDecimal(money.NewFromInt(taxesAmountCents), groups, func(i int) money.Decimal {
			return prorated(i, money.NewFromInt(fee(i).TaxesAmountCents))
		}, func(i int) money.Decimal {
			return prorated(i, fee(i).TaxesPreciseAmountCents)
		})
	} else {
		weights := make([]money.Decimal, len(groups))
		for g, gr := range groups {
			weights[g] = percentOf(basis[g], gr.rate)
		}
		booked = Allocate(money.NewFromInt(taxesAmountCents), weights)
	}
	out := make([]TaxSubtotal, len(groups))
	for g, gr := range groups {
		out[g] = TaxSubtotal{
			Category:         TaxCategoryFor(gr.rate, fee(gr.members[0]).Type == FeeCredit),
			Rate:             gr.rate,
			BasisAmountCents: basis[g],
			TaxAmountCents:   booked[g],
		}
	}
	return out, nil
}

type rateGroup struct {
	rate    money.Decimal
	members []int
}

// groupByRate groups n members by rate, ascending.
func groupByRate(n int, rate func(int) money.Decimal) []rateGroup {
	var groups []rateGroup
	at := map[string]int{}
	for i := range n {
		r := rate(i)
		k := rateKey(r)
		g, ok := at[k]
		if !ok {
			g = len(groups)
			at[k] = g
			groups = append(groups, rateGroup{rate: r})
		}
		groups[g].members = append(groups[g].members, i)
	}
	slices.SortStableFunc(groups, func(a, b rateGroup) int { return a.rate.Cmp(b.rate) })
	return groups
}

// allocateByGroupDecimal is [AllocateByGroup] with decimal booked amounts.
func allocateByGroupDecimal(total money.Decimal, groups []rateGroup, amount, precise func(int) money.Decimal) []int64 {
	weights := make([]money.Decimal, len(groups))
	for g, gr := range groups {
		weights[g] = money.Zero
		for _, i := range gr.members {
			weights[g] = weights[g].Add(amount(i))
		}
	}
	if money.Sum(weights...).IsZero() {
		for g, gr := range groups {
			weights[g] = money.Zero
			for _, i := range gr.members {
				weights[g] = weights[g].Add(precise(i))
			}
		}
	}
	return Allocate(total, weights)
}
