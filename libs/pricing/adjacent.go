package pricing

import "github.com/tanvoxyz/tanvo/libs/money"

// bounds is a tier's lower and upper bounds.
type bounds struct {
	from money.Decimal
	to   *money.Decimal
}

// Adjacent reports whether ranges touch: there are at least two and each
// starts where the previous one ends (a missing upper bound counting as 0).
// Units in adjacent tiers are counted half-open instead of with a one-unit
// step between tiers.
func Adjacent(ranges []Range) bool {
	b := make([]bounds, len(ranges))
	for i, r := range ranges {
		b[i] = bounds{r.From, r.To}
	}
	return adjacent(b)
}

// AdjacentPercentage is [Adjacent] for percentage tiers.
func AdjacentPercentage(ranges []PercentageRange) bool {
	b := make([]bounds, len(ranges))
	for i, r := range ranges {
		b[i] = bounds{r.From, r.To}
	}
	return adjacent(b)
}

func adjacent(ranges []bounds) bool {
	if len(ranges) < 2 {
		return false
	}
	for i := 1; i < len(ranges); i++ {
		if !ranges[i].from.Equal(orZero(ranges[i-1].to)) {
			return false
		}
	}
	return true
}

// orZero returns *d, or 0 when d is nil.
func orZero(d *money.Decimal) money.Decimal {
	if d == nil {
		return money.Zero
	}
	return *d
}

// ptr returns a pointer to a copy of d.
func ptr(d money.Decimal) *money.Decimal { return &d }
