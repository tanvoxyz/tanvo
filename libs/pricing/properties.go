package pricing

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// ChargeModel names a pricing model.
type ChargeModel string

// The charge models. Fixed charges support [Standard], [Graduated] and
// [Volume] only.
const (
	// Standard prices each unit at one amount.
	Standard ChargeModel = "standard"
	// Graduated prices each unit at the amount of its tier.
	Graduated ChargeModel = "graduated"
	// GraduatedPercentage takes a percentage of each unit at the rate of
	// its tier.
	GraduatedPercentage ChargeModel = "graduated_percentage"
	// Package prices units by started packages.
	Package ChargeModel = "package"
	// Percentage takes a percentage of the units, plus fixed amounts per
	// transaction.
	Percentage ChargeModel = "percentage"
	// Volume prices every unit at the amount of the tier the total falls
	// in.
	Volume ChargeModel = "volume"
	// Dynamic prices units at the amounts their events carry.
	Dynamic ChargeModel = "dynamic"
)

// ErrUnknownModel is returned for a charge model this package does not
// implement.
var ErrUnknownModel = errors.New("pricing: unknown charge model")

// ErrInvalidProperties is returned when properties cannot be priced, such
// as a package size of zero or ranges that cover no unit count.
var ErrInvalidProperties = errors.New("pricing: invalid properties")

// Properties are the typed properties of one charge model.
type Properties interface {
	// Model returns the charge model the properties belong to.
	Model() ChargeModel
	// Groups returns the grouping keys shared by every model.
	Groups() Grouping
}

// Grouping holds the properties that split a charge's usage into groups.
type Grouping struct {
	// PricingGroupKeys are the event properties whose values each get their
	// own fee, priced separately.
	PricingGroupKeys []string `json:"pricing_group_keys,omitempty"`
	// PresentationGroupKeys are the event properties shown as a breakdown
	// of a fee, at most two.
	PresentationGroupKeys []PresentationGroupKey `json:"presentation_group_keys,omitempty"`
}

// Groups returns g.
func (g Grouping) Groups() Grouping { return g }

// PresentationGroupKey is one presentation breakdown key.
type PresentationGroupKey struct {
	// Value is the event property name.
	Value string `json:"value"`
	// Options are the display options, nil when absent.
	Options *PresentationOptions `json:"options,omitempty"`
}

// PresentationOptions are the display options of a presentation key.
type PresentationOptions struct {
	// DisplayInInvoice shows the breakdown on the invoice.
	DisplayInInvoice bool `json:"display_in_invoice"`
}

// StandardProperties price every unit at Amount.
type StandardProperties struct {
	Grouping
	// Amount is the price of one unit.
	Amount money.Decimal `json:"amount"`
}

// Model returns [Standard].
func (StandardProperties) Model() ChargeModel { return Standard }

// PackageProperties price units by packages of PackageSize units, after
// FreeUnits free units; a started package is billed in full.
type PackageProperties struct {
	Grouping
	// Amount is the price of one package.
	Amount money.Decimal `json:"amount"`
	// PackageSize is the number of units in a package, at least 1.
	PackageSize int64 `json:"package_size"`
	// FreeUnits is the number of units given away before packages count.
	FreeUnits int64 `json:"free_units"`
}

// Model returns [Package].
func (PackageProperties) Model() ChargeModel { return Package }

// PercentageProperties charge a percentage of the aggregated amount, with
// an optional fixed fee per transaction and free units.
type PercentageProperties struct {
	Grouping
	// Rate is the percentage applied to the paid units (1.5 is 1.5 %).
	Rate money.Decimal `json:"rate"`
	// FixedAmount is charged for every paid transaction (event); nil for
	// none.
	FixedAmount *money.Decimal `json:"fixed_amount,omitempty"`
	// FreeUnitsPerEvents is the number of first events that are free; 0
	// for none.
	FreeUnitsPerEvents int64 `json:"free_units_per_events,omitempty"`
	// FreeUnitsPerTotalAggregation is the aggregated amount that is free;
	// nil (or 0) for none.
	FreeUnitsPerTotalAggregation *money.Decimal `json:"free_units_per_total_aggregation,omitempty"`
	// PerTransactionMinAmount is the minimum amount of one transaction;
	// nil for none. Setting it (or the maximum) prices transactions one by
	// one, which needs the per-event values of the aggregation.
	PerTransactionMinAmount *money.Decimal `json:"per_transaction_min_amount,omitempty"`
	// PerTransactionMaxAmount is the maximum amount of one transaction;
	// nil for none.
	PerTransactionMaxAmount *money.Decimal `json:"per_transaction_max_amount,omitempty"`
}

// Model returns [Percentage].
func (PercentageProperties) Model() ChargeModel { return Percentage }

// hasMinMax reports whether transactions are priced one by one.
func (p PercentageProperties) hasMinMax() bool {
	return p.PerTransactionMinAmount != nil || p.PerTransactionMaxAmount != nil
}

// Range is a tier of graduated or volume pricing. Units from From to To
// (nil for no upper bound) are priced at PerUnitAmount, plus FlatAmount once
// the tier is reached.
type Range struct {
	// From is the lower bound of the tier.
	From money.Decimal `json:"from_value"`
	// To is the upper bound of the tier, nil for the last tier.
	To *money.Decimal `json:"to_value"`
	// PerUnitAmount is the price of one unit in the tier.
	PerUnitAmount money.Decimal `json:"per_unit_amount"`
	// FlatAmount is charged once when the tier is reached.
	FlatAmount money.Decimal `json:"flat_amount"`
}

// GraduatedProperties price each unit at the tier it falls in.
type GraduatedProperties struct {
	Grouping
	// Ranges are the tiers in ascending order.
	Ranges []Range `json:"graduated_ranges"`
}

// Model returns [Graduated].
func (GraduatedProperties) Model() ChargeModel { return Graduated }

// VolumeProperties price every unit at the tier the total falls in.
type VolumeProperties struct {
	Grouping
	// Ranges are the tiers; they are matched in ascending order of From.
	Ranges []Range `json:"volume_ranges"`
}

// Model returns [Volume].
func (VolumeProperties) Model() ChargeModel { return Volume }

// PercentageRange is a tier of graduated percentage pricing.
type PercentageRange struct {
	// From is the lower bound of the tier.
	From money.Decimal `json:"from_value"`
	// To is the upper bound of the tier, nil for the last tier.
	To *money.Decimal `json:"to_value"`
	// Rate is the percentage applied to the units in the tier.
	Rate money.Decimal `json:"rate"`
	// FlatAmount is charged once when the tier is reached.
	FlatAmount money.Decimal `json:"flat_amount"`
}

// GraduatedPercentageProperties apply each tier's rate to the units in it.
type GraduatedPercentageProperties struct {
	Grouping
	// Ranges are the tiers in ascending order.
	Ranges []PercentageRange `json:"graduated_percentage_ranges"`
}

// Model returns [GraduatedPercentage].
func (GraduatedPercentageProperties) Model() ChargeModel { return GraduatedPercentage }

// DynamicProperties price units at the amounts the events carry.
type DynamicProperties struct {
	Grouping
}

// Model returns [Dynamic].
func (DynamicProperties) Model() ChargeModel { return Dynamic }

// DecodeProperties decodes the JSON properties of a charge with model into
// typed properties. Keys the model does not use are ignored. It does not
// validate them: see [Validate].
func DecodeProperties(model ChargeModel, data []byte) (Properties, error) {
	if len(data) == 0 {
		data = []byte("{}")
	}
	var (
		p   Properties
		err error
	)
	switch model {
	case Standard:
		var v StandardProperties
		err = json.Unmarshal(data, &v)
		p = v
	case Package:
		var v PackageProperties
		err = json.Unmarshal(data, &v)
		p = v
	case Percentage:
		var v PercentageProperties
		err = json.Unmarshal(data, &v)
		p = v
	case Graduated:
		var v GraduatedProperties
		err = json.Unmarshal(data, &v)
		p = v
	case Volume:
		var v VolumeProperties
		err = json.Unmarshal(data, &v)
		p = v
	case GraduatedPercentage:
		var v GraduatedPercentageProperties
		err = json.Unmarshal(data, &v)
		p = v
	case Dynamic:
		var v DynamicProperties
		err = json.Unmarshal(data, &v)
		p = v
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownModel, model)
	}
	if err != nil {
		return nil, fmt.Errorf("pricing: decode %s properties: %w", model, err)
	}
	return p, nil
}

// DefaultProperties returns the properties of a charge created with model
// and no properties: free units, a single free tier, or a package of one.
func DefaultProperties(model ChargeModel) (Properties, error) {
	freeTier := []Range{{From: money.Zero, PerUnitAmount: money.Zero, FlatAmount: money.Zero}}
	switch model {
	case Standard:
		return StandardProperties{Amount: money.Zero}, nil
	case Graduated:
		return GraduatedProperties{Ranges: freeTier}, nil
	case Package:
		return PackageProperties{PackageSize: 1, Amount: money.Zero}, nil
	case Percentage:
		return PercentageProperties{Rate: money.Zero}, nil
	case Volume:
		return VolumeProperties{Ranges: freeTier}, nil
	case GraduatedPercentage:
		return GraduatedPercentageProperties{
			Ranges: []PercentageRange{{From: money.Zero, Rate: money.Zero, FlatAmount: money.Zero}},
		}, nil
	case Dynamic:
		return DynamicProperties{}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownModel, model)
	}
}
