// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

// Package currency converts the minor-unit amounts of the export views to
// decimal amounts in major units, after the ISO 4217 exponents of the
// currencies the core accepts.
package currency

import (
	"fmt"

	"github.com/cockroachdb/apd/v3"
)

// exponents lists the currencies whose minor unit is not a hundredth: the
// entries of libs/money/currencies_table.go with another exponent, which
// TestExponents checks.
var exponents = map[string]int32{
	"BHD": 3, "BIF": 0, "CLF": 4, "CLP": 0, "DJF": 0, "GNF": 0, "ISK": 0,
	"JOD": 3, "JPY": 0, "KMF": 0, "KRW": 0, "KWD": 3, "PYG": 0, "RWF": 0,
	"TND": 3, "UGX": 0, "VND": 0, "VUV": 0, "XAF": 0, "XOF": 0, "XPF": 0,
}

// Exponent returns the number of decimals of code's minor unit.
func Exponent(code string) int32 {
	if e, ok := exponents[code]; ok {
		return e
	}
	return 2
}

// dc rounds half to even with enough precision for any amount.
var dc = func() apd.Context {
	c := apd.BaseContext.WithPrecision(60)
	c.Rounding = apd.RoundHalfEven
	return *c
}()

// Major returns minor, an amount in code's minor units, in major units
// rounded half to even to the minor unit, in plain notation ("12.50").
func Major(minor *apd.Decimal, code string) (string, error) {
	e := Exponent(code)
	var v apd.Decimal
	v.Set(minor)
	v.Exponent -= e
	if _, err := dc.Quantize(&v, &v, -e); err != nil {
		return "", fmt.Errorf("currency: %s %s: %w", minor, code, err)
	}
	if v.IsZero() {
		v.Negative = false
	}
	return v.Text('f'), nil
}

// MajorString is [Major] for a decimal string, as PostgreSQL returns
// numeric values; an empty string is zero.
func MajorString(minor, code string) (string, error) {
	var d apd.Decimal
	if minor != "" {
		if _, _, err := d.SetString(minor); err != nil {
			return "", fmt.Errorf("currency: amount %q: %w", minor, err)
		}
	}
	return Major(&d, code)
}
