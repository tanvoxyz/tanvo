package alert

import (
	"slices"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// MaxRecurringCrossings bounds the recurring values one evaluation
// reports, so a tiny step and a large jump cannot exhaust memory; the
// values nearest the current value are kept.
const MaxRecurringCrossings = 1000

// Crossed returns the sorted threshold values crossed when the watched
// value moves from previous to current in the alert's direction (lago
// find_thresholds_crossed). Increasing: one-time thresholds in
// (previous, current], then every multiple of the recurring step beyond
// the last one-time threshold (or 0) up to current. Decreasing mirrors
// it below the first one-time threshold. A move against the direction
// crosses nothing.
func Crossed(a *Alert, previous, current money.Decimal) []money.Decimal {
	oneTime := a.OneTimeValues()
	var step *money.Decimal
	if r, ok := a.RecurringThreshold(); ok && r.Value.IsPositive() {
		step = &r.Value
	}
	var crossed []money.Decimal
	if a.Direction == DirectionDecreasing {
		if current.GreaterThanOrEqual(previous) {
			return nil
		}
		initial := money.Decimal{}
		if len(oneTime) > 0 {
			if current.GreaterThan(oneTime[len(oneTime)-1]) {
				return nil
			}
			if previous.GreaterThan(oneTime[0]) {
				for _, v := range oneTime {
					if v.LessThan(previous) && v.GreaterThanOrEqual(current) {
						crossed = append(crossed, v)
					}
				}
			}
			initial = oneTime[0]
		}
		crossed = append(crossed, recurringDecreasing(previous, current, step, initial)...)
	} else {
		if current.LessThanOrEqual(previous) {
			return nil
		}
		initial := money.Decimal{}
		if len(oneTime) > 0 {
			if current.LessThan(oneTime[0]) {
				return nil
			}
			if previous.LessThan(oneTime[len(oneTime)-1]) {
				for _, v := range oneTime {
					if v.GreaterThan(previous) && v.LessThanOrEqual(current) {
						crossed = append(crossed, v)
					}
				}
			}
			initial = oneTime[len(oneTime)-1]
		}
		crossed = append(crossed, recurringIncreasing(previous, current, step, initial)...)
	}
	slices.SortFunc(crossed, money.Decimal.Cmp)
	return slices.CompactFunc(crossed, money.Decimal.Equal)
}

func recurringIncreasing(previous, current money.Decimal, step *money.Decimal, initial money.Decimal) []money.Decimal {
	if step == nil {
		return nil
	}
	prevSteps := previous.Sub(initial).Div(*step).Round(0, money.Ceiling)
	prevSteps = money.Max(prevSteps, money.NewFromInt(1))
	from := initial.Add(prevSteps.Mul(*step))
	curSteps := current.Sub(initial).Div(*step).Round(0, money.Floor)
	to := initial.Add(curSteps.Mul(*step))
	if from.GreaterThan(to) {
		return nil
	}
	if n := to.Sub(from).Div(*step); n.GreaterThanOrEqual(money.NewFromInt(MaxRecurringCrossings)) {
		from = to.Sub(step.Mul(money.NewFromInt(MaxRecurringCrossings - 1)))
	}
	var out []money.Decimal
	for v := from; v.LessThanOrEqual(to); v = v.Add(*step) {
		out = append(out, v)
	}
	return out
}

func recurringDecreasing(previous, current money.Decimal, step *money.Decimal, initial money.Decimal) []money.Decimal {
	if step == nil {
		return nil
	}
	prevSteps := initial.Sub(previous).Div(*step).Round(0, money.Ceiling)
	prevSteps = money.Max(prevSteps, money.NewFromInt(1))
	top := initial.Sub(prevSteps.Mul(*step))
	curSteps := initial.Sub(current).Div(*step).Round(0, money.Floor)
	bottom := initial.Sub(curSteps.Mul(*step))
	if top.LessThan(bottom) {
		return nil
	}
	if n := top.Sub(bottom).Div(*step); n.GreaterThanOrEqual(money.NewFromInt(MaxRecurringCrossings)) {
		top = bottom.Add(step.Mul(money.NewFromInt(MaxRecurringCrossings - 1)))
	}
	var out []money.Decimal
	for v := bottom; v.LessThanOrEqual(top); v = v.Add(*step) {
		out = append(out, v)
	}
	return out
}
