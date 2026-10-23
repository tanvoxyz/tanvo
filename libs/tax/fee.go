package tax

import "github.com/tanvoxyz/tanvo/libs/money"

// ApplyFeeTaxes computes the taxes of a fee from the taxes that apply to it
// (see [ResolveFeeTaxes]). Each tax is charged on the fee's amount less its
// coupons share: the booked amount from the rounded fee amount, the precise
// amount from the unrounded one. The fee total rounds the sum of the
// unrounded booked amounts once, so it may differ from the sum of the
// rounded lines. The base rate is 1.
func ApplyFeeTaxes(fee Fee, taxes []Tax) FeeTaxes {
	out := FeeTaxes{
		TaxesPreciseAmountCents: money.Zero,
		TaxesRate:               money.Zero,
		TaxesBaseRate:           money.One,
	}
	total := money.Zero
	base := fee.SubTotalExcludingTaxesAmountCents()
	preciseBase := fee.SubTotalExcludingTaxesPreciseAmountCents()
	for _, t := range taxes {
		amount := percentOf(base, t.Rate)
		precise := percentOf(preciseBase, t.Rate)
		out.AppliedTaxes = append(out.AppliedTaxes, FeeAppliedTax{
			TaxCode:            t.Code,
			TaxName:            t.Name,
			TaxDescription:     t.Description,
			TaxRate:            t.Rate,
			AmountCents:        roundCents(amount),
			PreciseAmountCents: precise,
		})
		total = total.Add(amount)
		out.TaxesPreciseAmountCents = out.TaxesPreciseAmountCents.Add(precise)
		out.TaxesRate = out.TaxesRate.Add(t.Rate)
	}
	out.TaxesAmountCents = roundCents(total)
	return out
}
