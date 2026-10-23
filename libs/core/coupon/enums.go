package coupon

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// Type is the kind of discount of a coupon (domain §8.1).
type Type string

// Type values.
const (
	// TypeFixedAmount is "fixed_amount": a fixed amount in the coupon's currency.
	TypeFixedAmount Type = "fixed_amount"
	// TypePercentage is "percentage": a percentage of the base.
	TypePercentage Type = "percentage"
)

// TypeValues returns every Type, in declaration order.
func TypeValues() []Type {
	return []Type{TypeFixedAmount, TypePercentage}
}

// Valid reports whether v is one of the declared Type values.
func (v Type) Valid() bool {
	switch v {
	case TypeFixedAmount, TypePercentage:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v Type) String() string { return string(v) }

// ParseType parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseType(s string) (Type, error) {
	if v := Type(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("coupon.Type", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v Type) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *Type) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseType(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// Frequency tells how many billing periods a coupon applies to (domain §8.1).
type Frequency string

// Frequency values.
const (
	// FrequencyOnce is "once": one application, until its amount is used up.
	FrequencyOnce Frequency = "once"
	// FrequencyRecurring is "recurring": a number of billing periods (frequency_duration).
	FrequencyRecurring Frequency = "recurring"
	// FrequencyForever is "forever": every billing period.
	FrequencyForever Frequency = "forever"
)

// FrequencyValues returns every Frequency, in declaration order.
func FrequencyValues() []Frequency {
	return []Frequency{FrequencyOnce, FrequencyRecurring, FrequencyForever}
}

// Valid reports whether v is one of the declared Frequency values.
func (v Frequency) Valid() bool {
	switch v {
	case FrequencyOnce, FrequencyRecurring, FrequencyForever:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v Frequency) String() string { return string(v) }

// ParseFrequency parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseFrequency(s string) (Frequency, error) {
	if v := Frequency(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("coupon.Frequency", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v Frequency) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *Frequency) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseFrequency(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// Expiration tells whether a coupon can still be applied after a date (domain §8.1).
type Expiration string

// Expiration values.
const (
	// ExpirationNoExpiration is "no_expiration": never expires.
	ExpirationNoExpiration Expiration = "no_expiration"
	// ExpirationTimeLimit is "time_limit": expires at expiration_at.
	ExpirationTimeLimit Expiration = "time_limit"
)

// ExpirationValues returns every Expiration, in declaration order.
func ExpirationValues() []Expiration {
	return []Expiration{ExpirationNoExpiration, ExpirationTimeLimit}
}

// Valid reports whether v is one of the declared Expiration values.
func (v Expiration) Valid() bool {
	switch v {
	case ExpirationNoExpiration, ExpirationTimeLimit:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v Expiration) String() string { return string(v) }

// ParseExpiration parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseExpiration(s string) (Expiration, error) {
	if v := Expiration(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("coupon.Expiration", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v Expiration) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *Expiration) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseExpiration(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// Status is the lifecycle status of a coupon and of an applied coupon (domain §8).
type Status string

// Status values.
const (
	// StatusActive is "active": can be applied (coupon) or applies to invoices (applied coupon).
	StatusActive Status = "active"
	// StatusTerminated is "terminated": terminal.
	StatusTerminated Status = "terminated"
)

// StatusValues returns every Status, in declaration order.
func StatusValues() []Status {
	return []Status{StatusActive, StatusTerminated}
}

// Valid reports whether v is one of the declared Status values.
func (v Status) Valid() bool {
	switch v {
	case StatusActive, StatusTerminated:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v Status) String() string { return string(v) }

// ParseStatus parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseStatus(s string) (Status, error) {
	if v := Status(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("coupon.Status", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v Status) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *Status) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}
