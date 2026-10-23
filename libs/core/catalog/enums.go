package catalog

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// AggregationType is how a billable metric aggregates its events over a period (domain §7.1). Lago's custom aggregation is not ported (domain §24, item 1).
type AggregationType string

// AggregationType values.
const (
	// AggregationTypeCount is "count": the number of events.
	AggregationTypeCount AggregationType = "count"
	// AggregationTypeSum is "sum": the sum of field_name.
	AggregationTypeSum AggregationType = "sum"
	// AggregationTypeMax is "max": the largest field_name.
	AggregationTypeMax AggregationType = "max"
	// AggregationTypeUniqueCount is "unique_count": the number of distinct field_name values.
	AggregationTypeUniqueCount AggregationType = "unique_count"
	// AggregationTypeWeightedSum is "weighted_sum": the time-weighted sum of field_name.
	AggregationTypeWeightedSum AggregationType = "weighted_sum"
	// AggregationTypeLatest is "latest": the field_name of the latest event.
	AggregationTypeLatest AggregationType = "latest"
)

// AggregationTypeValues returns every AggregationType, in declaration order.
func AggregationTypeValues() []AggregationType {
	return []AggregationType{AggregationTypeCount, AggregationTypeSum, AggregationTypeMax, AggregationTypeUniqueCount, AggregationTypeWeightedSum, AggregationTypeLatest}
}

// Valid reports whether v is one of the declared AggregationType values.
func (v AggregationType) Valid() bool {
	switch v {
	case AggregationTypeCount, AggregationTypeSum, AggregationTypeMax, AggregationTypeUniqueCount, AggregationTypeWeightedSum, AggregationTypeLatest:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v AggregationType) String() string { return string(v) }

// ParseAggregationType parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseAggregationType(s string) (AggregationType, error) {
	if v := AggregationType(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("catalog.AggregationType", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v AggregationType) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *AggregationType) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseAggregationType(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// WeightedInterval is the time unit of a weighted sum (domain §7.1).
type WeightedInterval string

// WeightedInterval values.
const (
	// WeightedIntervalSeconds is "seconds": values are weighted by the seconds they are held.
	WeightedIntervalSeconds WeightedInterval = "seconds"
)

// WeightedIntervalValues returns every WeightedInterval, in declaration order.
func WeightedIntervalValues() []WeightedInterval {
	return []WeightedInterval{WeightedIntervalSeconds}
}

// Valid reports whether v is one of the declared WeightedInterval values.
func (v WeightedInterval) Valid() bool {
	switch v {
	case WeightedIntervalSeconds:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v WeightedInterval) String() string { return string(v) }

// ParseWeightedInterval parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseWeightedInterval(s string) (WeightedInterval, error) {
	if v := WeightedInterval(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("catalog.WeightedInterval", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v WeightedInterval) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *WeightedInterval) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseWeightedInterval(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// RoundingFunction is the rounding applied to an aggregated value (domain §7.1).
type RoundingFunction string

// RoundingFunction values.
const (
	// RoundingFunctionRound is "round": half up.
	RoundingFunctionRound RoundingFunction = "round"
	// RoundingFunctionFloor is "floor": towards negative infinity.
	RoundingFunctionFloor RoundingFunction = "floor"
	// RoundingFunctionCeil is "ceil": towards positive infinity.
	RoundingFunctionCeil RoundingFunction = "ceil"
)

// RoundingFunctionValues returns every RoundingFunction, in declaration order.
func RoundingFunctionValues() []RoundingFunction {
	return []RoundingFunction{RoundingFunctionRound, RoundingFunctionFloor, RoundingFunctionCeil}
}

// Valid reports whether v is one of the declared RoundingFunction values.
func (v RoundingFunction) Valid() bool {
	switch v {
	case RoundingFunctionRound, RoundingFunctionFloor, RoundingFunctionCeil:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v RoundingFunction) String() string { return string(v) }

// ParseRoundingFunction parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseRoundingFunction(s string) (RoundingFunction, error) {
	if v := RoundingFunction(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("catalog.RoundingFunction", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v RoundingFunction) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *RoundingFunction) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseRoundingFunction(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// ChargeModel is the pricing model of a charge (domain §7.3); the values are libs/pricing's model names. Lago's custom model is not ported (domain §24, item 1).
type ChargeModel string

// ChargeModel values.
const (
	// ChargeModelStandard is "standard": units × amount.
	ChargeModelStandard ChargeModel = "standard"
	// ChargeModelGraduated is "graduated": tiered per unit.
	ChargeModelGraduated ChargeModel = "graduated"
	// ChargeModelGraduatedPercentage is "graduated_percentage": tiered percentage of the amount.
	ChargeModelGraduatedPercentage ChargeModel = "graduated_percentage"
	// ChargeModelPackage is "package": priced per package of units.
	ChargeModelPackage ChargeModel = "package"
	// ChargeModelPercentage is "percentage": a percentage of the amount plus fixed fees.
	ChargeModelPercentage ChargeModel = "percentage"
	// ChargeModelVolume is "volume": the single tier containing the units.
	ChargeModelVolume ChargeModel = "volume"
	// ChargeModelDynamic is "dynamic": the price carried by the events.
	ChargeModelDynamic ChargeModel = "dynamic"
)

// ChargeModelValues returns every ChargeModel, in declaration order.
func ChargeModelValues() []ChargeModel {
	return []ChargeModel{ChargeModelStandard, ChargeModelGraduated, ChargeModelGraduatedPercentage, ChargeModelPackage, ChargeModelPercentage, ChargeModelVolume, ChargeModelDynamic}
}

// Valid reports whether v is one of the declared ChargeModel values.
func (v ChargeModel) Valid() bool {
	switch v {
	case ChargeModelStandard, ChargeModelGraduated, ChargeModelGraduatedPercentage, ChargeModelPackage, ChargeModelPercentage, ChargeModelVolume, ChargeModelDynamic:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v ChargeModel) String() string { return string(v) }

// ParseChargeModel parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseChargeModel(s string) (ChargeModel, error) {
	if v := ChargeModel(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("catalog.ChargeModel", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v ChargeModel) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *ChargeModel) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseChargeModel(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// FixedChargeModel is the pricing model of a fixed charge (domain §7.5).
type FixedChargeModel string

// FixedChargeModel values.
const (
	// FixedChargeModelStandard is "standard": units × amount.
	FixedChargeModelStandard FixedChargeModel = "standard"
	// FixedChargeModelGraduated is "graduated": tiered per unit.
	FixedChargeModelGraduated FixedChargeModel = "graduated"
	// FixedChargeModelVolume is "volume": the single tier containing the units.
	FixedChargeModelVolume FixedChargeModel = "volume"
)

// FixedChargeModelValues returns every FixedChargeModel, in declaration order.
func FixedChargeModelValues() []FixedChargeModel {
	return []FixedChargeModel{FixedChargeModelStandard, FixedChargeModelGraduated, FixedChargeModelVolume}
}

// Valid reports whether v is one of the declared FixedChargeModel values.
func (v FixedChargeModel) Valid() bool {
	switch v {
	case FixedChargeModelStandard, FixedChargeModelGraduated, FixedChargeModelVolume:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v FixedChargeModel) String() string { return string(v) }

// ParseFixedChargeModel parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseFixedChargeModel(s string) (FixedChargeModel, error) {
	if v := FixedChargeModel(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("catalog.FixedChargeModel", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v FixedChargeModel) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *FixedChargeModel) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseFixedChargeModel(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// RegroupPaidFees tells how paid, non-invoiceable in-advance fees are regrouped (domain §7.3).
type RegroupPaidFees string

// RegroupPaidFees values.
const (
	// RegroupPaidFeesInvoice is "invoice": on one advance_charges invoice at the period end.
	RegroupPaidFeesInvoice RegroupPaidFees = "invoice"
)

// RegroupPaidFeesValues returns every RegroupPaidFees, in declaration order.
func RegroupPaidFeesValues() []RegroupPaidFees {
	return []RegroupPaidFees{RegroupPaidFeesInvoice}
}

// Valid reports whether v is one of the declared RegroupPaidFees values.
func (v RegroupPaidFees) Valid() bool {
	switch v {
	case RegroupPaidFeesInvoice:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v RegroupPaidFees) String() string { return string(v) }

// ParseRegroupPaidFees parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseRegroupPaidFees(s string) (RegroupPaidFees, error) {
	if v := RegroupPaidFees(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("catalog.RegroupPaidFees", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v RegroupPaidFees) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *RegroupPaidFees) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseRegroupPaidFees(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// PlanStatus is the lifecycle status of a plan (domain §7.2).
type PlanStatus string

// PlanStatus values.
const (
	// PlanStatusActive is "active": usable by subscriptions.
	PlanStatusActive PlanStatus = "active"
	// PlanStatusPendingDeletion is "pending_deletion": being deleted by a job.
	PlanStatusPendingDeletion PlanStatus = "pending_deletion"
	// PlanStatusDeleted is "deleted": deleted while still referenced by
	// subscriptions (and so by their invoices) or by override plans, so
	// kept for them but hidden from lookups by code and from lists; its
	// code is free again (domain §7.2).
	PlanStatusDeleted PlanStatus = "deleted"
)

// PlanStatusValues returns every PlanStatus, in declaration order.
func PlanStatusValues() []PlanStatus {
	return []PlanStatus{PlanStatusActive, PlanStatusPendingDeletion, PlanStatusDeleted}
}

// Valid reports whether v is one of the declared PlanStatus values.
func (v PlanStatus) Valid() bool {
	switch v {
	case PlanStatusActive, PlanStatusPendingDeletion, PlanStatusDeleted:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v PlanStatus) String() string { return string(v) }

// ParsePlanStatus parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParsePlanStatus(s string) (PlanStatus, error) {
	if v := PlanStatus(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("catalog.PlanStatus", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v PlanStatus) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *PlanStatus) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParsePlanStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}
