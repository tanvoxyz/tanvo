package money

import (
	"errors"
	"fmt"
	"iter"
	"math"
	"regexp"
	"slices"
	"strings"
)

// Currency is an ISO 4217 currency or a custom pricing unit. The zero value
// is not a valid currency.
type Currency struct {
	code     string
	numeric  string
	exponent int32
	name     string
	custom   bool
}

// ErrUnknownCurrency is returned for a code that is not an accepted ISO 4217
// currency.
var ErrUnknownCurrency = errors.New("money: unknown currency")

// LookupCurrency returns the ISO 4217 currency with the given code; case is
// ignored.
func LookupCurrency(code string) (Currency, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	i, ok := slices.BinarySearchFunc(isoCurrencies, code, func(c Currency, code string) int {
		return strings.Compare(c.code, code)
	})
	if !ok {
		return Currency{}, fmt.Errorf("%w: %q", ErrUnknownCurrency, code)
	}
	return isoCurrencies[i], nil
}

// MustCurrency is like [LookupCurrency] but panics for an unknown code. It is
// meant for constants and tests.
func MustCurrency(code string) Currency {
	c, err := LookupCurrency(code)
	if err != nil {
		panic(err)
	}
	return c
}

// Currencies returns every accepted ISO 4217 currency in code order.
func Currencies() iter.Seq[Currency] {
	return slices.Values(isoCurrencies)
}

var customCode = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

// NewCustomCurrency returns a custom pricing unit (credits, tokens, …) with
// the given code, name and minor-unit exponent (0 to 15).
func NewCustomCurrency(code, name string, exponent int32) (Currency, error) {
	if !customCode.MatchString(code) {
		return Currency{}, fmt.Errorf("money: invalid pricing unit code %q", code)
	}
	if exponent < 0 || exponent > 15 {
		return Currency{}, fmt.Errorf("money: pricing unit exponent %d out of range [0, 15]", exponent)
	}
	return Currency{code: code, exponent: exponent, name: name, custom: true}, nil
}

// Code returns the currency's code ("EUR", or a pricing unit's code).
func (c Currency) Code() string { return c.code }

// Numeric returns the ISO 4217 numeric code ("978"), empty for pricing units.
func (c Currency) Numeric() string { return c.numeric }

// Name returns the currency's English name.
func (c Currency) Name() string { return c.name }

// Exponent returns the number of decimal places of the minor unit: 2 for
// EUR, 0 for JPY, 3 for KWD.
func (c Currency) Exponent() int32 { return c.exponent }

// IsCustom reports whether c is a custom pricing unit.
func (c Currency) IsCustom() bool { return c.custom }

// IsZero reports whether c is the zero Currency.
func (c Currency) IsZero() bool { return c.code == "" }

// String returns the code.
func (c Currency) String() string { return c.code }

// Round rounds amount to the currency's minor unit with mode.
func (c Currency) Round(amount Decimal, mode RoundingMode) Decimal {
	return amount.Round(c.exponent, mode)
}

// SubunitToUnit returns 10^exponent, the number of minor units in one unit.
func (c Currency) SubunitToUnit() Decimal {
	return New(1, c.exponent)
}

// ErrOverflow is returned when a minor-unit amount does not fit in an int64.
var ErrOverflow = errors.New("money: amount overflows int64 minor units")

// ToMinor converts a decimal amount in units to minor units, rounding with
// mode: 12.345 EUR is 1235 with HalfUp.
func (c Currency) ToMinor(amount Decimal, mode RoundingMode) (int64, error) {
	minor := amount.Mul(c.SubunitToUnit()).Round(0, mode)
	n, ok := minor.Int64()
	if !ok || n == math.MinInt64 {
		return 0, fmt.Errorf("%w: %s %s", ErrOverflow, amount, c.code)
	}
	return n, nil
}

// MinorToUnits converts a decimal amount of minor units (a "precise amount
// in cents", which may carry fractions of a minor unit) to units.
func (c Currency) MinorToUnits(minor Decimal) Decimal {
	return minor.Div(c.SubunitToUnit())
}

// FromMinor converts minor units to a decimal amount in units: 1235 EUR minor
// units is 12.35.
func (c Currency) FromMinor(minor int64) Decimal {
	return New(minor, -c.exponent)
}
