package core

import (
	"fmt"
	"time"
)

// Date is a calendar date without a time zone (domain §2.2 `Date`); it is
// interpreted in a stated time zone. The zero Date is unset.
type Date struct {
	// Year is the year, for example 2024.
	Year int
	// Month is the month of the year.
	Month time.Month
	// Day is the day of the month, starting at 1.
	Day int
}

// DateOf returns the calendar date of t on the wall clock of loc.
func DateOf(t time.Time, loc *time.Location) Date {
	y, m, d := t.In(loc).Date()
	return Date{y, m, d}
}

// NewDate returns the normalized date for year, month and day (so day 32
// of January is 1 February).
func NewDate(year int, month time.Month, day int) Date {
	y, m, d := time.Date(year, month, day, 0, 0, 0, 0, time.UTC).Date()
	return Date{y, m, d}
}

// ParseDate parses an ISO 8601 calendar date "YYYY-MM-DD".
func ParseDate(s string) (Date, error) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return Date{}, fmt.Errorf("%w: date %q", ErrInvalidValue, s)
	}
	return DateOf(t, time.UTC), nil
}

// IsZero reports whether d is unset.
func (d Date) IsZero() bool { return d == Date{} }

// Midnight returns the instant of the local midnight that starts d in loc,
// in UTC (domain §2.3).
func (d Date) Midnight(loc *time.Location) time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, loc).UTC()
}

// AddDays returns d shifted by n calendar days.
func (d Date) AddDays(n int) Date { return NewDate(d.Year, d.Month, d.Day+n) }

// Compare returns -1, 0 or +1 as d is before, equal to or after e.
func (d Date) Compare(e Date) int {
	switch {
	case d.Year != e.Year:
		return cmpInt(d.Year, e.Year)
	case d.Month != e.Month:
		return cmpInt(int(d.Month), int(e.Month))
	default:
		return cmpInt(d.Day, e.Day)
	}
}

// Before reports whether d is before e.
func (d Date) Before(e Date) bool { return d.Compare(e) < 0 }

// After reports whether d is after e.
func (d Date) After(e Date) bool { return d.Compare(e) > 0 }

// String returns "YYYY-MM-DD", or "" when unset.
func (d Date) String() string {
	if d.IsZero() {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

// MarshalText implements encoding.TextMarshaler.
func (d Date) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler; "" decodes as unset.
func (d *Date) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*d = Date{}
		return nil
	}
	p, err := ParseDate(string(b))
	if err != nil {
		return err
	}
	*d = p
	return nil
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
