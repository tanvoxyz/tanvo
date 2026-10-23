package pricing

import "github.com/tanvoxyz/tanvo/libs/money"

// Result is the outcome of applying a charge model to an aggregation.
type Result struct {
	// Units is the aggregated number of units (the sum of the groups' for a
	// grouped charge).
	Units money.Decimal
	// CurrentUsageUnits is passed through from the aggregation.
	CurrentUsageUnits *money.Decimal
	// FullUnits is passed through from the aggregation.
	FullUnits *money.Decimal
	// Count is the number of events.
	Count int64
	// Amount is the price, in currency units, unrounded.
	Amount money.Decimal
	// UnitAmount is the average price of a unit, unrounded.
	UnitAmount money.Decimal
	// Details explains the amount; nil for the standard, dynamic and
	// prorated graduated models, and for a grouped result.
	Details Details
	// TotalAggregatedUnits is passed through from the aggregation.
	TotalAggregatedUnits *money.Decimal
	// GroupedBy holds the group's key values in an element of Groups.
	GroupedBy map[string]string
	// Groups holds the result of each group of a grouped charge; nil for an
	// ungrouped one (see [Result.GroupResults]).
	Groups []Result
	// ProjectedUnits is the number of units projected to the end of the
	// period, when projection is requested.
	ProjectedUnits money.Decimal
	// ProjectedAmount is the amount projected to the end of the period,
	// when projection is requested.
	ProjectedAmount money.Decimal
}

// GroupResults returns the results of each group, or the result itself
// when the charge is not grouped.
func (r Result) GroupResults() []Result {
	if r.Groups != nil {
		return r.Groups
	}
	return []Result{r}
}

// Details explains how a model computed an amount. It is one of
// [PackageDetails], [GraduatedDetails], [GraduatedPercentageDetails],
// [VolumeDetails] or [PercentageDetails].
type Details interface {
	details()
}

// PackageDetails explains a package amount.
type PackageDetails struct {
	// FreeUnits is the number of free units.
	FreeUnits money.Decimal `json:"free_units"`
	// PaidUnits is the number of units beyond the free ones, at least 0.
	PaidUnits money.Decimal `json:"paid_units"`
	// PerPackageSize is the package size (0 when there are no units).
	PerPackageSize int64 `json:"per_package_size"`
	// PerPackageUnitAmount is the price of a package (0 when there are no
	// units).
	PerPackageUnitAmount money.Decimal `json:"per_package_unit_amount"`
}

func (PackageDetails) details() {}

// GraduatedDetails explains a graduated amount, tier by tier.
type GraduatedDetails struct {
	// Ranges holds the tiers reached.
	Ranges []GraduatedRangeDetails `json:"graduated_ranges"`
}

func (GraduatedDetails) details() {}

// GraduatedRangeDetails is the share of one tier.
type GraduatedRangeDetails struct {
	// From is the tier's lower bound.
	From money.Decimal `json:"from_value"`
	// To is the tier's upper bound, nil for the last tier.
	To *money.Decimal `json:"to_value"`
	// FlatUnitAmount is the tier's flat amount.
	FlatUnitAmount money.Decimal `json:"flat_unit_amount"`
	// PerUnitAmount is the tier's unit price, 0 when no unit falls in it.
	PerUnitAmount money.Decimal `json:"per_unit_amount"`
	// Units is the number of units billed in the tier.
	Units money.Decimal `json:"units"`
	// PerUnitTotalAmount is Units × PerUnitAmount.
	PerUnitTotalAmount money.Decimal `json:"per_unit_total_amount"`
	// TotalWithFlatAmount is PerUnitTotalAmount + FlatUnitAmount.
	TotalWithFlatAmount money.Decimal `json:"total_with_flat_amount"`
}

// GraduatedPercentageDetails explains a graduated percentage amount.
type GraduatedPercentageDetails struct {
	// Ranges holds the tiers reached.
	Ranges []GraduatedPercentageRangeDetails `json:"graduated_percentage_ranges"`
}

func (GraduatedPercentageDetails) details() {}

// GraduatedPercentageRangeDetails is the share of one percentage tier.
type GraduatedPercentageRangeDetails struct {
	// From is the tier's lower bound.
	From money.Decimal `json:"from_value"`
	// To is the tier's upper bound, nil for the last tier.
	To *money.Decimal `json:"to_value"`
	// FlatUnitAmount is the tier's flat amount.
	FlatUnitAmount money.Decimal `json:"flat_unit_amount"`
	// Rate is the tier's percentage.
	Rate money.Decimal `json:"rate"`
	// Units is the number of units billed in the tier.
	Units money.Decimal `json:"units"`
	// PerUnitTotalAmount is Units × Rate / 100.
	PerUnitTotalAmount money.Decimal `json:"per_unit_total_amount"`
	// TotalWithFlatAmount is PerUnitTotalAmount + FlatUnitAmount.
	TotalWithFlatAmount money.Decimal `json:"total_with_flat_amount"`
}

// VolumeDetails explains a volume amount.
type VolumeDetails struct {
	// FlatUnitAmount is the matching tier's flat amount.
	FlatUnitAmount money.Decimal `json:"flat_unit_amount"`
	// PerUnitAmount is the effective price of a unit (PerUnitTotalAmount
	// over the number of units).
	PerUnitAmount money.Decimal `json:"per_unit_amount"`
	// PerUnitTotalAmount is the units priced at the matching tier.
	PerUnitTotalAmount money.Decimal `json:"per_unit_total_amount"`
}

func (VolumeDetails) details() {}

// PercentageDetails explains a percentage amount.
type PercentageDetails struct {
	// Units is the aggregated amount.
	Units money.Decimal `json:"units"`
	// FreeUnits is the free part of it.
	FreeUnits money.Decimal `json:"free_units"`
	// FreeEvents is the number of free events.
	FreeEvents int64 `json:"free_events"`
	// PaidUnits is Units - FreeUnits, at least 0.
	PaidUnits money.Decimal `json:"paid_units"`
	// Rate is the percentage.
	Rate money.Decimal `json:"rate"`
	// PerUnitTotalAmount is the percentage of the paid units.
	PerUnitTotalAmount money.Decimal `json:"per_unit_total_amount"`
	// PaidEvents is the number of paid events.
	PaidEvents int64 `json:"paid_events"`
	// FixedFeeUnitAmount is the fixed fee of one paid event.
	FixedFeeUnitAmount money.Decimal `json:"fixed_fee_unit_amount"`
	// FixedFeeTotalAmount is the fixed fees of all paid events.
	FixedFeeTotalAmount money.Decimal `json:"fixed_fee_total_amount"`
	// MinMaxAdjustmentTotalAmount is what per-transaction limits added to
	// (or took from) the amount.
	MinMaxAdjustmentTotalAmount money.Decimal `json:"min_max_adjustment_total_amount"`
}

func (PercentageDetails) details() {}
