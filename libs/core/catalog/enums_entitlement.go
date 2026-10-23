package catalog

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// CommitmentType is the kind of a plan commitment (domain §7.8).
type CommitmentType string

// CommitmentType values.
const (
	// CommitmentTypeMinimumCommitment is "minimum_commitment": a minimum spend per billing period.
	CommitmentTypeMinimumCommitment CommitmentType = "minimum_commitment"
)

// CommitmentTypeValues returns every CommitmentType, in declaration order.
func CommitmentTypeValues() []CommitmentType {
	return []CommitmentType{CommitmentTypeMinimumCommitment}
}

// Valid reports whether v is one of the declared CommitmentType values.
func (v CommitmentType) Valid() bool {
	switch v {
	case CommitmentTypeMinimumCommitment:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v CommitmentType) String() string { return string(v) }

// ParseCommitmentType parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseCommitmentType(s string) (CommitmentType, error) {
	if v := CommitmentType(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("catalog.CommitmentType", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v CommitmentType) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *CommitmentType) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseCommitmentType(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// PrivilegeValueType is the type of the values of a privilege (domain §7.10).
type PrivilegeValueType string

// PrivilegeValueType values.
const (
	// PrivilegeValueTypeInteger is "integer": a 64-bit integer.
	PrivilegeValueTypeInteger PrivilegeValueType = "integer"
	// PrivilegeValueTypeString is "string": free text.
	PrivilegeValueTypeString PrivilegeValueType = "string"
	// PrivilegeValueTypeBoolean is "boolean": true or false.
	PrivilegeValueTypeBoolean PrivilegeValueType = "boolean"
	// PrivilegeValueTypeSelect is "select": one of the privilege's select options.
	PrivilegeValueTypeSelect PrivilegeValueType = "select"
)

// PrivilegeValueTypeValues returns every PrivilegeValueType, in declaration order.
func PrivilegeValueTypeValues() []PrivilegeValueType {
	return []PrivilegeValueType{PrivilegeValueTypeInteger, PrivilegeValueTypeString, PrivilegeValueTypeBoolean, PrivilegeValueTypeSelect}
}

// Valid reports whether v is one of the declared PrivilegeValueType values.
func (v PrivilegeValueType) Valid() bool {
	switch v {
	case PrivilegeValueTypeInteger, PrivilegeValueTypeString, PrivilegeValueTypeBoolean, PrivilegeValueTypeSelect:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v PrivilegeValueType) String() string { return string(v) }

// ParsePrivilegeValueType parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParsePrivilegeValueType(s string) (PrivilegeValueType, error) {
	if v := PrivilegeValueType(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("catalog.PrivilegeValueType", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v PrivilegeValueType) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *PrivilegeValueType) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParsePrivilegeValueType(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// TaxSource is the level of tax resolution that supplied a fee's taxes (domain §22.10), in priority order.
type TaxSource string

// TaxSource values.
const (
	// TaxSourceExplicit is "explicit": tax codes given on a one-off invoice line.
	TaxSourceExplicit TaxSource = "explicit"
	// TaxSourceAddOn is "add_on": the add-on's taxes.
	TaxSourceAddOn TaxSource = "add_on"
	// TaxSourceCharge is "charge": the charge's taxes.
	TaxSourceCharge TaxSource = "charge"
	// TaxSourceFixedCharge is "fixed_charge": the fixed charge's taxes.
	TaxSourceFixedCharge TaxSource = "fixed_charge"
	// TaxSourceCommitment is "commitment": the commitment's taxes.
	TaxSourceCommitment TaxSource = "commitment"
	// TaxSourcePlan is "plan": the plan's taxes.
	TaxSourcePlan TaxSource = "plan"
	// TaxSourceCustomer is "customer": the customer's taxes.
	TaxSourceCustomer TaxSource = "customer"
	// TaxSourceBillingEntity is "billing_entity": the billing entity's default taxes.
	TaxSourceBillingEntity TaxSource = "billing_entity"
)

// TaxSourceValues returns every TaxSource, in declaration order.
func TaxSourceValues() []TaxSource {
	return []TaxSource{TaxSourceExplicit, TaxSourceAddOn, TaxSourceCharge, TaxSourceFixedCharge, TaxSourceCommitment, TaxSourcePlan, TaxSourceCustomer, TaxSourceBillingEntity}
}

// Valid reports whether v is one of the declared TaxSource values.
func (v TaxSource) Valid() bool {
	switch v {
	case TaxSourceExplicit, TaxSourceAddOn, TaxSourceCharge, TaxSourceFixedCharge, TaxSourceCommitment, TaxSourcePlan, TaxSourceCustomer, TaxSourceBillingEntity:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v TaxSource) String() string { return string(v) }

// ParseTaxSource parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseTaxSource(s string) (TaxSource, error) {
	if v := TaxSource(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("catalog.TaxSource", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v TaxSource) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *TaxSource) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseTaxSource(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}
