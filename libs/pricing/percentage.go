package pricing

import "github.com/tanvoxyz/tanvo/libs/money"

// percentage charges a rate of the paid units, plus a fixed fee per paid
// event. With per-transaction limits, each event is priced and clamped on
// its own.
func percentage(c calc, p PercentageProperties) output {
	pc := percentageCalc{c: c, p: p}
	pctAmount := pc.percentageAmount()
	fixedAmount := pc.fixedAmount()

	amount := pctAmount.Add(fixedAmount)
	adjustment := money.Zero
	if p.hasMinMax() {
		minMax := pc.minMaxAmount()
		adjustment = minMax.Sub(pctAmount).Sub(fixedAmount)
		amount = minMax
	}

	freeUnits := pc.freeUnitsValue()
	paidUnits := money.Max(c.units().Sub(freeUnits), money.Zero)
	freeEvents := min(c.agg.Count, pc.freeUnitsCount())
	paidEvents := c.agg.Count - freeEvents
	fixedUnit := money.Zero
	if paidEvents > 0 {
		fixedUnit = orZero(p.FixedAmount)
	}

	return output{
		amount:     amount,
		unitAmount: c.unitAmountOver(amount),
		projected:  c.scaleToPeriod(amount),
		details: PercentageDetails{
			Units:                       c.units(),
			FreeUnits:                   freeUnits,
			FreeEvents:                  freeEvents,
			PaidUnits:                   paidUnits,
			Rate:                        p.Rate,
			PerUnitTotalAmount:          pctAmount,
			PaidEvents:                  paidEvents,
			FixedFeeUnitAmount:          fixedUnit,
			FixedFeeTotalAmount:         fixedAmount,
			MinMaxAdjustmentTotalAmount: adjustment,
		},
	}
}

type percentageCalc struct {
	c calc
	p PercentageProperties
}

func (pc percentageCalc) freeTotal() money.Decimal {
	return orZero(pc.p.FreeUnitsPerTotalAggregation)
}

func (pc percentageCalc) lastRunningTotal() money.Decimal {
	rt := pc.c.agg.RunningTotal
	if len(rt) == 0 {
		return money.Zero
	}
	return rt[len(rt)-1]
}

// freeUnitsValue is the free part of the aggregated amount: the running
// total after the free events, or the free amount, whichever ends first.
func (pc percentageCalc) freeUnitsValue() money.Decimal {
	last := pc.lastRunningTotal()
	if last.IsZero() {
		return money.Zero
	}
	rt := pc.c.agg.RunningTotal
	if events := pc.p.FreeUnitsPerEvents; events > 0 && events < int64(len(rt)) {
		return rt[events-1]
	}
	freeTotal := pc.freeTotal()
	if freeTotal.IsZero() || last.LessThanOrEqual(freeTotal) {
		return last
	}
	return freeTotal
}

// freeUnitsCount is the number of free events: the smallest non-zero of the
// free events and the number of events within the free amount.
func (pc percentageCalc) freeUnitsCount() int64 {
	freeTotal := pc.freeTotal()
	var within int64
	for _, v := range pc.c.agg.RunningTotal {
		if v.LessThan(freeTotal) {
			within++
		}
	}
	var count int64
	for _, n := range []int64{pc.p.FreeUnitsPerEvents, within} {
		if n != 0 && (count == 0 || n < count) {
			count = n
		}
	}
	return count
}

func (pc percentageCalc) percentageAmount() money.Decimal {
	free := pc.freeUnitsValue()
	if free.GreaterThan(pc.c.units()) {
		return money.Zero
	}
	return pc.c.units().Sub(free).Mul(pc.p.Rate).Div(money.Hundred)
}

func (pc percentageCalc) fixedAmount() money.Decimal {
	freeCount := pc.freeUnitsCount()
	if pc.c.units().IsZero() || pc.p.FixedAmount == nil || freeCount >= pc.c.agg.Count {
		return money.Zero
	}
	return money.NewFromInt(pc.c.agg.Count - freeCount).Mul(*pc.p.FixedAmount)
}

// minMaxAmount prices each event on its own: free events and the free
// amount first, then the rate and the fixed fee, clamped to the limits.
func (pc percentageCalc) minMaxAmount() money.Decimal {
	remainingEvents := pc.p.FreeUnitsPerEvents
	remainingAmount := pc.freeTotal()
	fixed := orZero(pc.p.FixedAmount)
	total := money.Zero
	for _, value := range pc.c.agg.eventValues() {
		if remainingEvents > 0 || remainingAmount.IsPositive() {
			remainingEvents--
			if !remainingAmount.IsPositive() {
				continue
			}
			if remainingAmount.GreaterThan(value) {
				remainingAmount = remainingAmount.Sub(value)
				continue
			}
			value = value.Sub(remainingAmount)
			remainingAmount = money.Zero
			remainingEvents = 0
		}
		amount := value.Mul(pc.p.Rate).Div(money.Hundred).Add(fixed)
		total = total.Add(pc.clamp(amount))
	}
	return total
}

// clamp applies the per-transaction limits to amount.
func (pc percentageCalc) clamp(amount money.Decimal) money.Decimal {
	if lo := pc.p.PerTransactionMinAmount; lo != nil && amount.LessThan(*lo) {
		return *lo
	}
	if hi := pc.p.PerTransactionMaxAmount; hi != nil && amount.GreaterThan(*hi) {
		return *hi
	}
	return amount
}
