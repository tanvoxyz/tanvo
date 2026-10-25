package usage

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/pricing"
)

// Projection is a fee projected to the end of its charges period.
type Projection struct {
	// Units are the projected units, rounded half up to 2 places.
	Units money.Decimal
	// Amount is the projected amount, in minor units.
	Amount int64
	// PricingUnitAmount is the projected amount in minor units of the
	// charge's pricing unit; nil when the charge has none.
	PricingUnitAmount *int64
	// PresentationBreakdowns are the fee's breakdowns with their units
	// projected like the fee's.
	PresentationBreakdowns []invoice.PresentationBreakdown
}

// repricedModels price the projected units with the charge properties;
// the others depend on per-event data the fee does not keep, so their
// amount is scaled instead (lago Fees::ProjectionService).
var repricedModels = []catalog.ChargeModel{
	catalog.ChargeModelStandard, catalog.ChargeModelGraduated, catalog.ChargeModelPackage, catalog.ChargeModelVolume,
}

// pricingUnitOf returns the pricing unit charge c prices in, or nil.
func pricingUnitOf(ctx context.Context, r port.Repos, org core.OrganizationID, c *catalog.Charge) (*pricing.AppliedPricingUnit, error) {
	if c.AppliedPricingUnit == nil {
		return nil, nil
	}
	pu, err := r.PricingUnits.Get(ctx, org, c.AppliedPricingUnit.PricingUnitID)
	if err != nil {
		return nil, fmt.Errorf("load pricing unit of charge %s: %w", c.ID, err)
	}
	unit, err := money.NewCustomCurrency(pu.Code, pu.Name, catalog.PricingUnitExponent)
	if err != nil {
		return nil, fmt.Errorf("pricing unit %s: %w", pu.ID, err)
	}
	return &pricing.AppliedPricingUnit{Unit: unit, ConversionRate: c.AppliedPricingUnit.ConversionRate}, nil
}

// project projects the current usage fee f of charge c at now (lago
// Fees::ProjectionService): a recurring metric keeps its current usage;
// otherwise the units are divided by the elapsed ratio of the charges
// period and, for the standard, graduated, package and volume models,
// priced with the charge (or filter) properties, through the pricing unit
// pu when the charge has one; the other models scale the current amount.
func project(f *invoice.Fee, c *catalog.Charge, m *catalog.BillableMetric, pu *pricing.AppliedPricingUnit, now time.Time, loc *time.Location) (Projection, error) {
	if m.Recurring {
		p := Projection{Units: f.Units, Amount: f.Amount, PresentationBreakdowns: invoice.ClonePresentationBreakdowns(f.PresentationBreakdowns)}
		if f.PricingUnitUsage != nil {
			p.PricingUnitAmount = ptr(kernel.RoundCents(f.PricingUnitUsage.Amount))
		}
		return p, nil
	}
	ratio := periodRatio(f.Properties.ChargesFrom, f.Properties.ChargesTo, now, loc)
	if !ratio.IsPositive() {
		p := Projection{Units: money.Zero}
		if pu != nil {
			p.PricingUnitAmount = ptr(int64(0))
		}
		return p, nil
	}
	units := money.Zero
	if f.Units.IsPositive() {
		units = f.Units.Div(ratio).Round(2, money.HalfUp)
	}
	breakdowns := projectedBreakdowns(f, ratio)
	if !slices.Contains(repricedModels, c.ChargeModel) {
		p := Projection{Units: units, Amount: scale(f.PreciseAmount, ratio), PresentationBreakdowns: breakdowns}
		if f.PricingUnitUsage != nil {
			p.PricingUnitAmount = ptr(scale(f.PricingUnitUsage.PreciseAmount, ratio))
		}
		return p, nil
	}
	raw := c.Properties
	if !f.ChargeFilterID.IsZero() {
		for i := range c.Filters {
			if c.Filters[i].ID == f.ChargeFilterID && !c.Filters[i].Properties.IsZero() {
				raw = c.Filters[i].Properties
			}
		}
	}
	props, err := pricing.DecodeProperties(pricing.ChargeModel(c.ChargeModel), raw.Raw())
	if err != nil {
		return Projection{}, fmt.Errorf("properties of charge %s: %w", c.ID, err)
	}
	cur := f.Currency
	if pu != nil {
		cur = pu.Unit
	}
	cu := f.Units
	agg := pricing.AggregationResult{
		Units: cu, CurrentUsageUnits: &cu, FullUnits: &cu, TotalAggregatedUnits: &cu,
		Count: f.EventsCount, PreciseTotalAmountCents: ptr(money.Zero),
	}
	res, err := pricing.Apply(pricing.PricingStructure{Properties: props, Currency: cur}, agg, pricing.ApplyOptions{Project: true, PeriodRatio: ratio})
	if err != nil {
		return Projection{}, fmt.Errorf("project fee of charge %s: %w", c.ID, err)
	}
	projected := pricing.Result{Units: res.ProjectedUnits, Amount: res.ProjectedAmount, UnitAmount: res.UnitAmount}
	amounts, err := pricing.ComputeAmounts(pricing.AmountsInput{Currency: f.Currency, Result: &projected, PricingUnit: pu})
	if err != nil {
		return Projection{}, fmt.Errorf("project fee of charge %s: %w", c.ID, err)
	}
	p := Projection{Units: units, Amount: amounts.Amount.AmountCents, PresentationBreakdowns: breakdowns}
	if u := amounts.Amount.PricingUnitUsage; u != nil {
		p.PricingUnitAmount = ptr(u.AmountCents)
	}
	return p, nil
}

// projectedBreakdowns divides the units of the fee's breakdowns by the
// period ratio, rounded half up to the exponent of the fee's currency
// (lago Fees::ProjectionService#projected_presentation_breakdowns).
//
// source: app/services/fees/projection_service.rb:122-128
func projectedBreakdowns(f *invoice.Fee, ratio money.Decimal) []invoice.PresentationBreakdown {
	out := invoice.ClonePresentationBreakdowns(f.PresentationBreakdowns)
	for i := range out {
		out[i].Units = out[i].Units.Div(ratio).Round(f.Currency.Exponent(), money.HalfUp)
	}
	return out
}

// scale divides a precise amount by the period ratio, rounded half up and
// floored at 0.
func scale(precise, ratio money.Decimal) int64 {
	return max(0, kernel.RoundCents(precise.Div(ratio)))
}

// ptr returns a pointer to v.
func ptr[T any](v T) *T { return &v }

// periodRatio returns the elapsed share of the charges period [from, to)
// at now, in local days of loc, clamped to [0, 1] (lago
// Fees::ProjectionService#period_ratio). A started day counts as a whole
// one, and so does the day starting at now when now is a local midnight
// (lago Utils::Datetime.date_diff_with_timezone).
func periodRatio(from, to, now time.Time, loc *time.Location) money.Decimal {
	switch {
	case !now.Before(to):
		return money.NewFromInt(1)
	case now.Before(from):
		return money.Zero
	}
	total := localDays(from, to, loc)
	if total <= 0 {
		return money.NewFromInt(1)
	}
	end := now
	if l := now.In(loc); l.Hour() == 0 && l.Minute() == 0 && l.Second() == 0 && l.Nanosecond() == 0 {
		end = end.Add(time.Second)
	}
	passed := min(localDays(from, end, loc), total)
	return money.NewFromInt(passed).Div(money.NewFromInt(total))
}

// localDays returns the number of days from a to b counted on the local
// clock of loc, a started day counting as a whole one.
func localDays(a, b time.Time, loc *time.Location) int64 {
	_, ao := a.In(loc).Zone()
	_, bo := b.In(loc).Zone()
	d := b.Sub(a) + time.Duration(bo-ao)*time.Second
	const day = 24 * time.Hour
	if d <= 0 {
		return 0
	}
	return int64((d + day - 1) / day)
}
