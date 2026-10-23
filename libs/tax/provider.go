package tax

import (
	"github.com/tanvoxyz/tanvo/libs/money"
)

// specialTaxationTypes are provider tax types that rule a line untaxed.
var specialTaxationTypes = []string{"exempt", "notCollecting", "productNotTaxed", "jurisNotTaxed", "jurisHasNoTax"}

// ProviderBreakdownLine is one jurisdiction of a tax provider's response for
// a line, as returned.
type ProviderBreakdownLine struct {
	// Name is the jurisdiction's tax name ("GST/HST", "State"…).
	Name string
	// Reason explains a zero-tax ruling ("reverseCharge"…), if any.
	Reason string
	// Type is the provider's tax type ("tax", "exempt", "notCollecting"…).
	Type string
	// Rate is the rate as a fraction (0.1 for 10 %), nil when the provider
	// gave none.
	Rate *money.Decimal
	// TaxAmount is the exact tax in cents, possibly fractional.
	TaxAmount money.Decimal
}

// ProviderTax is one jurisdiction's tax on a line, normalized.
type ProviderTax struct {
	// Name is the display name; its parameterized form is the tax code.
	Name string
	// Type is the provider's tax type; it becomes the tax description.
	Type string
	// Rate is the rate as a fraction (0.1 for 10 %).
	Rate money.Decimal
	// TaxAmount is the exact tax in cents.
	TaxAmount money.Decimal
	// AllocatedAmountCents is the booked tax in cents when already
	// allocated (for a fee split from a grouped charge line), else nil.
	AllocatedAmountCents *int64
}

// ProviderTaxResult is a tax provider's result for one line.
type ProviderTaxResult struct {
	// ItemKey and ItemID identify the line (the fee, or the charge of a
	// grouped line); ItemCode is the provider's product mapping.
	ItemKey  string
	ItemID   string
	ItemCode string
	// AmountCents is the taxed amount of the line.
	AmountCents money.Decimal
	// TaxAmountCents is the provider's booked tax total for the line, nil
	// when not given.
	TaxAmountCents *int64
	// Breakdown are the line's taxes per jurisdiction.
	Breakdown []ProviderTax
}

// AllocatedAmounts are the booked cents of each jurisdiction: the
// allocations already made when every jurisdiction carries one, else the
// line's booked total (or the sum of the exact amounts) spread over the
// exact amounts with [Allocate].
func (r ProviderTaxResult) AllocatedAmounts() []int64 {
	all := true
	for _, t := range r.Breakdown {
		if t.AllocatedAmountCents == nil {
			all = false
			break
		}
	}
	out := make([]int64, len(r.Breakdown))
	if all {
		for i, t := range r.Breakdown {
			out[i] = *t.AllocatedAmountCents
		}
		return out
	}
	weights := make([]money.Decimal, len(r.Breakdown))
	for i, t := range r.Breakdown {
		weights[i] = t.TaxAmount
	}
	total := money.Sum(weights...)
	if r.TaxAmountCents != nil {
		total = money.NewFromInt(*r.TaxAmountCents)
	}
	return Allocate(total, weights)
}

// NormalizeProviderBreakdown maps a provider's breakdown for a line into
// [ProviderTax] values. Zero-tax rulings (exempt, not collecting, product or
// jurisdiction not taxed, jurisdiction without tax) and lines without a rate
// become a zero tax named after the reason or type ("Reverse charge"); a
// line without a type becomes "unknown_taxation". When the line books no tax
// (taxesToPay is zero) but a jurisdiction computed at least a cent, the
// seller pays it, and the jurisdiction becomes a zero "Tax".
func NormalizeProviderBreakdown(lines []ProviderBreakdownLine, taxesToPay int64) []ProviderTax {
	out := make([]ProviderTax, 0, len(lines))
	for _, b := range lines {
		switch {
		case isSpecialTaxationType(b.Type):
			out = append(out, ProviderTax{Name: humanize(underscore(cmpOr(b.Reason, b.Type))), Type: b.Type, Rate: money.Zero, TaxAmount: money.Zero})
		case b.Rate != nil:
			if taxesToPay == 0 && b.TaxAmount.GreaterThanOrEqual(money.One) {
				out = append(out, ProviderTax{Name: "Tax", Type: "tax", Rate: money.Zero, TaxAmount: money.Zero})
			} else {
				out = append(out, ProviderTax{Name: b.Name, Type: b.Type, Rate: *b.Rate, TaxAmount: b.TaxAmount})
			}
		default:
			typ := cmpOr(b.Type, "unknown_taxation")
			out = append(out, ProviderTax{Name: humanize(underscore(cmpOr(b.Reason, typ))), Type: typ, Rate: money.Zero, TaxAmount: money.Zero})
		}
	}
	return out
}

func isSpecialTaxationType(t string) bool {
	for _, s := range specialTaxationTypes {
		if s == t {
			return true
		}
	}
	return false
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// GroupFeesByCharge groups the fees of an invoice into provider lines: the
// fees of one charge (across subscriptions) form one line, placed where the
// charge first appears; every other fee is its own line. chargeIDs holds
// each fee's charge, or "" for fees other than charge fees. It returns the
// fee indexes of each line.
func GroupFeesByCharge(chargeIDs []string) [][]int {
	var out [][]int
	at := map[string]int{}
	for i, id := range chargeIDs {
		if id == "" {
			out = append(out, []int{i})
			continue
		}
		if g, ok := at[id]; ok {
			out[g] = append(out[g], i)
			continue
		}
		at[id] = len(out)
		out = append(out, []int{i})
	}
	return out
}

// ProviderItem is a fee of a grouped provider line.
type ProviderItem struct {
	// ItemKey and ItemID identify the fee.
	ItemKey string
	ItemID  string
	// Fee is the fee; its taxable amount weighs its share.
	Fee Fee
}

// SplitProviderTaxes splits the provider's result for a grouped charge line
// across the line's fees in proportion to their taxable amounts: each
// jurisdiction's exact amount with [AllocatePrecise] and its booked cents
// with [Allocate], so every jurisdiction's cents still add up to what the
// provider booked.
func SplitProviderTaxes(group ProviderTaxResult, items []ProviderItem) []ProviderTaxResult {
	weights := make([]money.Decimal, len(items))
	for i, it := range items {
		weights[i] = it.Fee.SubTotalExcludingTaxesAmountCents()
	}
	breakdowns := make([][]ProviderTax, len(items))
	for j, amount := range group.AllocatedAmounts() {
		t := group.Breakdown[j]
		precise := AllocatePrecise(t.TaxAmount, weights)
		booked := Allocate(money.NewFromInt(amount), weights)
		for i := range items {
			b := booked[i]
			breakdowns[i] = append(breakdowns[i], ProviderTax{
				Name: t.Name, Type: t.Type, Rate: t.Rate,
				TaxAmount: precise[i], AllocatedAmountCents: &b,
			})
		}
	}
	out := make([]ProviderTaxResult, len(items))
	for i, it := range items {
		var total int64
		for _, b := range breakdowns[i] {
			total += *b.AllocatedAmountCents
		}
		out[i] = ProviderTaxResult{
			ItemKey: it.ItemKey, ItemID: it.ItemID, ItemCode: group.ItemCode,
			AmountCents:    weights[i],
			TaxAmountCents: &total,
			Breakdown:      breakdowns[i],
		}
	}
	return out
}

// ApplyProviderFeeTaxes maps a provider's result for a fee into the fee's
// taxes: one applied tax per jurisdiction, coded by its parameterized name,
// described by its type, at its rate in percent, booking the allocated cents
// and keeping the exact amount. The fee's base rate is the taxed share the
// first jurisdiction implies (its exact tax over the tax at its full rate),
// 1 when it taxed the whole fee. A fee the provider did not report gets no
// taxes when it has no taxable amount, else [ErrFeeMissingFromTaxResponse].
func ApplyProviderFeeTaxes(fee Fee, r *ProviderTaxResult) (FeeTaxes, error) {
	out := FeeTaxes{TaxesPreciseAmountCents: money.Zero, TaxesRate: money.Zero, TaxesBaseRate: money.One}
	if r == nil {
		if !fee.Taxable() {
			return out, nil
		}
		return FeeTaxes{}, ErrFeeMissingFromTaxResponse
	}
	if len(r.Breakdown) > 0 {
		out.TaxesBaseRate = providerBaseRate(fee, r.Breakdown[0])
	}
	for i, amount := range r.AllocatedAmounts() {
		t := r.Breakdown[i]
		rate := t.Rate.Mul(money.Hundred)
		out.AppliedTaxes = append(out.AppliedTaxes, FeeAppliedTax{
			TaxCode:            parameterize(t.Name, "_"),
			TaxName:            t.Name,
			TaxDescription:     t.Type,
			TaxRate:            rate,
			Provider:           true,
			AmountCents:        amount,
			PreciseAmountCents: t.TaxAmount,
		})
		out.TaxesAmountCents += amount
		out.TaxesPreciseAmountCents = out.TaxesPreciseAmountCents.Add(t.TaxAmount)
		out.TaxesRate = out.TaxesRate.Add(rate)
	}
	return out, nil
}

func providerBaseRate(fee Fee, t ProviderTax) money.Decimal {
	expected := percentOf(fee.SubTotalExcludingTaxesAmountCents(), t.Rate.Mul(money.Hundred))
	if t.TaxAmount.LessThan(expected) && !expected.IsZero() {
		return t.TaxAmount.Div(expected)
	}
	return money.One
}

// ApplyProviderInvoiceTaxes sums the provider taxes of an invoice's fees
// (see [ApplyProviderFeeTaxes]) into invoice taxes: one per tax type, code
// and rate, in order of first appearance, keeping the cents booked on the
// fees. subTotal is the invoice's amount after coupons and progressive
// billing credits. The rate of each tax weighs by the share of the subtotal
// its fees make up or, when the subtotal is not positive, by the share of
// the taxed fees.
func ApplyProviderInvoiceTaxes(subTotal int64, fees []Fee) InvoiceTaxes {
	type entry struct {
		row    FeeAppliedTax
		amount int64
		fees   []int
	}
	var keys []string
	entries := map[string]*entry{}
	taxed := map[int]bool{}
	for i, f := range fees {
		for _, row := range f.AppliedTaxes {
			key := row.TaxDescription + "-" + parameterize(row.TaxName, "_") + "-" + rateKey(row.TaxRate)
			e, ok := entries[key]
			if !ok {
				e = &entry{row: row}
				entries[key] = e
				keys = append(keys, key)
			}
			e.amount += row.AmountCents
			e.fees = append(e.fees, i)
			taxed[i] = true
		}
	}
	out := InvoiceTaxes{TaxesRate: money.Zero}
	rate := money.Zero
	for _, key := range keys {
		e := entries[key]
		feesAmount, base := money.Zero, money.Zero
		for _, i := range e.fees {
			sub := fees[i].SubTotalExcludingTaxesAmountCents()
			feesAmount = feesAmount.Add(sub)
			base = base.Add(sub.Mul(fees[i].TaxesBaseRate))
		}
		out.AppliedTaxes = append(out.AppliedTaxes, InvoiceAppliedTax{
			TaxCode:                parameterize(e.row.TaxName, "_"),
			TaxName:                e.row.TaxName,
			TaxDescription:         e.row.TaxDescription,
			TaxRate:                e.row.TaxRate,
			Provider:               true,
			AmountCents:            e.amount,
			FeesAmountCents:        roundCents(feesAmount),
			TaxableBaseAmountCents: roundCents(base),
		})
		out.TaxesAmountCents += e.amount
		var share money.Decimal
		if subTotal > 0 {
			share = feesAmount.Div(money.NewFromInt(subTotal))
		} else {
			share = money.NewFromInt(int64(len(e.fees))).Div(money.NewFromInt(int64(len(taxed))))
		}
		rate = rate.Add(share.Mul(e.row.TaxRate))
	}
	out.TaxesRate = rate.Round(5, money.HalfUp)
	return out
}
