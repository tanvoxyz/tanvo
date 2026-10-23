package alert

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// Type is what an alert monitors (domain §19.1).
type Type string

// Type values.
const (
	// TypeCurrentUsageAmount is "current_usage_amount": the current-period usage amount of a subscription.
	TypeCurrentUsageAmount Type = "current_usage_amount"
	// TypeBillableMetricCurrentUsageAmount is "billable_metric_current_usage_amount": the current-period usage amount of one metric.
	TypeBillableMetricCurrentUsageAmount Type = "billable_metric_current_usage_amount"
	// TypeBillableMetricCurrentUsageUnits is "billable_metric_current_usage_units": the current-period units of one metric.
	TypeBillableMetricCurrentUsageUnits Type = "billable_metric_current_usage_units"
	// TypeLifetimeUsageAmount is "lifetime_usage_amount": the lifetime usage amount of a subscription.
	TypeLifetimeUsageAmount Type = "lifetime_usage_amount"
	// TypeBillableMetricLifetimeUsageUnits is "billable_metric_lifetime_usage_units": the lifetime units of one metric.
	TypeBillableMetricLifetimeUsageUnits Type = "billable_metric_lifetime_usage_units"
	// TypeWalletBalanceAmount is "wallet_balance_amount": a wallet's balance amount.
	TypeWalletBalanceAmount Type = "wallet_balance_amount"
	// TypeWalletCreditsBalance is "wallet_credits_balance": a wallet's credits balance.
	TypeWalletCreditsBalance Type = "wallet_credits_balance"
	// TypeWalletOngoingBalanceAmount is "wallet_ongoing_balance_amount": a wallet's ongoing balance amount.
	TypeWalletOngoingBalanceAmount Type = "wallet_ongoing_balance_amount"
	// TypeWalletCreditsOngoingBalance is "wallet_credits_ongoing_balance": a wallet's ongoing credits balance.
	TypeWalletCreditsOngoingBalance Type = "wallet_credits_ongoing_balance"
)

// TypeValues returns every Type, in declaration order.
func TypeValues() []Type {
	return []Type{TypeCurrentUsageAmount, TypeBillableMetricCurrentUsageAmount, TypeBillableMetricCurrentUsageUnits, TypeLifetimeUsageAmount, TypeBillableMetricLifetimeUsageUnits, TypeWalletBalanceAmount, TypeWalletCreditsBalance, TypeWalletOngoingBalanceAmount, TypeWalletCreditsOngoingBalance}
}

// Valid reports whether v is one of the declared Type values.
func (v Type) Valid() bool {
	switch v {
	case TypeCurrentUsageAmount, TypeBillableMetricCurrentUsageAmount, TypeBillableMetricCurrentUsageUnits, TypeLifetimeUsageAmount, TypeBillableMetricLifetimeUsageUnits, TypeWalletBalanceAmount, TypeWalletCreditsBalance, TypeWalletOngoingBalanceAmount, TypeWalletCreditsOngoingBalance:
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
	return "", core.InvalidEnum("alert.Type", s)
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

// Direction is the direction in which an alert's thresholds are crossed (domain §19.1).
type Direction string

// Direction values.
const (
	// DirectionIncreasing is "increasing": the value rises through a threshold.
	DirectionIncreasing Direction = "increasing"
	// DirectionDecreasing is "decreasing": the value falls through a threshold.
	DirectionDecreasing Direction = "decreasing"
)

// DirectionValues returns every Direction, in declaration order.
func DirectionValues() []Direction {
	return []Direction{DirectionIncreasing, DirectionDecreasing}
}

// Valid reports whether v is one of the declared Direction values.
func (v Direction) Valid() bool {
	switch v {
	case DirectionIncreasing, DirectionDecreasing:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v Direction) String() string { return string(v) }

// ParseDirection parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseDirection(s string) (Direction, error) {
	if v := Direction(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("alert.Direction", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v Direction) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *Direction) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseDirection(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}
