package tax

import (
	"cmp"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// FeeBookedTax is the share of an invoice's tax booked on one fee.
type FeeBookedTax struct {
	// AmountCents is the fee's share of the invoice's tax.
	AmountCents int64
	// RowAmountCents are the shares of the fee's applied taxes, in the
	// fee's order; empty when the fee has none, the fee then being booked
	// as a whole (UnitAmountCents).
	RowAmountCents []int64
	// UnitAmountCents is the fee's share when it has no applied taxes, from
	// the same split as RowAmountCents.
	UnitAmountCents int64
}

// BookTaxes splits what an invoice charged in tax across its fees and their
// applied taxes, so that crediting every fee in full credits exactly the
// invoice's tax.
//
// Fee taxes round line by line while the invoice rounds once, so the lines
// need not add up to the invoice. The split runs in two steps. Every applied
// tax of a fee (or the fee itself when it has none) is a unit; units are
// grouped by the invoice tax they resolve to ([Invoice.AppliedTaxFor]), the
// units resolving to none forming one last group. Each group books what its
// invoice tax charged (the unresolved group what its units booked); when the
// groups do not add up to the invoice's tax, the tax is spread over them in
// proportion to their exact amounts (else their booked amounts, else
// evenly). Each group's amount is then split over its units the same way.
//
// A fee's share ([FeeBookedTax.AmountCents]) is the sum of its units for an
// invoice with provider taxes; for internal taxes it is the invoice's tax
// split directly across the fees by their own tax amounts, credit notes
// crediting internal taxes at their rate.
func BookTaxes(inv Invoice) []FeeBookedTax {
	type unit struct {
		fee, row int // row is -1 for a fee booked as a whole
		booked   int64
		exact    money.Decimal
	}
	var units []unit
	for i, f := range inv.Fees {
		if len(f.AppliedTaxes) == 0 {
			units = append(units, unit{fee: i, row: -1, booked: f.TaxesAmountCents, exact: f.TaxesPreciseAmountCents})
			continue
		}
		for j, r := range f.AppliedTaxes {
			units = append(units, unit{fee: i, row: j, booked: r.AmountCents, exact: r.PreciseAmountCents})
		}
	}

	type group struct {
		tax   int // index into inv.AppliedTaxes, -1 for unresolved
		order int
		units []int
	}
	var groups []*group
	byTax := map[int]*group{}
	for u, un := range units {
		tax := -1
		if un.row >= 0 {
			if t, ok := inv.AppliedTaxFor(inv.Fees[un.fee].AppliedTaxes[un.row]); ok {
				tax = t
			}
		}
		g, ok := byTax[tax]
		if !ok {
			g = &group{tax: tax, order: len(groups)}
			byTax[tax] = g
			groups = append(groups, g)
		}
		g.units = append(g.units, u)
	}
	slices.SortStableFunc(groups, func(a, b *group) int {
		if (a.tax < 0) != (b.tax < 0) {
			if a.tax < 0 {
				return 1
			}
			return -1
		}
		if a.tax >= 0 {
			ta, tb := inv.AppliedTaxes[a.tax], inv.AppliedTaxes[b.tax]
			if c := cmp.Compare(ta.TaxCode, tb.TaxCode); c != 0 {
				return c
			}
			if c := ta.TaxRate.Cmp(tb.TaxRate); c != 0 {
				return c
			}
		}
		return cmp.Compare(a.order, b.order)
	})

	booked := make([]int64, len(groups))
	exact := make([]money.Decimal, len(groups))
	for i, g := range groups {
		exact[i] = money.Zero
		for _, u := range g.units {
			exact[i] = exact[i].Add(units[u].exact)
			if g.tax < 0 {
				booked[i] += units[u].booked
			}
		}
		if g.tax >= 0 {
			booked[i] = inv.AppliedTaxes[g.tax].AmountCents
		}
	}
	amounts := splitBooked(inv.TaxesAmountCents, booked, exact)

	out := make([]FeeBookedTax, len(inv.Fees))
	for i, f := range inv.Fees {
		if len(f.AppliedTaxes) > 0 {
			out[i].RowAmountCents = make([]int64, len(f.AppliedTaxes))
		}
	}
	for i, g := range groups {
		ub := make([]int64, len(g.units))
		ue := make([]money.Decimal, len(g.units))
		for k, u := range g.units {
			ub[k], ue[k] = units[u].booked, units[u].exact
		}
		for k, share := range splitBooked(amounts[i], ub, ue) {
			un := units[g.units[k]]
			if un.row < 0 {
				out[un.fee].UnitAmountCents = share
			} else {
				out[un.fee].RowAmountCents[un.row] = share
			}
		}
	}

	if inv.ProviderTaxes() {
		for i := range out {
			out[i].AmountCents = out[i].UnitAmountCents
			for _, r := range out[i].RowAmountCents {
				out[i].AmountCents += r
			}
		}
		return out
	}
	fb := make([]int64, len(inv.Fees))
	fe := make([]money.Decimal, len(inv.Fees))
	for i, f := range inv.Fees {
		fb[i], fe[i] = f.TaxesAmountCents, f.TaxesPreciseAmountCents
	}
	for i, share := range splitBooked(inv.TaxesAmountCents, fb, fe) {
		out[i].AmountCents = share
	}
	return out
}

// splitBooked keeps the booked amounts when they add up to total, else
// spreads total over the exact amounts, falling back to the booked amounts,
// then to equal weights, when those sum to zero.
func splitBooked(total int64, booked []int64, exact []money.Decimal) []int64 {
	var sum int64
	bw := make([]money.Decimal, len(booked))
	for i, b := range booked {
		sum += b
		bw[i] = money.NewFromInt(b)
	}
	if sum == total {
		return slices.Clone(booked)
	}
	weights := exact
	switch {
	case !money.Sum(exact...).IsZero():
	case sum != 0:
		weights = bw
	default:
		weights = make([]money.Decimal, len(booked))
		for i := range weights {
			weights[i] = money.One
		}
	}
	return Allocate(money.NewFromInt(total), weights)
}
