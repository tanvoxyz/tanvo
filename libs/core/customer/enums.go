package customer

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// Type is the legal kind of a customer (domain §6).
type Type string

// Type values.
const (
	// TypeCompany is "company": a legal entity.
	TypeCompany Type = "company"
	// TypeIndividual is "individual": a natural person.
	TypeIndividual Type = "individual"
)

// TypeValues returns every Type, in declaration order.
func TypeValues() []Type {
	return []Type{TypeCompany, TypeIndividual}
}

// Valid reports whether v is one of the declared Type values.
func (v Type) Valid() bool {
	switch v {
	case TypeCompany, TypeIndividual:
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
	return "", core.InvalidEnum("customer.Type", s)
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

// AccountType tells whether a customer is billed normally or self-bills Tanvo's user (domain §6).
type AccountType string

// AccountType values.
const (
	// AccountTypeCustomer is "customer": a regular customer.
	AccountTypeCustomer AccountType = "customer"
	// AccountTypePartner is "partner": a partner; its invoices are self-billed (domain §11.1).
	AccountTypePartner AccountType = "partner"
)

// AccountTypeValues returns every AccountType, in declaration order.
func AccountTypeValues() []AccountType {
	return []AccountType{AccountTypeCustomer, AccountTypePartner}
}

// Valid reports whether v is one of the declared AccountType values.
func (v AccountType) Valid() bool {
	switch v {
	case AccountTypeCustomer, AccountTypePartner:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v AccountType) String() string { return string(v) }

// ParseAccountType parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseAccountType(s string) (AccountType, error) {
	if v := AccountType(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("customer.AccountType", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v AccountType) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *AccountType) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseAccountType(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// ZeroAmountFinalization is a customer's choice for zero-amount invoices (domain §22.12).
type ZeroAmountFinalization string

// ZeroAmountFinalization values.
const (
	// ZeroAmountFinalizationInherit is "inherit": follow the billing entity's finalize_zero_amount_invoice.
	ZeroAmountFinalizationInherit ZeroAmountFinalization = "inherit"
	// ZeroAmountFinalizationSkip is "skip": close zero-amount invoices without finalizing them.
	ZeroAmountFinalizationSkip ZeroAmountFinalization = "skip"
	// ZeroAmountFinalizationFinalize is "finalize": finalize zero-amount invoices.
	ZeroAmountFinalizationFinalize ZeroAmountFinalization = "finalize"
)

// ZeroAmountFinalizationValues returns every ZeroAmountFinalization, in declaration order.
func ZeroAmountFinalizationValues() []ZeroAmountFinalization {
	return []ZeroAmountFinalization{ZeroAmountFinalizationInherit, ZeroAmountFinalizationSkip, ZeroAmountFinalizationFinalize}
}

// Valid reports whether v is one of the declared ZeroAmountFinalization values.
func (v ZeroAmountFinalization) Valid() bool {
	switch v {
	case ZeroAmountFinalizationInherit, ZeroAmountFinalizationSkip, ZeroAmountFinalizationFinalize:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v ZeroAmountFinalization) String() string { return string(v) }

// ParseZeroAmountFinalization parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseZeroAmountFinalization(s string) (ZeroAmountFinalization, error) {
	if v := ZeroAmountFinalization(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("customer.ZeroAmountFinalization", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v ZeroAmountFinalization) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *ZeroAmountFinalization) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseZeroAmountFinalization(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// LockScope names one of a customer's advisory locks (domain §6); operations that read and write a customer's balances take the matching lock.
type LockScope string

// LockScope values.
const (
	// LockScopeCoupons is "coupons": coupon application.
	LockScopeCoupons LockScope = "coupons"
	// LockScopeCreditNotes is "credit_notes": credit note credit application.
	LockScopeCreditNotes LockScope = "credit_notes"
	// LockScopePrepaidCredits is "prepaid_credits": wallet balances and transactions.
	LockScopePrepaidCredits LockScope = "prepaid_credits"
	// LockScopeBillingSchedule is "billing_schedule": the customer's billing runs.
	LockScopeBillingSchedule LockScope = "billing_schedule"
)

// LockScopeValues returns every LockScope, in declaration order.
func LockScopeValues() []LockScope {
	return []LockScope{LockScopeCoupons, LockScopeCreditNotes, LockScopePrepaidCredits, LockScopeBillingSchedule}
}

// Valid reports whether v is one of the declared LockScope values.
func (v LockScope) Valid() bool {
	switch v {
	case LockScopeCoupons, LockScopeCreditNotes, LockScopePrepaidCredits, LockScopeBillingSchedule:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v LockScope) String() string { return string(v) }

// ParseLockScope parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseLockScope(s string) (LockScope, error) {
	if v := LockScope(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("customer.LockScope", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v LockScope) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *LockScope) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseLockScope(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}
