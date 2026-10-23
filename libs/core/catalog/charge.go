package catalog

import (
	"slices"
	"strconv"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Charge prices a billable metric on a plan (domain §7.3). A charge with a
// ParentID belongs to an override plan and copies the charge ParentID of
// the original plan.
type Charge struct {
	// ID identifies the charge.
	ID core.ChargeID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// PlanID is the plan the charge belongs to.
	PlanID core.PlanID
	// ParentID is the charge of the original plan this override charge
	// copies.
	ParentID core.ChargeID
	// Code is unique per plan; see [DefaultChargeCode].
	Code string
	// BillableMetricID is the priced metric.
	BillableMetricID core.BillableMetricID
	// ChargeModel selects the pricing math of libs/pricing.
	ChargeModel ChargeModel
	// Properties are the model's properties (validated by libs/pricing).
	Properties Properties
	// Filters price slices of the events with their own properties
	// (domain §7.4).
	Filters []ChargeFilter
	// PayInAdvance bills a fee for each event as it arrives.
	PayInAdvance bool
	// Invoiceable puts in-advance fees on invoices; default true.
	Invoiceable bool
	// RegroupPaidFees regroups paid in-advance fees on one invoice.
	RegroupPaidFees RegroupPaidFees
	// Prorated prorates recurring usage by time.
	Prorated bool
	// MinAmount is the per-period minimum, ≥ 0, in minor units of the plan
	// currency.
	MinAmount int64
	// InvoiceDisplayName replaces the metric name on invoices.
	InvoiceDisplayName *string
	// AppliedPricingUnit prices the charge in a pricing unit (domain §7.6).
	AppliedPricingUnit *AppliedPricingUnit
	// TaxIDs are the charge's taxes (domain §22.10, step 5).
	TaxIDs []core.TaxID
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// AppliedPricingUnit is the pricing unit a charge is priced in, with the
// fiat value of one pricing unit (domain §7.6).
type AppliedPricingUnit struct {
	// PricingUnitID is the pricing unit.
	PricingUnitID core.PricingUnitID
	// ConversionRate is the fiat amount of one pricing unit, > 0.
	ConversionRate money.Decimal
}

// Validate checks conversion_rate > 0 and the pricing unit reference.
func (a *AppliedPricingUnit) Validate() error {
	var v core.Validator
	v.Check(!a.PricingUnitID.IsZero(), "pricing_unit_id", core.CodeRequired)
	v.DecimalPositive("conversion_rate", a.ConversionRate)
	return v.Err()
}

// ToFiat converts an amount in the pricing unit to the currency:
// round_half_up(amount × conversion_rate, currency exponent), in currency
// units (domain §7.6).
func (a *AppliedPricingUnit) ToFiat(amount money.Decimal, c money.Currency) money.Decimal {
	return c.Round(amount.Mul(a.ConversionRate), money.HalfUp)
}

// IsChild reports whether the charge belongs to an override plan.
func (c *Charge) IsChild() bool { return !c.ParentID.IsZero() }

// InvoiceName returns the invoice display name, or metricName when unset.
func (c *Charge) InvoiceName(metricName string) string {
	if c.InvoiceDisplayName != nil && *c.InvoiceDisplayName != "" {
		return *c.InvoiceDisplayName
	}
	return metricName
}

// EqualProperties reports whether c and other price the same way: same
// model and equal properties.
func (c *Charge) EqualProperties(other *Charge) bool {
	return c.ChargeModel == other.ChargeModel && c.Properties.Equal(other.Properties)
}

// Filter returns the filter with id.
func (c *Charge) Filter(id core.ChargeFilterID) (*ChargeFilter, bool) {
	for i := range c.Filters {
		if c.Filters[i].ID == id {
			return &c.Filters[i], true
		}
	}
	return nil, false
}

// Validate checks the rules of domain §7.3 that need no other entity; the
// metric-dependent matrix is [ValidateCharge].
func (c *Charge) Validate() error {
	var v core.Validator
	v.Code("code", c.Code, 255)
	v.Check(!c.PlanID.IsZero(), "plan_id", core.CodeRequired)
	v.Check(!c.BillableMetricID.IsZero(), "billable_metric_id", core.CodeRequired)
	v.Enum("charge_model", c.ChargeModel)
	v.Check(c.Properties.IsObject(), "properties", core.CodeInvalid)
	v.OptionalEnum("regroup_paid_fees", c.RegroupPaidFees)
	v.NonNegative("min_amount", c.MinAmount)
	v.OptionalString("invoice_display_name", c.InvoiceDisplayName, 255)
	if c.AppliedPricingUnit != nil {
		v.Merge("applied_pricing_unit", c.AppliedPricingUnit.Validate())
	}
	if !c.Invoiceable {
		v.Check(c.PayInAdvance, "invoiceable", CodeMustBeTrueUnlessPayInAdvance)
	}
	if c.RegroupPaidFees != "" {
		v.Check(c.PayInAdvance && !c.Invoiceable, "regroup_paid_fees", CodeOnlyCompatibleWithPayInAdvanceAndNonInvoiceable)
	}
	if c.PayInAdvance && c.MinAmount > 0 {
		v.Add("min_amount", CodeNotCompatibleWithPayInAdvance)
	}
	if c.PayInAdvance && c.ChargeModel == ChargeModelVolume {
		v.Add("pay_in_advance", CodeInvalidAggregationTypeOrChargeModel)
	}
	codes := map[string]bool{}
	for i := range c.Filters {
		f := &c.Filters[i]
		p := "filters[" + strconv.Itoa(i) + "]"
		v.Check(f.Properties.IsObject(), p+".properties", core.CodeInvalid)
		v.OptionalString(p+".invoice_display_name", f.InvoiceDisplayName, 255)
		if f.Code != "" {
			v.Check(!codes[f.Code], p+".code", core.CodeDuplicated)
			codes[f.Code] = true
		}
	}
	return v.Err()
}

// ValidateCharge checks c alone and against its metric m: the validation
// matrix of domain §7.3 and the filter values of domain §7.4.
func ValidateCharge(c *Charge, m *BillableMetric) error {
	var v core.Validator
	v.Merge("", c.Validate())
	if m == nil {
		return v.Err()
	}
	v.Check(c.BillableMetricID == m.ID, "billable_metric_id", core.CodeInvalid)
	switch c.ChargeModel {
	case ChargeModelDynamic:
		v.Check(m.AggregationType == AggregationTypeSum, "charge_model", CodeInvalidAggregationTypeOrChargeModel)
	case ChargeModelPercentage, ChargeModelGraduatedPercentage:
		v.Check(m.AggregationType != AggregationTypeLatest, "charge_model", CodeInvalidAggregationTypeOrChargeModel)
	}
	if c.PayInAdvance && c.ChargeModel != ChargeModelVolume {
		v.Check(m.PayableInAdvance(), "pay_in_advance", CodeInvalidAggregationTypeOrChargeModel)
	}
	if c.Prorated {
		v.Check(ChargeProratable(c.ChargeModel, c.PayInAdvance, m), "prorated", CodeInvalidBillableMetricOrChargeModel)
	}
	for i := range c.Filters {
		v.Merge("filters["+strconv.Itoa(i)+"]", ValidateChargeFilter(&c.Filters[i], m))
	}
	for i := range c.Filters {
		for j := range i {
			if ChargeFilterValuesEqual(c.Filters[i].Values, c.Filters[j].Values) {
				v.Add("filters["+strconv.Itoa(i)+"].values", core.CodeDuplicated)
				break
			}
		}
	}
	return v.Err()
}

// ChargeProratable reports whether a charge of model, billed in advance or
// not, may be prorated on metric m: the metric is recurring and not
// weighted_sum; in advance the model is standard; in arrears it is
// standard, volume or graduated.
func ChargeProratable(model ChargeModel, payInAdvance bool, m *BillableMetric) bool {
	if !m.Recurring || m.AggregationType == AggregationTypeWeightedSum {
		return false
	}
	if payInAdvance {
		return model == ChargeModelStandard
	}
	return model == ChargeModelStandard || model == ChargeModelVolume || model == ChargeModelGraduated
}

// NextFreeCode returns base when it is not in taken, else the first of
// base_2, base_3, … that is not.
func NextFreeCode(base string, taken func(string) bool) string {
	if !taken(base) {
		return base
	}
	for n := 2; ; n++ {
		if c := base + "_" + strconv.Itoa(n); !taken(c) {
			return c
		}
	}
}

// DefaultChargeCode returns the default code of a new charge of metric
// metricCode on a plan whose charges use the codes in taken: the metric
// code, then metricCode_2, metricCode_3, … (domain §7.3).
func DefaultChargeCode(metricCode string, taken []string) string {
	return NextFreeCode(metricCode, func(s string) bool { return slices.Contains(taken, s) })
}

// NewOverride returns the copy of c for the override plan plan: a new ID,
// ParentID = c and copied filters with new IDs from filterID (which keep
// their codes).
func (c *Charge) NewOverride(id core.ChargeID, plan core.PlanID, filterID func() core.ChargeFilterID, now time.Time) *Charge {
	o := chargeClone(c)
	o.ID = id
	o.PlanID = plan
	o.ParentID = c.ID
	o.CreatedAt = now
	o.UpdatedAt = now
	for i := range o.Filters {
		o.Filters[i].ID = filterID()
		o.Filters[i].ChargeID = id
		o.Filters[i].CreatedAt = now
	}
	return o
}

func chargeClone(c *Charge) *Charge {
	o := *c
	o.Properties = c.Properties.Clone()
	o.TaxIDs = slices.Clone(c.TaxIDs)
	if c.AppliedPricingUnit != nil {
		a := *c.AppliedPricingUnit
		o.AppliedPricingUnit = &a
	}
	o.Filters = make([]ChargeFilter, len(c.Filters))
	for i := range c.Filters {
		o.Filters[i] = c.Filters[i].Clone()
	}
	if c.Filters == nil {
		o.Filters = nil
	}
	return &o
}

// Clone returns a deep copy of c.
func (c *Charge) Clone() *Charge { return chargeClone(c) }
