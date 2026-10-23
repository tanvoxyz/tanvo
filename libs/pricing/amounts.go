package pricing

import (
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// AppliedPricingUnit is a custom pricing unit a charge is priced in, with
// its conversion rate to the invoice currency (fiat amount = pricing-unit
// amount × ConversionRate).
type AppliedPricingUnit struct {
	// Unit is the pricing unit, a custom [money.Currency] (its exponent is
	// 2 for every pricing unit).
	Unit money.Currency
	// ConversionRate is the fiat value of one pricing unit, positive.
	ConversionRate money.Decimal
}

// FeeAmount is the amount of a fee in the invoice currency.
type FeeAmount struct {
	// AmountCents is the rounded amount in minor units.
	AmountCents int64
	// PreciseAmountCents is the unrounded amount in minor units.
	PreciseAmountCents money.Decimal
	// UnitAmountCents is the unit amount in minor units, truncated toward
	// zero.
	UnitAmountCents int64
	// PreciseUnitAmount is the unrounded unit amount in units.
	PreciseUnitAmount money.Decimal
	// PricingUnitUsage is the amount in the pricing unit, when the charge
	// has one.
	PricingUnitUsage *PricingUnitUsage
}

// PricingUnitUsage is the amount of a fee in its pricing unit.
type PricingUnitUsage struct {
	// Unit is the pricing unit.
	Unit money.Currency
	// ConversionRate is the rate the fiat amounts were converted with.
	ConversionRate money.Decimal
	// AmountCents is the rounded amount in pricing-unit minor units.
	AmountCents int64
	// PreciseAmountCents is the unrounded amount in pricing-unit minor
	// units.
	PreciseAmountCents money.Decimal
	// UnitAmountCents is the unit amount in pricing-unit minor units,
	// truncated toward zero.
	UnitAmountCents int64
	// PreciseUnitAmount is the unrounded unit amount in pricing units.
	PreciseUnitAmount money.Decimal
}

// Deduction takes an amount already billed, prorated to the billed days,
// off a fee (an in-advance fee already paid, for instance).
type Deduction struct {
	// AmountCents is the amount to deduct for a full period, in minor
	// units.
	AmountCents int64
	// BilledDays and PeriodDays prorate it.
	BilledDays int64
	// PeriodDays is the number of days in the period, positive.
	PeriodDays int64
}

// proratedAmountCents returns AmountCents × BilledDays / PeriodDays rounded
// half up.
func (d Deduction) proratedAmountCents() money.Decimal {
	return money.NewFromInt(d.AmountCents).Mul(money.NewFromInt(d.BilledDays)).
		Div(money.NewFromInt(d.PeriodDays)).Round(0, money.HalfUp)
}

// TrueUp is a minimum commitment: when a charge's usage is below the
// prorated minimum, a true-up fee bills the difference.
type TrueUp struct {
	// MinimumAmountCents is the minimum for a full period, in minor units
	// (pricing-unit minor units for a charge with a pricing unit).
	MinimumAmountCents int64
	// BilledDays and PeriodDays prorate the minimum.
	BilledDays int64
	// PeriodDays is the number of days in the period, positive.
	PeriodDays int64
	// UsedAmountCents is the amount the minimum is compared with when the
	// usage spans several fees (grouped fees); nil to use this fee's.
	UsedAmountCents *money.Decimal
	// UsedPreciseAmountCents is the precise counterpart of
	// UsedAmountCents; nil to use this fee's.
	UsedPreciseAmountCents *money.Decimal
}

// proratedMinimumAmountCents returns MinimumAmountCents / PeriodDays ×
// BilledDays, unrounded.
func (t TrueUp) proratedMinimumAmountCents() money.Decimal {
	return money.NewFromInt(t.MinimumAmountCents).Mul(money.NewFromInt(t.BilledDays)).
		Div(money.NewFromInt(t.PeriodDays))
}

// AmountsInput is what [Amounts] builds the amounts of a fee from.
type AmountsInput struct {
	// Currency is the invoice currency.
	Currency money.Currency
	// Result is a charge model result, or nil when InAdvance is set.
	Result *Result
	// InAdvance is a pay-in-advance result, or nil.
	InAdvance *InAdvanceResult
	// PricingUnit is the charge's pricing unit, or nil.
	PricingUnit *AppliedPricingUnit
	// Deduction is taken off the fee, or nil.
	Deduction *Deduction
	// TrueUp is the charge's minimum commitment, or nil.
	TrueUp *TrueUp
}

// Amounts are the fee amounts of a model result.
type Amounts struct {
	// Amount is the fee's amount, nil when there is no model result.
	Amount *FeeAmount
	// TrueUp is the true-up fee's amount, nil when there is none.
	TrueUp *FeeAmount
}

// ComputeAmounts converts a charge model result into the amounts of a fee, in
// minor units of the invoice currency: rounded half up to the currency's
// exponent, unrounded, and per unit. A negative result (units or amount) is
// a zero fee. With a pricing unit, the amounts are first expressed in it,
// then converted at its rate.
func ComputeAmounts(in AmountsInput) (Amounts, error) {
	var out Amounts
	switch {
	case in.InAdvance != nil:
		a, err := advanceAmount(in)
		if err != nil {
			return Amounts{}, err
		}
		out.Amount = &a
	case in.Result != nil:
		amount, unit := in.Result.Amount, in.Result.UnitAmount
		if in.Result.Units.IsNegative() || amount.IsNegative() {
			amount, unit = money.Zero, money.Zero
		}
		a, err := buildAmount(in, amount, unit)
		if err != nil {
			return Amounts{}, err
		}
		out.Amount = &a
	}
	if out.Amount != nil && in.Deduction != nil {
		if in.Deduction.PeriodDays <= 0 {
			return Amounts{}, fmt.Errorf("pricing: deduction over %d period days", in.Deduction.PeriodDays)
		}
		d := in.Deduction.proratedAmountCents()
		cents := money.Max(money.NewFromInt(out.Amount.AmountCents).Sub(d), money.Zero)
		n, err := toInt(cents)
		if err != nil {
			return Amounts{}, err
		}
		out.Amount.AmountCents = n
		out.Amount.PreciseAmountCents = money.Max(out.Amount.PreciseAmountCents.Sub(d), money.Zero)
	}
	if in.TrueUp != nil {
		t, err := trueUpAmount(in, out.Amount)
		if err != nil {
			return Amounts{}, err
		}
		out.TrueUp = t
	}
	return out, nil
}

// advanceAmount converts a pay-in-advance result, already in minor units.
func advanceAmount(in AmountsInput) (FeeAmount, error) {
	r := in.InAdvance
	if in.PricingUnit != nil {
		return buildAmount(in, r.AmountCents.Div(in.PricingUnit.Unit.SubunitToUnit()), r.UnitAmount)
	}
	cents, err := toInt(r.AmountCents)
	if err != nil {
		return FeeAmount{}, err
	}
	unitCents, err := toInt(r.UnitAmount.Mul(in.Currency.SubunitToUnit()))
	if err != nil {
		return FeeAmount{}, err
	}
	return FeeAmount{
		AmountCents:        cents,
		PreciseAmountCents: r.PreciseAmountCents,
		UnitAmountCents:    unitCents,
		PreciseUnitAmount:  r.UnitAmount,
	}, nil
}

// buildAmount converts an amount and a unit amount in units (of the pricing
// unit when the charge has one).
func buildAmount(in AmountsInput, amount, unitAmount money.Decimal) (FeeAmount, error) {
	if in.PricingUnit == nil {
		return fiatAmount(in.Currency, amount, unitAmount)
	}
	pu := in.PricingUnit
	usage, err := fiatAmount(pu.Unit, amount, unitAmount)
	if err != nil {
		return FeeAmount{}, err
	}
	// The fiat amounts convert the pricing-unit minor units, rounded and
	// truncated as stored.
	sub := pu.Unit.SubunitToUnit()
	adjusted := money.NewFromInt(usage.AmountCents).Mul(pu.ConversionRate).Div(sub)
	adjustedUnit := money.NewFromInt(usage.UnitAmountCents).Mul(pu.ConversionRate).Div(sub)
	fiat, err := fiatAmount(in.Currency, adjusted, adjustedUnit)
	if err != nil {
		return FeeAmount{}, err
	}
	fiat.PricingUnitUsage = &PricingUnitUsage{
		Unit:               pu.Unit,
		ConversionRate:     pu.ConversionRate,
		AmountCents:        usage.AmountCents,
		PreciseAmountCents: usage.PreciseAmountCents,
		UnitAmountCents:    usage.UnitAmountCents,
		PreciseUnitAmount:  usage.PreciseUnitAmount,
	}
	return fiat, nil
}

// fiatAmount expresses amounts in units of cur in its minor units.
func fiatAmount(cur money.Currency, amount, unitAmount money.Decimal) (FeeAmount, error) {
	sub := cur.SubunitToUnit()
	cents, err := toInt(cur.Round(amount, money.HalfUp).Mul(sub))
	if err != nil {
		return FeeAmount{}, err
	}
	unitCents, err := toInt(unitAmount.Mul(sub))
	if err != nil {
		return FeeAmount{}, err
	}
	return FeeAmount{
		AmountCents:        cents,
		PreciseAmountCents: amount.Mul(sub),
		UnitAmountCents:    unitCents,
		PreciseUnitAmount:  unitAmount,
	}, nil
}

// trueUpAmount bills the difference between the prorated minimum and the
// usage, if the usage is below it.
func trueUpAmount(in AmountsInput, amount *FeeAmount) (*FeeAmount, error) {
	t := in.TrueUp
	if t.PeriodDays <= 0 {
		return nil, fmt.Errorf("pricing: true-up over %d period days", t.PeriodDays)
	}
	minimum := t.proratedMinimumAmountCents()

	var used, preciseUsed money.Decimal
	switch {
	case amount != nil && amount.PricingUnitUsage != nil:
		used = money.NewFromInt(amount.PricingUnitUsage.AmountCents)
		preciseUsed = amount.PricingUnitUsage.PreciseAmountCents
	case amount != nil:
		used = money.NewFromInt(amount.AmountCents)
		preciseUsed = amount.PreciseAmountCents
	}
	if t.UsedAmountCents != nil {
		used = *t.UsedAmountCents
	}
	if t.UsedPreciseAmountCents != nil {
		preciseUsed = *t.UsedPreciseAmountCents
	}
	if used.GreaterThanOrEqual(minimum) {
		return nil, nil
	}
	diff := minimum.Sub(used)
	preciseDiff := minimum.Sub(preciseUsed)

	if in.PricingUnit != nil {
		sub := in.PricingUnit.Unit.SubunitToUnit()
		a, err := buildAmount(in, diff.Div(sub), preciseDiff.Div(sub))
		if err != nil {
			return nil, err
		}
		return &a, nil
	}
	cents, err := toInt(diff.Round(0, money.HalfUp))
	if err != nil {
		return nil, err
	}
	return &FeeAmount{
		AmountCents:        cents,
		PreciseAmountCents: preciseDiff,
		UnitAmountCents:    cents,
		PreciseUnitAmount:  in.Currency.MinorToUnits(preciseDiff),
	}, nil
}

// toInt truncates d toward zero to an int64, as a bigint column stores it.
func toInt(d money.Decimal) (int64, error) {
	n, ok := d.Int64()
	if !ok {
		return 0, fmt.Errorf("%w: %s", money.ErrOverflow, d)
	}
	return n, nil
}
