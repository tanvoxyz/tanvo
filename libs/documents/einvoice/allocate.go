package einvoice

import (
	"slices"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// allocate spreads total minor units across parts in proportion to
// weights so that the parts add up to total and each stays within one unit
// of its exact share. Each part starts from its exact share truncated
// toward zero; the remaining units go one at a time to the parts with the
// largest fractional remainders, ties to the earlier part. Negative totals
// and weights are allowed; a zero total or weights summing to zero give
// zeros.
func allocate(total int64, weights []money.Decimal) []int64 {
	out := make([]int64, len(weights))
	sum := money.Sum(weights...)
	if total == 0 || sum.IsZero() {
		return out
	}
	t := money.NewFromInt(total)
	exact := make([]money.Decimal, len(weights))
	var allocated int64
	for i, w := range weights {
		exact[i] = t.Mul(w).Div(sum)
		out[i], _ = exact[i].Truncate(0).Int64()
		allocated += out[i]
	}
	residue := total - allocated
	step := int64(1)
	if residue < 0 {
		step, residue = -1, -residue
	}
	keys := make([]money.Decimal, len(exact))
	order := make([]int, len(exact))
	for i, e := range exact {
		order[i] = i
		keys[i] = e.Sub(e.Truncate(0)).Mul(money.NewFromInt(-step))
	}
	slices.SortStableFunc(order, func(a, b int) int { return keys[a].Cmp(keys[b]) })
	for k := 0; int64(k) < residue && k < len(order); k++ {
		out[order[k]] += step
	}
	return out
}

// allocateByGroup is [allocate] weighted by the groups' booked amounts, or
// by their precise amounts when every group booked zero.
func allocateByGroup(total int64, booked, precise []money.Decimal) []int64 {
	if money.Sum(booked...).IsZero() {
		return allocate(total, precise)
	}
	return allocate(total, booked)
}
