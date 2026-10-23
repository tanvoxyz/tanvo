package pricing

import (
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// PricingStructure is what a charge model needs to know about a charge.
type PricingStructure struct {
	// Properties are the model's properties; they select the model.
	Properties Properties
	// Prorated is set for charges prorated over the period.
	Prorated bool
	// AcceptsTargetWallet is set for charges whose usage is split by the
	// wallet events target; their aggregation is grouped.
	AcceptsTargetWallet bool
	// Currency is the currency of the amounts.
	Currency money.Currency
	// ProductCatalog is set for catalog rates, whose touching tiers are
	// counted half-open and whose prorated graduated tiers are priced on
	// the full units.
	ProductCatalog bool
}

// ErrNoCurrency is returned for a pricing structure without a currency.
var ErrNoCurrency = errors.New("pricing: currency is mandatory")

// check rejects a structure that cannot price anything.
func (s PricingStructure) check() error {
	if s.Properties == nil {
		return fmt.Errorf("%w: no properties", ErrInvalidProperties)
	}
	if s.Currency.IsZero() {
		return ErrNoCurrency
	}
	return nil
}

// proratedProductCatalog reports whether graduated tiers are reached by the
// full units and priced at the proration ratio.
func (s PricingStructure) proratedProductCatalog() bool {
	return s.ProductCatalog && s.Prorated
}

// grouped reports whether aggregations are priced per group.
func (s PricingStructure) grouped() bool {
	return len(s.Properties.Groups().PricingGroupKeys) > 0 || s.AcceptsTargetWallet
}

// ApplyOptions tune [Apply].
type ApplyOptions struct {
	// Project computes ProjectedUnits and ProjectedAmount.
	Project bool
	// PeriodRatio is the elapsed share of the billing period (0.5 halfway
	// through), used for projection. Projections are 0 when it is not
	// positive.
	PeriodRatio money.Decimal
}

// calc is one application of a model to an aggregation.
type calc struct {
	s    PricingStructure
	agg  AggregationResult
	opts ApplyOptions
	// excludeEvent prices the aggregation without the pay-in-advance
	// event: tiers then charge no flat amount for zero units.
	excludeEvent bool
	// inAdvance prices a pay-in-advance event: graduated charges are then
	// priced without the prorated calculators.
	inAdvance bool
}

func (c calc) units() money.Decimal { return c.agg.Units }

// projectedUnits returns the units projected to the end of the period,
// rounded half up to 2 decimals.
func (c calc) projectedUnits() money.Decimal {
	if c.units().IsZero() || !c.opts.PeriodRatio.IsPositive() {
		return money.Zero
	}
	return c.units().Div(c.opts.PeriodRatio).Round(2, money.HalfUp)
}

// scaleToPeriod divides a current amount by the period ratio.
func (c calc) scaleToPeriod(amount money.Decimal) money.Decimal {
	if amount.IsZero() || c.opts.PeriodRatio.IsZero() {
		return money.Zero
	}
	return amount.Div(c.opts.PeriodRatio)
}

// unitAmountOver returns amount over the full units (or units), 0 when
// there are none.
func (c calc) unitAmountOver(amount money.Decimal) money.Decimal {
	return amount.DivOrZero(c.agg.fullOrUnits())
}

// output is what a model computes.
type output struct {
	amount     money.Decimal
	unitAmount money.Decimal
	details    Details
	projected  money.Decimal
}

// Apply prices agg with the model of s.Properties. A grouped charge (see
// [PricingStructure]) with agg.Groups set is priced group by group, and its
// amount and units are the groups' sums.
func Apply(s PricingStructure, agg AggregationResult, opts ApplyOptions) (Result, error) {
	if err := s.check(); err != nil {
		return Result{}, err
	}
	if s.grouped() && agg.Groups != nil {
		return applyGrouped(s, agg, opts)
	}
	return applyOne(calc{s: s, agg: agg, opts: opts})
}

func applyGrouped(s PricingStructure, agg AggregationResult, opts ApplyOptions) (Result, error) {
	r := Result{Groups: make([]Result, 0, len(agg.Groups))}
	for _, group := range agg.Groups {
		// Groups share the parent's per-event availability, which selects
		// the prorated graduated calculator.
		if group.PerEvent == nil && agg.PerEvent != nil {
			group.PerEvent = &PerEventAggregation{}
		}
		gr, err := applyOne(calc{s: s, agg: group, opts: opts})
		if err != nil {
			return Result{}, err
		}
		gr.GroupedBy = group.GroupedBy
		r.Groups = append(r.Groups, gr)
		r.Amount = r.Amount.Add(gr.Amount)
		r.Units = r.Units.Add(gr.Units)
		if opts.Project {
			r.ProjectedAmount = r.ProjectedAmount.Add(gr.ProjectedAmount)
			r.ProjectedUnits = r.ProjectedUnits.Add(gr.ProjectedUnits)
		}
	}
	return r, nil
}

// applyOne prices a single (group) aggregation.
func applyOne(c calc) (Result, error) {
	out, err := compute(c)
	if err != nil {
		return Result{}, err
	}
	r := Result{
		Units:                c.agg.Units,
		CurrentUsageUnits:    c.agg.CurrentUsageUnits,
		FullUnits:            c.agg.FullUnits,
		Count:                c.agg.Count,
		Amount:               out.amount,
		UnitAmount:           out.unitAmount,
		Details:              out.details,
		TotalAggregatedUnits: c.agg.TotalAggregatedUnits,
		GroupedBy:            c.agg.GroupedBy,
	}
	if c.opts.Project {
		r.ProjectedUnits = c.projectedUnits()
		r.ProjectedAmount = out.projected
	}
	return r, nil
}

// compute dispatches to the model.
func compute(c calc) (output, error) {
	switch p := c.s.Properties.(type) {
	case StandardProperties:
		return standard(c, p), nil
	case PackageProperties:
		return packageModel(c, p)
	case PercentageProperties:
		return percentage(c, p), nil
	case GraduatedProperties:
		if c.s.Prorated && c.agg.PerEvent != nil && !c.inAdvance {
			if c.s.ProductCatalog {
				return proratedAdjacentGraduated(c, p)
			}
			return proratedGraduated(c, p)
		}
		return graduated(c, p)
	case VolumeProperties:
		return volume(c, p)
	case GraduatedPercentageProperties:
		return graduatedPercentage(c, p)
	case DynamicProperties:
		return dynamic(c), nil
	default:
		return output{}, fmt.Errorf("%w: %T", ErrUnknownModel, p)
	}
}
