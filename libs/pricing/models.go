package pricing

import (
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// standard prices every unit at the same amount.
func standard(c calc, p StandardProperties) output {
	amount := c.units().Mul(p.Amount)
	return output{
		amount:     amount,
		unitAmount: c.unitAmountOver(amount),
		projected:  c.projectedUnits().Mul(p.Amount),
	}
}

// ceilDiv returns ⌈a / b⌉ for b > 0.
func ceilDiv(a, b money.Decimal) money.Decimal {
	return a.Div(b).Round(0, money.Ceiling)
}

// packageModel bills each started package of units after the free units.
func packageModel(c calc, p PackageProperties) (output, error) {
	if p.PackageSize <= 0 {
		return output{}, fmt.Errorf("%w: package size %d", ErrInvalidProperties, p.PackageSize)
	}
	size := money.NewFromInt(p.PackageSize)
	free := money.NewFromInt(p.FreeUnits)
	paid := c.units().Sub(free)

	var out output
	if !paid.IsNegative() {
		out.amount = ceilDiv(paid, size).Mul(p.Amount)
	}
	if paid.IsPositive() {
		out.unitAmount = out.amount.Div(paid)
	}

	switch {
	case c.units().IsZero():
		out.details = PackageDetails{}
	case paid.IsNegative():
		out.details = PackageDetails{FreeUnits: free, PerPackageSize: p.PackageSize, PerPackageUnitAmount: p.Amount}
	default:
		out.details = PackageDetails{FreeUnits: free, PaidUnits: paid, PerPackageSize: p.PackageSize, PerPackageUnitAmount: p.Amount}
	}

	if projected := c.projectedUnits().Sub(free); projected.IsPositive() {
		out.projected = ceilDiv(projected, size).Mul(p.Amount)
	}
	return out, nil
}

// dynamic prices the amounts the events carry.
func dynamic(c calc) output {
	var out output
	if c.agg.fullOrUnits().IsZero() {
		return out
	}
	out.amount = c.s.Currency.MinorToUnits(orZero(c.agg.PreciseTotalAmountCents))
	out.unitAmount = c.unitAmountOver(out.amount)
	out.projected = c.scaleToPeriod(out.amount)
	return out
}

// graduated prices each unit at the tier it falls in.
func graduated(c calc, p GraduatedProperties) (output, error) {
	if len(p.Ranges) == 0 {
		return output{}, fmt.Errorf("%w: no graduated ranges", ErrInvalidProperties)
	}
	tierUnits := c.units()
	ratio := money.One
	if c.s.proratedProductCatalog() {
		tierUnits = c.agg.fullOrUnits()
		if !tierUnits.IsZero() {
			ratio = c.units().Div(tierUnits)
		}
	}
	isAdjacent := Adjacent(p.Ranges)

	details := GraduatedDetails{Ranges: []GraduatedRangeDetails{}}
	total := money.Zero
	for _, r := range p.Ranges {
		d := graduatedRangeDetails(r, tierUnits, isAdjacent, ratio)
		details.Ranges = append(details.Ranges, d)
		total = total.Add(d.TotalWithFlatAmount)
		if r.To == nil || r.To.GreaterThanOrEqual(tierUnits) {
			break
		}
	}

	out := output{details: details, amount: total}
	// The first pay-in-advance event is priced as cost(1) - cost(0) without
	// the flat amount of cost(0).
	if c.units().IsZero() && c.excludeEvent {
		out.amount = money.Zero
	}
	out.unitAmount = c.unitAmountOver(out.amount)
	out.projected = graduatedProjection(c.projectedUnits(), p.Ranges)
	return out, nil
}

// graduatedRangeDetails computes the share of tier r in totalUnits units.
func graduatedRangeDetails(r Range, totalUnits money.Decimal, isAdjacent bool, ratio money.Decimal) GraduatedRangeDetails {
	effective := totalUnits
	if r.To != nil && totalUnits.GreaterThanOrEqual(*r.To) {
		effective = *r.To
	}
	var units money.Decimal
	if r.From.IsZero() {
		units = effective.Mul(ratio)
	} else {
		diff := effective.Sub(r.From)
		if !isAdjacent {
			diff = diff.Add(money.One)
		}
		units = diff.Mul(ratio)
	}
	perUnit := r.PerUnitAmount
	if units.IsZero() {
		perUnit = money.Zero
	}
	perUnitTotal := units.Mul(perUnit)
	return GraduatedRangeDetails{
		From:                r.From,
		To:                  r.To,
		FlatUnitAmount:      r.FlatAmount,
		PerUnitAmount:       perUnit,
		Units:               units,
		PerUnitTotalAmount:  perUnitTotal,
		TotalWithFlatAmount: perUnitTotal.Add(r.FlatAmount),
	}
}

// graduatedProjection fills the tiers by capacity with the projected units.
func graduatedProjection(projected money.Decimal, ranges []Range) money.Decimal {
	total := money.Zero
	if projected.IsZero() {
		return total
	}
	remaining := projected
	priced := money.Zero
	for _, r := range ranges {
		inTier := remaining
		if r.To != nil {
			inTier = money.Min(remaining, r.To.Sub(priced))
		}
		if inTier.IsPositive() {
			total = total.Add(inTier.Mul(r.PerUnitAmount)).Add(r.FlatAmount)
			remaining = remaining.Sub(inTier)
			priced = priced.Add(inTier)
		}
		if !remaining.IsPositive() {
			break
		}
	}
	return total
}

// graduatedPercentage applies each tier's rate to the units in it.
func graduatedPercentage(c calc, p GraduatedPercentageProperties) (output, error) {
	if len(p.Ranges) == 0 {
		return output{}, fmt.Errorf("%w: no graduated percentage ranges", ErrInvalidProperties)
	}
	units := c.units()
	// Charges keep counting touching tiers with a one-unit step; only
	// catalog rates count them half-open.
	halfOpen := c.s.ProductCatalog && AdjacentPercentage(p.Ranges)

	details := GraduatedPercentageDetails{Ranges: []GraduatedPercentageRangeDetails{}}
	total := money.Zero
	for _, r := range p.Ranges {
		d := graduatedPercentageRangeDetails(r, units, halfOpen)
		if units.IsZero() && c.excludeEvent {
			d.FlatUnitAmount = money.Zero
			d.TotalWithFlatAmount = money.Zero
		}
		details.Ranges = append(details.Ranges, d)
		total = total.Add(d.TotalWithFlatAmount)
		if r.To == nil || r.To.GreaterThanOrEqual(units) {
			break
		}
	}
	out := output{amount: total, details: details}
	out.unitAmount = c.unitAmountOver(total)
	out.projected = c.scaleToPeriod(total)
	return out, nil
}

// graduatedPercentageRangeDetails computes the share of tier r.
func graduatedPercentageRangeDetails(r PercentageRange, totalUnits money.Decimal, halfOpen bool) GraduatedPercentageRangeDetails {
	var units money.Decimal
	switch {
	case halfOpen:
		upper := totalUnits
		if r.To != nil && totalUnits.GreaterThanOrEqual(*r.To) {
			upper = *r.To
		}
		units = upper.Sub(r.From)
	case r.To != nil && totalUnits.GreaterThanOrEqual(*r.To):
		lower := r.From
		if lower.IsZero() {
			lower = money.One
		}
		units = r.To.Sub(lower).Add(money.One)
	case r.From.IsZero():
		units = totalUnits
	default:
		units = totalUnits.Sub(r.From).Add(money.One)
	}
	perUnitTotal := units.Mul(r.Rate).Div(money.Hundred)
	return GraduatedPercentageRangeDetails{
		From:                r.From,
		To:                  r.To,
		FlatUnitAmount:      r.FlatAmount,
		Rate:                r.Rate,
		Units:               units,
		PerUnitTotalAmount:  perUnitTotal,
		TotalWithFlatAmount: perUnitTotal.Add(r.FlatAmount),
	}
}

// volume prices every unit at the tier the total falls in.
func volume(c calc, p VolumeProperties) (output, error) {
	if len(p.Ranges) == 0 {
		return output{}, fmt.Errorf("%w: no volume ranges", ErrInvalidProperties)
	}
	ranges := slices.Clone(p.Ranges)
	slices.SortStableFunc(ranges, func(a, b Range) int { return a.From.Cmp(b.From) })

	n := c.units()
	if c.s.Prorated && c.agg.FullUnits != nil {
		n = *c.agg.FullUnits
	}

	var flat, perUnit, perUnitTotal money.Decimal
	// A negative total (refunds) falls below the first tier: no tier
	// applies and nothing is charged.
	if r, ok := volumeRange(ranges, n); ok {
		flat = r.FlatAmount
		perUnitTotal = c.units().Mul(r.PerUnitAmount)
		perUnit = perUnitTotal.DivOrZero(n)
	}
	amount := perUnitTotal.Add(flat)
	out := output{
		amount:     amount,
		unitAmount: amount.DivOrZero(n),
		details:    VolumeDetails{FlatUnitAmount: flat, PerUnitAmount: perUnit, PerUnitTotalAmount: perUnitTotal},
	}
	if projected := c.projectedUnits(); !projected.IsZero() {
		if r, ok := volumeRange(ranges, projected); ok {
			out.projected = projected.Mul(r.PerUnitAmount).Add(r.FlatAmount)
		}
	}
	return out, nil
}

// volumeRange finds the tier of n units: the first with From ≤ ⌈n⌉ and n ≤
// To.
func volumeRange(ranges []Range, n money.Decimal) (Range, bool) {
	ceil := n.Round(0, money.Ceiling)
	for _, r := range ranges {
		if r.From.LessThanOrEqual(ceil) && (r.To == nil || n.LessThanOrEqual(*r.To)) {
			return r, true
		}
	}
	return Range{}, false
}
