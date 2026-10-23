package wallet

import (
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// CreditPlaces is the number of decimal places credits are floored to on
// input (domain §22.9).
const CreditPlaces = 5

// NormalizeCredits floors credits to [CreditPlaces] decimal places.
func NormalizeCredits(credits money.Decimal) money.Decimal {
	return credits.Round(CreditPlaces, money.Floor)
}

// AmountFromCredits returns credits × rate, half up to minor units of
// currency (domain §22.9).
func AmountFromCredits(credits money.Decimal, currency money.Currency, rate money.Decimal) (int64, error) {
	n, err := currency.ToMinor(credits.Mul(rate), money.HalfUp)
	if err != nil {
		return 0, fmt.Errorf("wallet credits: %w", err)
	}
	return n, nil
}

// CreditsFromMinor converts an amount in minor units of currency to
// credits at rate, floored to [CreditPlaces] decimal places.
func CreditsFromMinor(amount int64, currency money.Currency, rate money.Decimal) money.Decimal {
	return NormalizeCredits(currency.FromMinor(amount).Div(rate))
}

// Credit is a quantity of wallet credits with its monetary amount; build it
// with [NewCredit] or [CreditFromMinor] so the two stay consistent.
type Credit struct {
	// Credits is the number of credits.
	Credits money.Decimal
	// Amount is the value of Credits, in minor units of the wallet's
	// currency.
	Amount int64
}

// NewCredit floors credits to [CreditPlaces] places and values them at
// rate, half up to minor units. Invoiceable credits (paid top-ups) are
// then recomputed from the rounded amount, so that only whole minor units
// are bought: 1034 credits at 0.001 EUR become 1030 credits for 1.03 EUR.
func NewCredit(credits money.Decimal, currency money.Currency, rate money.Decimal, invoiceable bool) (Credit, error) {
	credits = NormalizeCredits(credits)
	amount, err := AmountFromCredits(credits, currency, rate)
	if err != nil {
		return Credit{}, err
	}
	if invoiceable {
		credits = CreditsFromMinor(amount, currency, rate)
	}
	return Credit{Credits: credits, Amount: amount}, nil
}

// CreditFromMinor builds a credit from a (possibly fractional) amount in
// minor units: the amount is rounded half up to a whole minor unit first.
func CreditFromMinor(amount money.Decimal, currency money.Currency, rate money.Decimal) (Credit, error) {
	n, ok := amount.Round(0, money.HalfUp).Int64()
	if !ok {
		return Credit{}, fmt.Errorf("wallet credits: %w: amount %s", core.ErrInvalidValue, amount)
	}
	return Credit{Credits: CreditsFromMinor(n, currency, rate), Amount: n}, nil
}

// RoundsToZero reports whether a positive number of credits is worth less
// than half a minor unit, so that a top-up of them is rejected (domain
// §22.9). Zero and negative credits do not round to zero.
func RoundsToZero(credits money.Decimal, currency money.Currency, rate money.Decimal) bool {
	credits = NormalizeCredits(credits)
	if !credits.IsPositive() {
		return false
	}
	amount, err := AmountFromCredits(credits, currency, rate)
	return err == nil && amount == 0
}
