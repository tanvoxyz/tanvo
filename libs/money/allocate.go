package money

import (
	"errors"
	"slices"
)

// Allocate splits total minor units across shares proportional to weights so
// that the parts sum exactly to total. Parts are rounded down first; the
// remaining units go one each to the parts with the largest remainders, ties
// to the earlier part. Negative totals are split by their magnitude and
// negated. Weights must be non-negative and not all zero.
func Allocate(total int64, weights []Decimal) ([]int64, error) {
	if len(weights) == 0 {
		return nil, errors.New("money: allocate across no weights")
	}
	sum := Zero
	for _, w := range weights {
		if w.IsNegative() {
			return nil, errors.New("money: negative allocation weight")
		}
		sum = sum.Add(w)
	}
	if sum.IsZero() {
		return nil, errors.New("money: allocation weights sum to zero")
	}
	negative := total < 0
	magnitude := total
	if negative {
		magnitude = -total
	}

	parts := make([]int64, len(weights))
	remainders := make([]Decimal, len(weights))
	allocated := int64(0)
	t := NewFromInt(magnitude)
	for i, w := range weights {
		exact := t.Mul(w).Div(sum)
		floor := exact.Round(0, Floor)
		n, _ := floor.Int64()
		parts[i] = n
		remainders[i] = exact.Sub(floor)
		allocated += n
	}

	order := make([]int, len(weights))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		return remainders[b].Cmp(remainders[a])
	})
	for i := int64(0); i < magnitude-allocated; i++ {
		parts[order[int(i)%len(order)]]++
	}

	if negative {
		for i := range parts {
			parts[i] = -parts[i]
		}
	}
	return parts, nil
}

// Prorate returns amount × numerator ÷ denominator, or zero when the
// denominator is zero, without intermediate rounding beyond
// [DivisionPrecision].
func Prorate(amount, numerator, denominator Decimal) Decimal {
	return amount.Mul(numerator).DivOrZero(denominator)
}
