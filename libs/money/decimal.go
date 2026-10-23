package money

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/cockroachdb/apd/v3"
)

// DivisionPrecision is the number of significant digits a quotient keeps.
const DivisionPrecision = 40

// RoundingMode selects how [Decimal.Round] rounds.
type RoundingMode int

const (
	// HalfUp rounds to the nearest neighbor, ties away from zero (1.5 → 2,
	// -1.5 → -2). It is the default mode for amounts.
	HalfUp RoundingMode = iota
	// HalfEven rounds to the nearest neighbor, ties to the even neighbor
	// (banker's rounding).
	HalfEven
	// HalfDown rounds to the nearest neighbor, ties toward zero.
	HalfDown
	// Up rounds away from zero.
	Up
	// Down rounds toward zero (truncation).
	Down
	// Ceiling rounds toward positive infinity.
	Ceiling
	// Floor rounds toward negative infinity.
	Floor
)

func (m RoundingMode) apd() apd.Rounder {
	switch m {
	case HalfEven:
		return apd.RoundHalfEven
	case HalfDown:
		return apd.RoundHalfDown
	case Up:
		return apd.RoundUp
	case Down:
		return apd.RoundDown
	case Ceiling:
		return apd.RoundCeiling
	case Floor:
		return apd.RoundFloor
	default:
		return apd.RoundHalfUp
	}
}

// String returns the mode's name.
func (m RoundingMode) String() string {
	switch m {
	case HalfUp:
		return "half_up"
	case HalfEven:
		return "half_even"
	case HalfDown:
		return "half_down"
	case Up:
		return "up"
	case Down:
		return "down"
	case Ceiling:
		return "ceiling"
	case Floor:
		return "floor"
	default:
		return fmt.Sprintf("RoundingMode(%d)", int(m))
	}
}

// exact performs operations whose results are exact (precision 0 means no
// rounding); quotient performs divisions.
var (
	exact = apd.Context{
		MaxExponent: apd.MaxExponent,
		MinExponent: apd.MinExponent,
		Traps:       apd.DefaultTraps,
	}
	quotient = apd.Context{
		Precision:   DivisionPrecision,
		MaxExponent: apd.MaxExponent,
		MinExponent: apd.MinExponent,
		Traps:       apd.DefaultTraps,
		Rounding:    apd.RoundHalfUp,
	}
)

// Decimal is an immutable arbitrary-precision decimal number. The zero value
// is 0. Decimals are compared with [Decimal.Cmp] or [Decimal.Equal], never
// with ==, since 1.0 and 1.00 are equal with different representations.
type Decimal struct {
	v apd.Decimal
}

// Common values.
var (
	Zero    = Decimal{}
	One     = NewFromInt(1)
	Hundred = NewFromInt(100)
)

// New returns coefficient × 10^exponent.
func New(coefficient int64, exponent int32) Decimal {
	var d Decimal
	d.v.SetFinite(coefficient, exponent)
	return d
}

// NewFromInt returns n.
func NewFromInt(n int64) Decimal {
	return New(n, 0)
}

// Parse parses a decimal in plain or scientific notation ("12.5", "-3",
// "1e-3"). NaN and infinities are rejected.
func Parse(s string) (Decimal, error) {
	s = strings.TrimSpace(s)
	v, _, err := apd.NewFromString(s)
	if err != nil {
		return Decimal{}, fmt.Errorf("money: parse %q: %w", s, err)
	}
	if v.Form != apd.Finite {
		return Decimal{}, fmt.Errorf("money: parse %q: not a finite number", s)
	}
	return norm(Decimal{v: *v}), nil
}

// MustParse is like [Parse] but panics on an invalid input. It is meant for
// constants and tests.
func MustParse(s string) Decimal {
	d, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return d
}

// NewFromFloat converts f using its shortest decimal representation, so that
// 0.1 becomes exactly 0.1. It is meant for values that arrive as floats from
// outside, such as JSON numbers decoded into float64; it fails for NaN and
// infinities.
func NewFromFloat(f float64) (Decimal, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return Decimal{}, fmt.Errorf("money: %v is not a finite number", f)
	}
	var d Decimal
	if _, err := d.v.SetFloat64(f); err != nil {
		return Decimal{}, fmt.Errorf("money: convert %v: %w", f, err)
	}
	return norm(d), nil
}

func must(_ apd.Condition, err error) {
	if err != nil {
		panic(fmt.Sprintf("money: %v", err))
	}
}

// norm clears the sign of a zero result, so that no operation yields -0.
func norm(r Decimal) Decimal {
	if r.v.IsZero() {
		r.v.Negative = false
	}
	return r
}

// Add returns d + e.
func (d Decimal) Add(e Decimal) Decimal {
	var r Decimal
	must(exact.Add(&r.v, &d.v, &e.v))
	return norm(r)
}

// Sub returns d - e.
func (d Decimal) Sub(e Decimal) Decimal {
	var r Decimal
	must(exact.Sub(&r.v, &d.v, &e.v))
	return norm(r)
}

// Mul returns d × e.
func (d Decimal) Mul(e Decimal) Decimal {
	var r Decimal
	must(exact.Mul(&r.v, &d.v, &e.v))
	return norm(r)
}

// Div returns d ÷ e rounded half up to [DivisionPrecision] significant
// digits, with trailing zeros removed. It panics if e is zero; callers check
// [Decimal.IsZero] first, as with math/big.
func (d Decimal) Div(e Decimal) Decimal {
	if e.IsZero() {
		panic("money: division by zero")
	}
	var r Decimal
	must(quotient.Quo(&r.v, &d.v, &e.v))
	r.v.Reduce(&r.v)
	return norm(r)
}

// DivOrZero returns d ÷ e, or zero when e is zero.
func (d Decimal) DivOrZero(e Decimal) Decimal {
	if e.IsZero() {
		return Zero
	}
	return d.Div(e)
}

// Neg returns -d.
func (d Decimal) Neg() Decimal {
	var r Decimal
	r.v.Neg(&d.v)
	return norm(r)
}

// Abs returns |d|.
func (d Decimal) Abs() Decimal {
	var r Decimal
	r.v.Abs(&d.v)
	return norm(r)
}

// Pow returns d^n for an integer n ≥ 0.
func (d Decimal) Pow(n int) Decimal {
	if n < 0 {
		panic("money: negative exponent")
	}
	r := One
	for range n {
		r = r.Mul(d)
	}
	return norm(r)
}

// Round rounds d to places decimal places (negative places round to tens,
// hundreds, …) with mode.
func (d Decimal) Round(places int32, mode RoundingMode) Decimal {
	if d.IsZero() {
		return New(0, -places)
	}
	// Quantize needs a precision large enough for the result's digits.
	digits := d.v.NumDigits() + int64(d.v.Exponent) + int64(places)
	ctx := exact
	ctx.Precision = uint32(max(digits, 0) + 1)
	ctx.Rounding = mode.apd()
	var r Decimal
	must(ctx.Quantize(&r.v, &d.v, -places))
	return norm(r)
}

// Truncate rounds toward zero to places decimal places.
func (d Decimal) Truncate(places int32) Decimal {
	return d.Round(places, Down)
}

// Reduce removes trailing zeros: 1.500 becomes 1.5 and 100 stays 100 (it is
// never written in exponent form by [Decimal.String]).
func (d Decimal) Reduce() Decimal {
	var r Decimal
	r.v.Reduce(&d.v)
	return norm(r)
}

// Cmp compares d and e: -1 if d < e, 0 if d == e, +1 if d > e.
func (d Decimal) Cmp(e Decimal) int {
	return d.v.Cmp(&e.v)
}

// Equal reports whether d and e have the same value.
func (d Decimal) Equal(e Decimal) bool { return d.Cmp(e) == 0 }

// LessThan reports whether d < e.
func (d Decimal) LessThan(e Decimal) bool { return d.Cmp(e) < 0 }

// LessThanOrEqual reports whether d ≤ e.
func (d Decimal) LessThanOrEqual(e Decimal) bool { return d.Cmp(e) <= 0 }

// GreaterThan reports whether d > e.
func (d Decimal) GreaterThan(e Decimal) bool { return d.Cmp(e) > 0 }

// GreaterThanOrEqual reports whether d ≥ e.
func (d Decimal) GreaterThanOrEqual(e Decimal) bool { return d.Cmp(e) >= 0 }

// Sign returns -1, 0 or +1.
func (d Decimal) Sign() int { return d.v.Sign() }

// IsZero reports whether d is 0.
func (d Decimal) IsZero() bool { return d.v.IsZero() }

// IsNegative reports whether d < 0.
func (d Decimal) IsNegative() bool { return d.Sign() < 0 }

// IsPositive reports whether d > 0.
func (d Decimal) IsPositive() bool { return d.Sign() > 0 }

// IsInteger reports whether d has no fractional part.
func (d Decimal) IsInteger() bool {
	var r apd.Decimal
	r.Reduce(&d.v)
	return r.Exponent >= 0
}

// Min returns the smallest of the values.
func Min(first Decimal, rest ...Decimal) Decimal {
	m := first
	for _, d := range rest {
		if d.LessThan(m) {
			m = d
		}
	}
	return m
}

// Max returns the largest of the values.
func Max(first Decimal, rest ...Decimal) Decimal {
	m := first
	for _, d := range rest {
		if d.GreaterThan(m) {
			m = d
		}
	}
	return m
}

// Sum returns the sum of the values, 0 for none.
func Sum(values ...Decimal) Decimal {
	s := Zero
	for _, d := range values {
		s = s.Add(d)
	}
	return s
}

// Int64 returns the integer part of d (truncated toward zero) and whether it
// fits in an int64.
func (d Decimal) Int64() (int64, bool) {
	t := d.Truncate(0)
	n, err := t.v.Int64()
	return n, err == nil
}

// Float64 returns the nearest float64. It is meant for statistics and
// display, never for amounts.
func (d Decimal) Float64() float64 {
	f, err := d.v.Float64()
	if err != nil {
		// Values beyond float64's range.
		if d.IsNegative() {
			return math.Inf(-1)
		}
		return math.Inf(1)
	}
	return f
}

// Exponent returns the exponent of d's representation: -2 for 1.50.
func (d Decimal) Exponent() int32 { return d.v.Exponent }

// String returns d in plain notation, keeping its representation's scale:
// "1.50", "-3", "0.001", "1000000".
func (d Decimal) String() string {
	return d.v.Text('f')
}

// StringFixed returns d rounded half up to places decimal places in plain
// notation, padded with zeros: StringFixed(2) of 1.5 is "1.50".
func (d Decimal) StringFixed(places int32) string {
	return d.Round(places, HalfUp).String()
}

// MarshalText implements encoding.TextMarshaler.
func (d Decimal) MarshalText() ([]byte, error) {
	return []byte(d.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Decimal) UnmarshalText(b []byte) error {
	v, err := Parse(string(b))
	if err != nil {
		return err
	}
	*d = v
	return nil
}

// MarshalJSON writes d as a JSON string, which keeps its exact value in every
// JSON parser.
func (d Decimal) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// UnmarshalJSON accepts a JSON string or a JSON number.
func (d *Decimal) UnmarshalJSON(b []byte) error {
	s := string(b)
	if s == "null" {
		return errors.New("money: decimal is null")
	}
	if strings.HasPrefix(s, `"`) {
		if err := json.Unmarshal(b, &s); err != nil {
			return fmt.Errorf("money: %w", err)
		}
	}
	return d.UnmarshalText([]byte(s))
}

// Value implements driver.Valuer, writing d as text for numeric columns.
func (d Decimal) Value() (driver.Value, error) {
	return d.String(), nil
}

// Scan implements sql.Scanner for numeric columns read as text, bytes,
// integers or floats.
func (d *Decimal) Scan(src any) error {
	switch v := src.(type) {
	case string:
		return d.UnmarshalText([]byte(v))
	case []byte:
		return d.UnmarshalText(v)
	case int64:
		*d = NewFromInt(v)
		return nil
	case float64:
		f, err := NewFromFloat(v)
		if err != nil {
			return err
		}
		*d = f
		return nil
	case nil:
		return errors.New("money: scan NULL into Decimal")
	default:
		return fmt.Errorf("money: cannot scan %T into Decimal", src)
	}
}
