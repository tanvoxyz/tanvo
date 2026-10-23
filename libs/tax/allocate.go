package tax

import (
	"slices"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// Allocate spreads total, rounded half up to whole cents, across parts in
// proportion to weights, so that the parts add up to the rounded total and
// each stays within a cent of its exact share. Each part starts from its
// exact share truncated toward zero; the remaining cents go one at a time to
// the parts with the largest fractional remainders, ties to the earlier
// part. Negative totals and weights are allowed. A zero total or weights
// summing to zero give zeros.
//
// Rounding each share on its own would not add up to the total: equal shares
// all round the same way, so the error grows with their number.
func Allocate(total money.Decimal, weights []money.Decimal) []int64 {
	out := make([]int64, len(weights))
	exact := AllocatePrecise(total, weights)
	if total.IsZero() || money.Sum(weights...).IsZero() {
		return out
	}
	var allocated int64
	for i, e := range exact {
		out[i], _ = e.Truncate(0).Int64()
		allocated += out[i]
	}
	residue := roundCents(total) - allocated
	step := int64(1)
	if residue < 0 {
		step, residue = -1, -residue
	}
	order := make([]int, len(exact))
	keys := make([]money.Decimal, len(exact))
	for i, e := range exact {
		order[i] = i
		keys[i] = e.Sub(e.Truncate(0)).Mul(money.NewFromInt(-step))
	}
	slices.SortStableFunc(order, func(a, b int) int { return keys[a].Cmp(keys[b]) })
	for k := int64(0); k < residue && int(k) < len(order); k++ {
		out[order[k]] += step
	}
	return out
}

// AllocatePrecise returns the exact shares of total in proportion to
// weights, unrounded. A zero total or weights summing to zero give zeros.
func AllocatePrecise(total money.Decimal, weights []money.Decimal) []money.Decimal {
	out := make([]money.Decimal, len(weights))
	sum := money.Sum(weights...)
	for i, w := range weights {
		if total.IsZero() || sum.IsZero() {
			out[i] = money.Zero
			continue
		}
		out[i] = total.Mul(w).Div(sum)
	}
	return out
}

// AllocationGroup is a group of lines whose booked and exact amounts weigh a
// group in [AllocateByGroup].
type AllocationGroup struct {
	// AmountCents are the lines' booked amounts.
	AmountCents []int64
	// PreciseAmountCents are the lines' exact amounts.
	PreciseAmountCents []money.Decimal
}

func (g AllocationGroup) booked() money.Decimal {
	var s int64
	for _, a := range g.AmountCents {
		s += a
	}
	return money.NewFromInt(s)
}

func (g AllocationGroup) precise() money.Decimal {
	return money.Sum(g.PreciseAmountCents...)
}

// AllocateByGroup spreads total across groups in proportion to their booked
// amounts, keeping the booked proportions; only when every group booked
// zero does it weigh them by their exact amounts.
func AllocateByGroup(total money.Decimal, groups []AllocationGroup) []int64 {
	weights := make([]money.Decimal, len(groups))
	for i, g := range groups {
		weights[i] = g.booked()
	}
	if money.Sum(weights...).IsZero() {
		for i, g := range groups {
			weights[i] = g.precise()
		}
	}
	return Allocate(total, weights)
}
