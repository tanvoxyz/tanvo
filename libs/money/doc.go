// Package money provides the exact decimal arithmetic, currencies and
// minor-unit conversions every amount, price and quantity in Tanvo uses.
//
// [Decimal] is an immutable arbitrary-precision decimal. Addition,
// subtraction and multiplication are exact; division rounds to
// [DivisionPrecision] significant digits. Every other rounding is explicit:
// [Decimal.Round] takes the number of decimal places and a [RoundingMode].
//
// [Currency] describes an ISO 4217 currency, or a custom pricing unit, with
// the exponent of its minor unit; [Currency.ToMinor] and
// [Currency.FromMinor] convert between decimal amounts and integer minor
// units.
package money
