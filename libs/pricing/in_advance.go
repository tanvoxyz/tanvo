package pricing

import (
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// InAdvanceEvent is the event a pay-in-advance fee is billed for.
type InAdvanceEvent struct {
	// Persisted is set when the aggregation already includes the event;
	// it is then priced as total minus total without it, otherwise as
	// total with it minus total.
	Persisted bool
	// Units is the event's contribution to the aggregation.
	Units money.Decimal
	// PreciseTotalAmountCents is the event's amount in minor units, for the
	// dynamic model.
	PreciseTotalAmountCents money.Decimal
	// Value is the event's per-event value, appended to the aggregation's
	// per-event values when the event is not persisted.
	Value money.Decimal
	// ValuesWithoutEvent are the aggregation's per-event values without
	// this event, used when the event is persisted. They matter only for
	// the percentage model with per-transaction limits.
	ValuesWithoutEvent []money.Decimal
}

// InAdvanceResult is the price of one pay-in-advance event.
type InAdvanceResult struct {
	// Units is the number of units billed for the event.
	Units money.Decimal
	// Count is the number of events billed: 1.
	Count int64
	// AmountCents is the amount in minor units, rounded half up to the
	// currency's exponent.
	AmountCents money.Decimal
	// PreciseAmountCents is the unrounded amount in minor units.
	PreciseAmountCents money.Decimal
	// UnitAmount is the rounded amount (in units) per unit billed.
	UnitAmount money.Decimal
	// Details is the difference between the details with and without the
	// event, for the percentage and graduated percentage models of a
	// persisted event; nil otherwise.
	Details Details
}

// ApplyInAdvance prices a pay-in-advance event. agg is the aggregation of
// the period's events, including the event when ev.Persisted. The volume
// model, which reprices every unit when a tier changes, cannot be billed in
// advance, and prorated graduated charges are priced without proration.
func ApplyInAdvance(s PricingStructure, agg AggregationResult, ev InAdvanceEvent) (InAdvanceResult, error) {
	if err := s.check(); err != nil {
		return InAdvanceResult{}, err
	}
	if s.Properties.Model() == Volume {
		return InAdvanceResult{}, fmt.Errorf("%w: %s charges cannot be paid in advance", ErrUnknownModel, Volume)
	}
	// The derived aggregation keeps only the units, count, amounts and
	// running total.
	derived := AggregationResult{RunningTotal: agg.RunningTotal}
	var with, without AggregationResult
	if ev.Persisted {
		with = agg
		without = derived
		without.Units = agg.Units.Sub(ev.Units)
		without.Count = agg.Count - 1
		if agg.PreciseTotalAmountCents != nil {
			without.PreciseTotalAmountCents = ptr(agg.PreciseTotalAmountCents.Sub(ev.PreciseTotalAmountCents))
		}
		without.PerEvent = &PerEventAggregation{Values: ev.ValuesWithoutEvent}
	} else {
		without = agg
		with = derived
		with.Units = agg.Units.Add(ev.Units)
		with.Count = agg.Count + 1
		if agg.PreciseTotalAmountCents != nil {
			with.PreciseTotalAmountCents = ptr(agg.PreciseTotalAmountCents.Add(ev.PreciseTotalAmountCents))
		}
		values := append(append([]money.Decimal{}, agg.eventValues()...), ev.Value)
		with.PerEvent = &PerEventAggregation{Values: values}
	}

	withOut, err := compute(calc{s: s, agg: with, inAdvance: true})
	if err != nil {
		return InAdvanceResult{}, err
	}
	withoutOut, err := compute(calc{s: s, agg: without, inAdvance: true, excludeEvent: ev.Persisted})
	if err != nil {
		return InAdvanceResult{}, err
	}

	amount := withOut.amount.Sub(withoutOut.amount)
	cur := s.Currency
	rounded := cur.Round(amount, money.HalfUp)

	r := InAdvanceResult{
		Units:              inAdvanceUnits(s, agg, ev),
		Count:              1,
		AmountCents:        rounded.Mul(cur.SubunitToUnit()),
		PreciseAmountCents: amount.Mul(cur.SubunitToUnit()),
	}
	if !rounded.IsZero() {
		r.UnitAmount = rounded.DivOrZero(r.Units)
	}
	if ev.Persisted {
		r.Details = inAdvanceDetails(withOut.details, withoutOut.details)
	}
	return r, nil
}

// inAdvanceUnits returns the units billed for the event: the units applied
// to a max-style aggregation that did not exceed its maximum, the full
// units of a prorated charge, or the event's units.
func inAdvanceUnits(s PricingStructure, agg AggregationResult, ev InAdvanceEvent) money.Decimal {
	if agg.CurrentAggregation != nil && agg.MaxAggregation != nil && agg.UnitsApplied != nil &&
		agg.CurrentAggregation.LessThanOrEqual(*agg.MaxAggregation) {
		return money.Max(*agg.UnitsApplied, money.Zero)
	}
	if s.Prorated {
		return orZero(agg.FullUnits)
	}
	return ev.Units
}

// inAdvanceDetails returns the details of one event: with minus without.
func inAdvanceDetails(with, without Details) Details {
	switch w := with.(type) {
	case PercentageDetails:
		wo, ok := without.(PercentageDetails)
		if !ok {
			return nil
		}
		d := PercentageDetails{
			Units:                       w.Units.Sub(wo.Units),
			FreeEvents:                  w.FreeEvents - wo.FreeEvents,
			PaidUnits:                   w.PaidUnits.Sub(wo.PaidUnits),
			Rate:                        w.Rate,
			PerUnitTotalAmount:          w.PerUnitTotalAmount.Sub(wo.PerUnitTotalAmount),
			PaidEvents:                  w.PaidEvents - wo.PaidEvents,
			FixedFeeUnitAmount:          w.FixedFeeUnitAmount,
			FixedFeeTotalAmount:         w.FixedFeeTotalAmount.Sub(wo.FixedFeeTotalAmount),
			MinMaxAdjustmentTotalAmount: w.MinMaxAdjustmentTotalAmount.Sub(wo.MinMaxAdjustmentTotalAmount),
		}
		// The free units are what the event's units did not pay for.
		d.FreeUnits = d.Units.Sub(d.PaidUnits)
		return d
	case GraduatedPercentageDetails:
		wo, ok := without.(GraduatedPercentageDetails)
		if !ok {
			return nil
		}
		d := GraduatedPercentageDetails{Ranges: make([]GraduatedPercentageRangeDetails, 0, len(w.Ranges))}
		for _, rw := range w.Ranges {
			var ro GraduatedPercentageRangeDetails
			for _, r := range wo.Ranges {
				if r.From.Equal(rw.From) && equalBound(r.To, rw.To) {
					ro = r
					break
				}
			}
			total := rw.TotalWithFlatAmount.Sub(ro.TotalWithFlatAmount)
			units := rw.Units.Sub(ro.Units)
			perUnit := money.Zero
			if units.IsPositive() {
				perUnit = total.Div(units).Round(2, money.HalfUp)
			}
			d.Ranges = append(d.Ranges, GraduatedPercentageRangeDetails{
				From:                rw.From,
				To:                  rw.To,
				FlatUnitAmount:      rw.FlatUnitAmount.Sub(ro.FlatUnitAmount),
				Rate:                rw.Rate,
				Units:               units,
				PerUnitTotalAmount:  perUnit,
				TotalWithFlatAmount: total,
			})
		}
		return d
	default:
		return nil
	}
}

func equalBound(a, b *money.Decimal) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
