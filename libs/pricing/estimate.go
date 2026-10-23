package pricing

import (
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// Estimate is the instant estimate of one event's price.
type Estimate struct {
	// Units is the event's units, 0 when negative.
	Units money.Decimal
	// Amount is the event's price in units, unrounded.
	Amount money.Decimal
}

// EstimateInstant estimates the price of one event of units for a
// standard or percentage charge, without its period's other events: units ×
// amount, or units × rate / 100 plus the fixed amount, clamped to the
// per-transaction limits. A negative quantity is priced 0.
func EstimateInstant(p Properties, units money.Decimal) (Estimate, error) {
	if units.IsNegative() {
		return Estimate{Units: money.Zero, Amount: money.Zero}, nil
	}
	switch p := p.(type) {
	case StandardProperties:
		return Estimate{Units: units, Amount: units.Mul(p.Amount)}, nil
	case PercentageProperties:
		amount := units.Mul(p.Rate).Div(money.Hundred).Add(orZero(p.FixedAmount))
		pc := percentageCalc{p: p}
		return Estimate{Units: units, Amount: pc.clamp(amount)}, nil
	default:
		return Estimate{}, fmt.Errorf("%w: %T cannot be estimated", ErrUnknownModel, p)
	}
}

// Price returns the amount, in currency units, of units priced with s on
// their own: no events, no running total, and the full units equal to the
// units. It quotes a price for a quantity.
func Price(s PricingStructure, units money.Decimal) (money.Decimal, error) {
	agg := AggregationResult{
		Units:                   units,
		CurrentUsageUnits:       &units,
		FullUnits:               &units,
		TotalAggregatedUnits:    &units,
		PreciseTotalAmountCents: ptr(money.Zero),
	}
	r, err := Apply(s, agg, ApplyOptions{})
	if err != nil {
		return money.Zero, err
	}
	return r.Amount, nil
}
