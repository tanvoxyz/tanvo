package catalog

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// PricingUnitExponent is the exponent of every pricing unit: amounts have
// two decimals (domain §7.6).
const PricingUnitExponent = 2

// PricingUnit is a custom unit in which charges can be priced, such as
// credits or tokens (domain §7.6).
type PricingUnit struct {
	// ID identifies the pricing unit.
	ID core.PricingUnitID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Code is unique and immutable.
	Code string
	// Name is required, ≤ 255 characters.
	Name string
	// ShortName is the symbol printed with amounts, ≤ 3 characters.
	ShortName string
	// Description is free text, ≤ 600 characters.
	Description *string
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Exponent returns [PricingUnitExponent].
func (*PricingUnit) Exponent() int { return PricingUnitExponent }

// SubunitToUnit returns 10^exponent (100).
func (p *PricingUnit) SubunitToUnit() int64 {
	n := int64(1)
	for range p.Exponent() {
		n *= 10
	}
	return n
}

// Validate checks the invariants of domain §7.6.
func (p *PricingUnit) Validate() error {
	var v core.Validator
	v.Code("code", p.Code, 255)
	v.String("name", p.Name, 255)
	v.String("short_name", p.ShortName, 3)
	v.OptionalString("description", p.Description, 600)
	return v.Err()
}

// ValidatePricingUnitUpdate refuses a code change (core.CodeImmutable),
// then validates updated.
func ValidatePricingUnitUpdate(old, updated *PricingUnit) error {
	var v core.Validator
	v.Check(old.Code == updated.Code, "code", core.CodeImmutable)
	v.Merge("", updated.Validate())
	return v.Err()
}
