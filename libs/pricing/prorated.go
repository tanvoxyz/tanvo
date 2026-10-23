package pricing

import (
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// proratedEvents returns the full and prorated value of each event.
func proratedEvents(c calc) (full, prorated []money.Decimal) {
	if c.agg.PerEvent == nil {
		return nil, nil
	}
	full = c.agg.PerEvent.Values
	prorated = c.agg.PerEvent.Prorated
	if len(full) < len(prorated) {
		prorated = prorated[:len(full)]
	}
	return full, prorated
}

// proratedOutput completes a prorated graduated amount: the unit amount is
// over the full units of the events, and there are no details.
func proratedOutput(c calc, amount money.Decimal) output {
	full, _ := proratedEvents(c)
	return output{
		amount:     amount,
		unitAmount: amount.DivOrZero(money.Sum(full...)),
		projected:  c.scaleToPeriod(amount),
	}
}

// proratedGraduated prices prorated graduated tiers by stepping through the
// events: the running full units select the tier, and the prorated units are
// what is priced in it. An event that straddles tiers overflows into the
// next one at its proration coefficient.
func proratedGraduated(c calc, p GraduatedProperties) (output, error) {
	ranges := p.Ranges
	if len(ranges) == 0 {
		return output{}, fmt.Errorf("%w: no graduated ranges", ErrInvalidProperties)
	}
	if c.units().IsZero() {
		return proratedOutput(c, flatTotal(ranges, money.Zero, money.Zero, money.Zero)), nil
	}

	full, prorated := proratedEvents(c)
	count := len(prorated)
	var (
		index       int
		overflow    = money.Zero
		fullSum     = money.Zero
		maxFullSum  = money.Zero
		proratedSum = money.Zero
		result      = money.Zero
		current     *Range
	)
	coefficient := func(i int) money.Decimal { return prorated[i].DivOrZero(full[i]) }

	// Each pass consumes an event or moves an overflow up one tier.
	limit := (count + 1) * (len(ranges) + 2)
	for iterations := 0; index < count || !overflow.IsZero(); iterations++ {
		if iterations > limit {
			return output{}, fmt.Errorf("%w: prorated graduated ranges do not converge", ErrInvalidProperties)
		}
		var next *money.Decimal
		if index < len(full) {
			next = &full[index]
		}
		current = proratedRange(ranges, fullSum, overflow, next)

		// The overflow of the previous event goes into this tier, or
		// through it when it covers the whole tier.
		if !overflow.IsZero() {
			coef := coefficient(index - 1)
			proratedSum = proratedSum.Add(overflow.Mul(coef))
			if current.To != nil && fullSum.GreaterThanOrEqual(*current.To) {
				overflow = fullSum.Sub(*current.To)
				proratedSum = proratedSum.Sub(overflow.Mul(coef))
				result = result.Add(proratedSum.Mul(current.PerUnitAmount))
				proratedSum = money.Zero
				continue
			}
			overflow = money.Zero
		}

		if index >= count {
			break
		}
		// An addition worth nothing in the period (removed before it) does
		// not move the tiers, nor does the removal of such an addition,
		// which would take the usage below zero.
		if prorated[index].IsZero() && full[index].IsPositive() {
			index++
			continue
		}
		if prorated[index].IsZero() && full[index].IsNegative() && fullSum.Add(full[index]).IsNegative() {
			index++
			continue
		}

		fullSum = fullSum.Add(full[index])
		maxFullSum = money.Max(maxFullSum, fullSum)
		proratedSum = proratedSum.Add(prorated[index])
		index++

		if skipOverflow(fullSum, *current) {
			continue
		}
		overflow = proratedOverflow(fullSum, *current)
		proratedSum = proratedSum.Sub(overflow.Mul(coefficient(index - 1)))
		result = result.Add(proratedSum.Mul(current.PerUnitAmount))
		proratedSum = money.Zero
	}
	// The units left are in the highest tier reached.
	if current != nil {
		result = result.Add(proratedSum.Mul(current.PerUnitAmount))
	}
	return proratedOutput(c, flatTotal(ranges, result, fullSum, maxFullSum)), nil
}

// proratedRange selects the tier of fullSum units, given the overflow being
// carried and the next event's full value.
func proratedRange(ranges []Range, fullSum, overflow money.Decimal, next *money.Decimal) *Range {
	if !fullSum.IsPositive() {
		return &ranges[0]
	}
	units := fullSum
	switch {
	case overflow.IsPositive():
		units = fullSum.Sub(overflow).Add(money.One)
	case overflow.IsNegative():
		units = fullSum.Add(overflow)
	}
	for i := range ranges {
		r := &ranges[i]
		if r.To != nil && units.Equal(*r.To) {
			if next != nil && next.IsPositive() && i+1 < len(ranges) {
				return &ranges[i+1]
			}
			return r
		}
		if units.GreaterThanOrEqual(r.From) && (r.To == nil || units.LessThan(*r.To)) {
			return r
		}
	}
	return &ranges[0]
}

// skipOverflow reports whether fullSum is still inside tier r.
func skipOverflow(fullSum money.Decimal, r Range) bool {
	if r.To == nil {
		return fullSum.GreaterThanOrEqual(r.From.Sub(money.One))
	}
	return fullSum.LessThan(*r.To) && fullSum.GreaterThanOrEqual(r.From)
}

// proratedOverflow returns the units of fullSum beyond tier r.
func proratedOverflow(fullSum money.Decimal, r Range) money.Decimal {
	if r.To != nil && fullSum.GreaterThanOrEqual(*r.To) {
		return fullSum.Sub(*r.To)
	}
	return fullSum.Sub(r.From).Add(money.One)
}

// flatTotal adds to result (floored at 0) the flat amounts of the tiers up
// to the one holding the maximum usage; it is 0 when the final usage is
// negative.
func flatTotal(ranges []Range, result, totalFull, maxFull money.Decimal) money.Decimal {
	if totalFull.IsNegative() {
		return money.Zero
	}
	result = money.Max(result, money.Zero)
	flat := money.Zero
	for _, r := range ranges {
		flat = flat.Add(r.FlatAmount)
		if r.To == nil || maxFull.LessThanOrEqual(*r.To) {
			break
		}
	}
	return result.Add(flat)
}

// proratedAdjacentGraduated prices prorated graduated tiers that touch (the
// shape of catalog rates): each event fills the tiers between the usage
// before and after it, upward for an addition and from the top for a
// removal, and each tier's share is priced at the event's proration.
// Nothing steps by whole units, so bounds can be decimals.
func proratedAdjacentGraduated(c calc, p GraduatedProperties) (output, error) {
	ranges := p.Ranges
	if len(ranges) == 0 {
		return output{}, fmt.Errorf("%w: no graduated ranges", ErrInvalidProperties)
	}
	// Catalog tiers stored before they were defined by their upper bound
	// still step by one unit.
	if len(ranges) >= 2 && !Adjacent(ranges) {
		return proratedGraduated(c, p)
	}
	if c.units().IsZero() {
		return proratedOutput(c, flatTotal(ranges, money.Zero, money.Zero, money.Zero)), nil
	}

	full, prorated := proratedEvents(c)
	fullSum, maxFullSum, amount := money.Zero, money.Zero, money.Zero
	for i, pv := range prorated {
		f := full[i]
		// The events the step calculator leaves out too.
		if f.IsZero() || (pv.IsZero() && (f.IsPositive() || fullSum.Add(f).IsNegative())) {
			continue
		}
		amount = amount.Add(pv.Mul(tierCost(ranges, fullSum, fullSum.Add(f))).Div(f))
		fullSum = fullSum.Add(f)
		maxFullSum = money.Max(maxFullSum, fullSum)
	}
	return proratedOutput(c, flatTotal(ranges, amount, fullSum, maxFullSum)), nil
}

// tierCost is the price of the usage between two totals: positive when
// usage rises, negative when it falls. Usage below zero is in no tier.
func tierCost(ranges []Range, before, after money.Decimal) money.Decimal {
	low, high := money.Min(before, after), money.Max(before, after)
	cost := money.Zero
	for _, r := range ranges {
		upper := high
		if r.To != nil {
			upper = money.Min(high, *r.To)
		}
		if covered := upper.Sub(money.Max(low, r.From)); covered.IsPositive() {
			cost = cost.Add(covered.Mul(r.PerUnitAmount))
		}
	}
	if after.LessThan(before) {
		return cost.Neg()
	}
	return cost
}
