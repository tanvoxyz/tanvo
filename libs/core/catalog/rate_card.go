package catalog

import (
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Rule codes of rate cards, rates and phases (lago's names).
const (
	// CodeDoesNotBelongToProduct: the product filter is another product's.
	CodeDoesNotBelongToProduct = "does_not_belong_to_product"
	// CodeNotAllowedForBillingTiming: the setting does not fit the rate
	// card's billing timing.
	CodeNotAllowedForBillingTiming = "not_allowed_for_billing_timing"
	// CodeNotAllowedForAggregationType: the setting does not fit the
	// metric's aggregation.
	CodeNotAllowedForAggregationType = "not_allowed_for_aggregation_type"
	// CodeRequiresRecurringMetric: proration needs a recurring metric.
	CodeRequiresRecurringMetric = "requires_recurring_metric"
	// CodeNotAllowedWithDisplayOnInvoice: paid fees are regrouped only
	// when hidden from invoices.
	CodeNotAllowedWithDisplayOnInvoice = "not_allowed_with_display_on_invoice"
	// CodeNotAllowedForProduct: the rate model does not price fixed
	// products.
	CodeNotAllowedForProduct = "not_allowed_for_product"
	// CodeNotAllowedWithProration: the rate model cannot be prorated.
	CodeNotAllowedWithProration = "not_allowed_with_proration"
	// CodeNotEditableWithRates: the billing settings of a rate card freeze
	// once it has rates.
	CodeNotEditableWithRates = "not_editable_with_rates"
	// CodeMustBeAfterActiveRate: a new rate takes effect after the active
	// one.
	CodeMustBeAfterActiveRate = "must_be_after_active_rate"
	// CodeMustNotBeBeforeToday: the effective date is in the past.
	CodeMustNotBeBeforeToday = "must_not_be_before_today"
	// CodeNotEditableOnActiveRate: only the prices of the active rate are
	// editable.
	CodeNotEditableOnActiveRate = "not_editable_on_active_rate"
	// CodeTerminatedRateNotEditable: a superseded rate is frozen.
	CodeTerminatedRateNotEditable = "terminated_rate_not_editable"
	// CodeOnlyPendingRatesCanBeDeleted: active and terminated rates are
	// kept.
	CodeOnlyPendingRatesCanBeDeleted = "only_pending_rates_can_be_deleted"
	// CodeFromValueNotAllowed: catalog tiers name their upper bound only.
	CodeFromValueNotAllowed = "from_value_not_allowed"
	// CodePlanLocked: the plan has subscriptions, so its rate cards are
	// frozen.
	CodePlanLocked = "plan_locked"
	// CodeCurrencyDoesNotMatch: the rate card's currency is not the
	// plan's.
	CodeCurrencyDoesNotMatch = "currency_does_not_match"
	// CodeProductAlreadyPriced: the plan already prices the product.
	CodeProductAlreadyPriced = "product_already_priced"
	// CodeProductFilterAlreadyPriced: the plan already prices the product
	// filter.
	CodeProductFilterAlreadyPriced = "product_filter_already_priced"
	// CodePositionsMustBeContiguous: phase positions run from 1 without
	// gaps.
	CodePositionsMustBeContiguous = "positions_must_be_contiguous"
	// CodeIndefinitePhaseMustBeLast: only the last phase has no cycle
	// count.
	CodeIndefinitePhaseMustBeLast = "indefinite_phase_must_be_last"
	// CodeLastPhaseMustBeIndefinite: the last phase has no cycle count.
	CodeLastPhaseMustBeIndefinite = "last_phase_must_be_indefinite"
	// CodeIndefinitePhaseNotDeletable: the indefinite last phase stays.
	CodeIndefinitePhaseNotDeletable = "indefinite_phase_not_deletable"
	// CodeCannotDeleteLastPhase: a plan rate card keeps one phase.
	CodeCannotDeleteLastPhase = "cannot_delete_last_phase"
)

// BillingTiming says when a rate card bills.
type BillingTiming string

// BillingTiming values.
const (
	// BillingTimingArrears is "arrears": at the end of each interval.
	BillingTimingArrears BillingTiming = "arrears"
	// BillingTimingAdvance is "advance": at the start of each interval.
	BillingTimingAdvance BillingTiming = "advance"
)

// BillingTimingValues returns every BillingTiming, in declaration order.
func BillingTimingValues() []BillingTiming {
	return []BillingTiming{BillingTimingArrears, BillingTimingAdvance}
}

// Valid reports whether v is one of the declared BillingTiming values.
func (v BillingTiming) Valid() bool { return v == BillingTimingArrears || v == BillingTimingAdvance }

// String returns the stored form of v.
func (v BillingTiming) String() string { return string(v) }

// ParseBillingTiming parses the stored form s, failing with
// [core.ErrInvalidValue] for anything but a declared value.
func ParseBillingTiming(s string) (BillingTiming, error) {
	if v := BillingTiming(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("catalog.BillingTiming", s)
}

// IntervalUnit is the unit of a rate's billing interval.
type IntervalUnit string

// IntervalUnit values.
const (
	// IntervalUnitDay is "day".
	IntervalUnitDay IntervalUnit = "day"
	// IntervalUnitWeek is "week".
	IntervalUnitWeek IntervalUnit = "week"
	// IntervalUnitMonth is "month".
	IntervalUnitMonth IntervalUnit = "month"
	// IntervalUnitYear is "year".
	IntervalUnitYear IntervalUnit = "year"
)

// IntervalUnitValues returns every IntervalUnit, in declaration order.
func IntervalUnitValues() []IntervalUnit {
	return []IntervalUnit{IntervalUnitDay, IntervalUnitWeek, IntervalUnitMonth, IntervalUnitYear}
}

// Valid reports whether v is one of the declared IntervalUnit values.
func (v IntervalUnit) Valid() bool { return slices.Contains(IntervalUnitValues(), v) }

// String returns the stored form of v.
func (v IntervalUnit) String() string { return string(v) }

// ParseIntervalUnit parses the stored form s, failing with
// [core.ErrInvalidValue] for anything but a declared value.
func ParseIntervalUnit(s string) (IntervalUnit, error) {
	if v := IntervalUnit(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("catalog.IntervalUnit", s)
}

// RateStatus is where a rate stands on its rate card's timeline; it is
// derived, not stored.
type RateStatus string

// RateStatus values.
const (
	// RateStatusPending is "pending": the rate takes effect later.
	RateStatusPending RateStatus = "pending"
	// RateStatusActive is "active": the latest rate in effect.
	RateStatusActive RateStatus = "active"
	// RateStatusTerminated is "terminated": a later rate superseded it.
	RateStatusTerminated RateStatus = "terminated"
)

// RateStatusValues returns every RateStatus, in declaration order.
func RateStatusValues() []RateStatus {
	return []RateStatus{RateStatusPending, RateStatusActive, RateStatusTerminated}
}

// Valid reports whether v is one of the declared RateStatus values.
func (v RateStatus) Valid() bool { return slices.Contains(RateStatusValues(), v) }

// String returns the stored form of v.
func (v RateStatus) String() string { return string(v) }

// RateCard prices a product, or one of its filters, in one currency.
type RateCard struct {
	// ID identifies the rate card.
	ID core.RateCardID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// ProductID is the priced product.
	ProductID core.ProductID
	// ProductFilterID is the priced filter of the product; zero for the
	// whole product.
	ProductFilterID core.ProductFilterID
	// Code is unique among the organization's rate cards.
	Code string
	// Name is the display name.
	Name string
	// Description is free text.
	Description *string
	// Currency is the currency of the rates.
	Currency money.Currency
	// BillingTiming says when the card bills.
	BillingTiming BillingTiming
	// Proration prorates partial intervals.
	Proration bool
	// DisplayOnInvoice prints the card's fees on invoices.
	DisplayOnInvoice bool
	// RegroupPaidFees regroups paid in-advance fees; empty keeps them
	// standalone.
	RegroupPaidFees RegroupPaidFees
	// PricingUnitID is the pricing unit the rates are expressed in; zero
	// for the currency.
	PricingUnitID core.PricingUnitID
	// TaxIDs are the card's taxes.
	TaxIDs []core.TaxID
	// CreatedAt is when the card was created.
	CreatedAt time.Time
	// UpdatedAt is when the card last changed.
	UpdatedAt time.Time
	// DeletedAt is when the row was discarded; nil while kept.
	DeletedAt *time.Time
}

// ValidateRateCard checks the card c of product p, whose metric m is nil
// for a fixed product, and filter f (nil when c prices the whole
// product).
//
// source: app/models/rate_card.rb:37-104
func ValidateRateCard(c *RateCard, p *Product, f *ProductFilter, m *BillableMetric) error {
	var v core.Validator
	v.String("name", c.Name, 255)
	catalogCode(&v, "code", c.Code)
	v.Currency("currency", c.Currency)
	timing := v.Enum("billing_timing", c.BillingTiming)
	regroup := v.OptionalEnum("regroup_paid_fees", c.RegroupPaidFees)
	if f != nil && f.ProductID != c.ProductID {
		v.Add("product_filter", CodeDoesNotBelongToProduct)
	}
	if !c.DisplayOnInvoice && timing {
		switch {
		case p != nil && p.ProductType == ProductTypeFixed:
			v.Add("display_on_invoice", CodeNotAllowedForProductType)
		case c.BillingTiming != BillingTimingAdvance:
			v.Add("display_on_invoice", CodeNotAllowedForBillingTiming)
		}
	}
	if c.Proration && p != nil && p.ProductType == ProductTypeMetered && m != nil {
		v.Check(m.Recurring, "proration", CodeRequiresRecurringMetric)
		v.Check(m.AggregationType != AggregationTypeWeightedSum, "proration", CodeNotAllowedForAggregationType)
	}
	if regroup && c.RegroupPaidFees == RegroupPaidFeesInvoice {
		v.Check(c.BillingTiming == BillingTimingAdvance, "regroup_paid_fees", CodeNotAllowedForBillingTiming)
		v.Check(!c.DisplayOnInvoice, "regroup_paid_fees", CodeNotAllowedWithDisplayOnInvoice)
	}
	return v.Err()
}

// Rate models of the product catalog's compatibility matrix.
//
// source: app/services/rate_card_rates/model_compatibility.rb:7-11
var (
	fixedProductRateModels   = []ChargeModel{ChargeModelStandard, ChargeModelGraduated, ChargeModelVolume}
	prorationArrearsModels   = []ChargeModel{ChargeModelStandard, ChargeModelVolume, ChargeModelGraduated}
	prorationAdvanceModels   = []ChargeModel{ChargeModelStandard}
	percentageRateModelsList = []ChargeModel{ChargeModelPercentage, ChargeModelGraduatedPercentage}
)

// RateModelError returns the rule code a rate of model breaks on the card
// c of product p (metric m, nil for a fixed product), or "" when the
// combination is billable.
//
// source: app/services/rate_card_rates/model_compatibility.rb:15-57
func RateModelError(model ChargeModel, c *RateCard, p *Product, m *BillableMetric) string {
	if model == "" || p == nil {
		return ""
	}
	if p.ProductType == ProductTypeFixed && !slices.Contains(fixedProductRateModels, model) {
		return CodeNotAllowedForProduct
	}
	if model == ChargeModelDynamic && (m == nil || m.AggregationType != AggregationTypeSum) {
		return CodeNotAllowedForAggregationType
	}
	if slices.Contains(percentageRateModelsList, model) && m != nil && m.AggregationType == AggregationTypeLatest {
		return CodeNotAllowedForAggregationType
	}
	if c.BillingTiming == BillingTimingAdvance {
		if model == ChargeModelVolume {
			return CodeNotAllowedForBillingTiming
		}
		if m != nil && !m.PayableInAdvance() {
			return CodeNotAllowedForAggregationType
		}
	}
	if c.Proration && !slices.Contains(prorationModels(c, m), model) {
		return CodeNotAllowedWithProration
	}
	return ""
}

func prorationModels(c *RateCard, m *BillableMetric) []ChargeModel {
	if m != nil && (m.AggregationType == AggregationTypeWeightedSum || !m.Recurring) {
		return nil
	}
	if c.BillingTiming == BillingTimingAdvance {
		return prorationAdvanceModels
	}
	return prorationArrearsModels
}

// Rate is the price of a rate card from its effective instant until the
// next rate's. A card's rates form an append-only timeline.
type Rate struct {
	// ID identifies the rate.
	ID core.RateCardRateID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// RateCardID is the card.
	RateCardID core.RateCardID
	// Code is unique among the card's rates.
	Code string
	// EffectiveFrom is when the rate takes effect; unique per card.
	EffectiveFrom time.Time
	// RateModel is the pricing model.
	RateModel ChargeModel
	// Properties are the prices, as charge properties with adjacent
	// ranges.
	Properties Properties
	// MinAmount is the minimum per billing interval, in minor units of
	// the card's currency.
	MinAmount int64
	// BillingIntervalCount is the number of units of the interval, ≥ 1.
	BillingIntervalCount int
	// BillingIntervalUnit is the unit of the interval.
	BillingIntervalUnit IntervalUnit
	// PricingUnitConversionRate converts the card's pricing unit to its
	// currency; required when the card has a pricing unit.
	PricingUnitConversionRate *money.Decimal
	// CreatedAt is when the rate was created.
	CreatedAt time.Time
	// UpdatedAt is when the rate last changed.
	UpdatedAt time.Time
	// DeletedAt is when the row was discarded; nil while kept.
	DeletedAt *time.Time
}

// RateStatusAt returns the status of r at now among the card's rates:
// pending before its effective instant, terminated once a later rate is
// in effect, active otherwise.
//
// source: app/models/rate_card_rate.rb:66-80
func RateStatusAt(r *Rate, rates []*Rate, now time.Time) RateStatus {
	if r.EffectiveFrom.After(now) {
		return RateStatusPending
	}
	for _, o := range rates {
		if o.EffectiveFrom.After(r.EffectiveFrom) && !o.EffectiveFrom.After(now) {
			return RateStatusTerminated
		}
	}
	return RateStatusActive
}

// ActiveRate returns the latest rate in effect at now, or nil.
//
// source: app/models/rate_card.rb:120-122
func ActiveRate(rates []*Rate, now time.Time) *Rate {
	var out *Rate
	for _, r := range rates {
		if !r.EffectiveFrom.After(now) && (out == nil || r.EffectiveFrom.After(out.EffectiveFrom)) {
			out = r
		}
	}
	return out
}

// NormalizeEffectiveFrom returns t in UTC, at midnight on an arrears card:
// arrears rates apply per whole day, advance rates price per event.
//
// source: app/models/rate_card_rate.rb:104-109
func NormalizeEffectiveFrom(t time.Time, timing BillingTiming) time.Time {
	t = t.UTC()
	if timing == BillingTimingArrears {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}
	return t
}

// ValidateRate checks the rate r of card c (product p, metric m nil for a
// fixed product) among the card's other rates at now; placed reports
// whether r is new or its effective instant changed, which checks its
// place on the timeline. The properties are checked by the pricing
// library.
//
// source: app/models/rate_card_rate.rb:47-58, :111-160
func ValidateRate(r *Rate, c *RateCard, p *Product, m *BillableMetric, others []*Rate, placed bool, now time.Time) error {
	var v core.Validator
	catalogCode(&v, "code", r.Code)
	if r.BillingIntervalUnit == "" {
		v.Add("billing_interval.unit", core.CodeRequired)
	} else {
		v.Enum("billing_interval.unit", r.BillingIntervalUnit)
	}
	if r.EffectiveFrom.IsZero() {
		v.Add("effective_from", core.CodeRequired)
	}
	model := false
	if r.RateModel == "" {
		v.Add("rate_model", core.CodeRequired)
	} else if model = v.Enum("rate_model", r.RateModel); model {
		if code := RateModelError(r.RateModel, c, p, m); code != "" {
			v.Add("rate_model", code)
		}
	}
	v.Check(r.MinAmount >= 0, "min_amount", core.CodeOutOfRange)
	v.Check(r.BillingIntervalCount >= 1, "billing_interval.count", core.CodeOutOfRange)
	if placed && !r.EffectiveFrom.IsZero() {
		validatePlacement(&v, r, others, now)
	}
	if !c.PricingUnitID.IsZero() {
		v.Check(r.PricingUnitConversionRate != nil, "pricing_unit_conversion_rate", core.CodeRequired)
	}
	if r.MinAmount > 0 && c.BillingTiming == BillingTimingAdvance {
		v.Add("min_amount", CodeNotAllowedForBillingTiming)
	}
	if r.PricingUnitConversionRate != nil {
		v.DecimalPositive("pricing_unit_conversion_rate", *r.PricingUnitConversionRate)
	}
	return v.Err()
}

// validatePlacement checks the append-only timeline: no other rate at the
// same instant, and a rate after the latest one in effect.
//
// source: app/models/rate_card_rate.rb:134-150
func validatePlacement(v *core.Validator, r *Rate, others []*Rate, now time.Time) {
	var boundary *time.Time
	for _, o := range others {
		if o.ID == r.ID {
			continue
		}
		if o.EffectiveFrom.Equal(r.EffectiveFrom) {
			v.Add("effective_from", core.CodeAlreadyExists)
			return
		}
		if !o.EffectiveFrom.After(now) && (boundary == nil || o.EffectiveFrom.After(*boundary)) {
			boundary = &o.EffectiveFrom
		}
	}
	if boundary != nil && !r.EffectiveFrom.After(*boundary) {
		v.Add("effective_from", CodeMustBeAfterActiveRate)
	}
}

// PlanRateCard applies a rate card to a plan.
type PlanRateCard struct {
	// ID identifies the plan rate card.
	ID core.PlanRateCardID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// PlanID is the plan.
	PlanID core.PlanID
	// RateCardID is the applied card; a card is applied once per plan.
	RateCardID core.RateCardID
	// Units is the quantity billed, for fixed products; nil when unset.
	Units *money.Decimal
	// CreatedAt is when the card was applied.
	CreatedAt time.Time
	// UpdatedAt is when the plan rate card last changed.
	UpdatedAt time.Time
	// DeletedAt is when the row was discarded; nil while kept.
	DeletedAt *time.Time
}

// Validate checks the plan rate card's own fields.
//
// source: app/models/plan_rate_card.rb:17
func (p *PlanRateCard) Validate() error {
	var v core.Validator
	if p.Units != nil {
		v.Check(!p.Units.IsNegative(), "units", core.CodeOutOfRange)
	}
	return v.Err()
}

// RateOverride replaces a rate's prices during a phase. The card's
// structural settings (currency, timing, proration, display, regrouping,
// pricing unit) are never overridden.
type RateOverride struct {
	// RateModel is the pricing model.
	RateModel ChargeModel
	// Properties are the prices.
	Properties Properties
	// MinAmount is the minimum per billing interval, in minor units.
	MinAmount int64
	// BillingIntervalCount overrides the rate's interval count; nil keeps
	// it.
	BillingIntervalCount *int
	// BillingIntervalUnit overrides the rate's interval unit; empty keeps
	// it.
	BillingIntervalUnit IntervalUnit
	// PricingUnitConversionRate converts the card's pricing unit; required
	// when the card has one.
	PricingUnitConversionRate *money.Decimal
}

// ValidateRateOverride checks the override o of a phase of card c
// (product p, metric m nil for a fixed product). The properties are
// checked by the pricing library.
//
// source: app/services/rate_overrides/create_service.rb:23-44, app/models/rate_override.rb:29-33
func ValidateRateOverride(o *RateOverride, c *RateCard, p *Product, m *BillableMetric) error {
	var v core.Validator
	if !c.PricingUnitID.IsZero() && o.PricingUnitConversionRate == nil {
		v.Add("pricing_unit_conversion_rate", core.CodeRequired)
		return v.Err()
	}
	if o.RateModel == "" {
		v.Add("rate_model", core.CodeRequired)
	} else if v.Enum("rate_model", o.RateModel) {
		if code := RateModelError(o.RateModel, c, p, m); code != "" {
			v.Add("rate_model", code)
			return v.Err()
		}
	}
	if o.MinAmount > 0 && c.BillingTiming == BillingTimingAdvance {
		v.Add("min_amount", CodeNotAllowedForBillingTiming)
		return v.Err()
	}
	v.Check(o.MinAmount >= 0, "min_amount", core.CodeOutOfRange)
	if o.BillingIntervalCount != nil {
		v.Check(*o.BillingIntervalCount >= 1, "billing_interval.count", core.CodeOutOfRange)
	}
	v.OptionalEnum("billing_interval.unit", o.BillingIntervalUnit)
	if o.PricingUnitConversionRate != nil {
		v.DecimalPositive("pricing_unit_conversion_rate", *o.PricingUnitConversionRate)
	}
	return v.Err()
}

// RatePhase is one step of a plan rate card's ramp: a number of billing
// interval cycles, optionally at overridden prices. Positions run from 1;
// only the last phase is indefinite.
type RatePhase struct {
	// ID identifies the phase.
	ID core.RatePhaseID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// PlanRateCardID is the plan rate card.
	PlanRateCardID core.PlanRateCardID
	// Code is unique among the plan rate card's phases.
	Code string
	// Position is the phase's place, from 1.
	Position int
	// Name is the display name.
	Name *string
	// BillingIntervalCycleCount is the number of billing intervals; nil
	// for the indefinite last phase.
	BillingIntervalCycleCount *int
	// Override is the phase's prices; nil bills the card's rates.
	Override *RateOverride
	// CreatedAt is when the phase was created.
	CreatedAt time.Time
	// UpdatedAt is when the phase last changed.
	UpdatedAt time.Time
	// DeletedAt is when the row was discarded; nil while kept.
	DeletedAt *time.Time
}

// Validate checks the phase's own fields.
//
// source: app/models/rate_phase.rb:19-29
func (p *RatePhase) Validate() error {
	var v core.Validator
	catalogCode(&v, "code", p.Code)
	v.Check(p.Position > 0, "position", core.CodeOutOfRange)
	if p.BillingIntervalCycleCount != nil {
		v.Check(*p.BillingIntervalCycleCount > 0, "billing_interval_cycle_count", core.CodeOutOfRange)
	}
	v.OptionalString("name", p.Name, 255)
	return v.Err()
}
