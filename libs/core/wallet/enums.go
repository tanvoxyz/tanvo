package wallet

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// Status is the lifecycle status of a wallet (domain §14.1).
type Status string

// Status values.
const (
	// StatusActive is "active": accepts transactions.
	StatusActive Status = "active"
	// StatusTerminated is "terminated": closed; no further transactions.
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
	return "", core.InvalidEnum("wallet.Status", s)
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

// TransactionType is the direction of a wallet transaction (domain §14.2).
type TransactionType string

// TransactionType values.
const (
	// TransactionTypeInbound is "inbound": adds credits.
	TransactionTypeInbound TransactionType = "inbound"
	// TransactionTypeOutbound is "outbound": removes credits.
	TransactionTypeOutbound TransactionType = "outbound"
)

// TransactionTypeValues returns every TransactionType, in declaration order.
func TransactionTypeValues() []TransactionType {
	return []TransactionType{TransactionTypeInbound, TransactionTypeOutbound}
}

// Valid reports whether v is one of the declared TransactionType values.
func (v TransactionType) Valid() bool {
	switch v {
	case TransactionTypeInbound, TransactionTypeOutbound:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v TransactionType) String() string { return string(v) }

// ParseTransactionType parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseTransactionType(s string) (TransactionType, error) {
	if v := TransactionType(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("wallet.TransactionType", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v TransactionType) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *TransactionType) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseTransactionType(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// TransactionStatus is the nature of a wallet transaction (domain §14.2): inbound transactions are purchased or granted, outbound ones invoiced or voided.
type TransactionStatus string

// TransactionStatus values.
const (
	// TransactionStatusPurchased is "purchased": paid credits, invoiced on a credit invoice.
	TransactionStatusPurchased TransactionStatus = "purchased"
	// TransactionStatusGranted is "granted": free credits.
	TransactionStatusGranted TransactionStatus = "granted"
	// TransactionStatusVoided is "voided": credits removed without consumption.
	TransactionStatusVoided TransactionStatus = "voided"
	// TransactionStatusInvoiced is "invoiced": credits consumed by an invoice.
	TransactionStatusInvoiced TransactionStatus = "invoiced"
)

// TransactionStatusValues returns every TransactionStatus, in declaration order.
func TransactionStatusValues() []TransactionStatus {
	return []TransactionStatus{TransactionStatusPurchased, TransactionStatusGranted, TransactionStatusVoided, TransactionStatusInvoiced}
}

// Valid reports whether v is one of the declared TransactionStatus values.
func (v TransactionStatus) Valid() bool {
	switch v {
	case TransactionStatusPurchased, TransactionStatusGranted, TransactionStatusVoided, TransactionStatusInvoiced:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v TransactionStatus) String() string { return string(v) }

// ParseTransactionStatus parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseTransactionStatus(s string) (TransactionStatus, error) {
	if v := TransactionStatus(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("wallet.TransactionStatus", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v TransactionStatus) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *TransactionStatus) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseTransactionStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// SettlementStatus is the settlement status of a wallet transaction (domain §14.2 `status`, WalletTransactionStatus).
type SettlementStatus string

// SettlementStatus values.
const (
	// SettlementStatusPending is "pending": awaiting the credit invoice's finalization or payment.
	SettlementStatusPending SettlementStatus = "pending"
	// SettlementStatusSettled is "settled": the credits are on the balance.
	SettlementStatusSettled SettlementStatus = "settled"
	// SettlementStatusFailed is "failed": the payment failed or the credit invoice was voided.
	SettlementStatusFailed SettlementStatus = "failed"
)

// SettlementStatusValues returns every SettlementStatus, in declaration order.
func SettlementStatusValues() []SettlementStatus {
	return []SettlementStatus{SettlementStatusPending, SettlementStatusSettled, SettlementStatusFailed}
}

// Valid reports whether v is one of the declared SettlementStatus values.
func (v SettlementStatus) Valid() bool {
	switch v {
	case SettlementStatusPending, SettlementStatusSettled, SettlementStatusFailed:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v SettlementStatus) String() string { return string(v) }

// ParseSettlementStatus parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseSettlementStatus(s string) (SettlementStatus, error) {
	if v := SettlementStatus(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("wallet.SettlementStatus", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v SettlementStatus) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *SettlementStatus) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseSettlementStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// TransactionSource tells what created a wallet transaction (domain §14.2).
type TransactionSource string

// TransactionSource values.
const (
	// TransactionSourceManual is "manual": an API or user request.
	TransactionSourceManual TransactionSource = "manual"
	// TransactionSourceInterval is "interval": an interval recurring rule.
	TransactionSourceInterval TransactionSource = "interval"
	// TransactionSourceThreshold is "threshold": a threshold recurring rule.
	TransactionSourceThreshold TransactionSource = "threshold"
)

// TransactionSourceValues returns every TransactionSource, in declaration order.
func TransactionSourceValues() []TransactionSource {
	return []TransactionSource{TransactionSourceManual, TransactionSourceInterval, TransactionSourceThreshold}
}

// Valid reports whether v is one of the declared TransactionSource values.
func (v TransactionSource) Valid() bool {
	switch v {
	case TransactionSourceManual, TransactionSourceInterval, TransactionSourceThreshold:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v TransactionSource) String() string { return string(v) }

// ParseTransactionSource parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseTransactionSource(s string) (TransactionSource, error) {
	if v := TransactionSource(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("wallet.TransactionSource", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v TransactionSource) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *TransactionSource) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseTransactionSource(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// RecurringMethod is how a recurring rule computes its top-up (domain §14.3).
type RecurringMethod string

// RecurringMethod values.
const (
	// RecurringMethodFixed is "fixed": fixed paid and granted credits.
	RecurringMethodFixed RecurringMethod = "fixed"
	// RecurringMethodTarget is "target": up to a target ongoing balance.
	RecurringMethodTarget RecurringMethod = "target"
)

// RecurringMethodValues returns every RecurringMethod, in declaration order.
func RecurringMethodValues() []RecurringMethod {
	return []RecurringMethod{RecurringMethodFixed, RecurringMethodTarget}
}

// Valid reports whether v is one of the declared RecurringMethod values.
func (v RecurringMethod) Valid() bool {
	switch v {
	case RecurringMethodFixed, RecurringMethodTarget:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v RecurringMethod) String() string { return string(v) }

// ParseRecurringMethod parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseRecurringMethod(s string) (RecurringMethod, error) {
	if v := RecurringMethod(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("wallet.RecurringMethod", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v RecurringMethod) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *RecurringMethod) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseRecurringMethod(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// RecurringTrigger is what runs a recurring rule (domain §14.3).
type RecurringTrigger string

// RecurringTrigger values.
const (
	// RecurringTriggerInterval is "interval": each interval boundary.
	RecurringTriggerInterval RecurringTrigger = "interval"
	// RecurringTriggerThreshold is "threshold": when the ongoing balance falls to the threshold.
	RecurringTriggerThreshold RecurringTrigger = "threshold"
)

// RecurringTriggerValues returns every RecurringTrigger, in declaration order.
func RecurringTriggerValues() []RecurringTrigger {
	return []RecurringTrigger{RecurringTriggerInterval, RecurringTriggerThreshold}
}

// Valid reports whether v is one of the declared RecurringTrigger values.
func (v RecurringTrigger) Valid() bool {
	switch v {
	case RecurringTriggerInterval, RecurringTriggerThreshold:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v RecurringTrigger) String() string { return string(v) }

// ParseRecurringTrigger parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseRecurringTrigger(s string) (RecurringTrigger, error) {
	if v := RecurringTrigger(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("wallet.RecurringTrigger", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v RecurringTrigger) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *RecurringTrigger) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseRecurringTrigger(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// RuleStatus is the lifecycle status of a recurring rule (domain §14.3).
type RuleStatus string

// RuleStatus values.
const (
	// RuleStatusActive is "active": runs.
	RuleStatusActive RuleStatus = "active"
	// RuleStatusTerminated is "terminated": stopped.
	RuleStatusTerminated RuleStatus = "terminated"
)

// RuleStatusValues returns every RuleStatus, in declaration order.
func RuleStatusValues() []RuleStatus {
	return []RuleStatus{RuleStatusActive, RuleStatusTerminated}
}

// Valid reports whether v is one of the declared RuleStatus values.
func (v RuleStatus) Valid() bool {
	switch v {
	case RuleStatusActive, RuleStatusTerminated:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v RuleStatus) String() string { return string(v) }

// ParseRuleStatus parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseRuleStatus(s string) (RuleStatus, error) {
	if v := RuleStatus(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("wallet.RuleStatus", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v RuleStatus) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *RuleStatus) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseRuleStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}
