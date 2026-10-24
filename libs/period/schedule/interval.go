package schedule

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// ErrInvalid is the error of a schedule that cannot be built from its
// inputs (lago's ArgumentError, answered as invalid_billing_schedule).
var ErrInvalid = errors.New("invalid billing schedule")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Unit is the unit of an [Interval].
type Unit string

// Unit values.
const (
	// Day is "day".
	Day Unit = "day"
	// Week is "week".
	Week Unit = "week"
	// Month is "month".
	Month Unit = "month"
	// Year is "year".
	Year Unit = "year"
)

// Units returns every unit, in declaration order.
func Units() []Unit { return []Unit{Day, Week, Month, Year} }

// Interval is a cadence: how far one billing period runs.
type Interval struct {
	// Count is the number of units, ≥ 1.
	Count int
	// Unit is the unit.
	Unit Unit
}

// NewInterval returns the interval count × unit, failing with [ErrInvalid]
// for an unknown unit or a count that is not positive.
//
// source: app/services/billing/interval.rb:23-30
func NewInterval(count int, unit Unit) (Interval, error) {
	if !slices.Contains(Units(), unit) {
		return Interval{}, invalid("unknown interval unit %q, expected one of day, week, month, year", unit)
	}
	if count <= 0 {
		return Interval{}, invalid("interval count must be a positive integer, got %d", count)
	}
	return Interval{Count: count, Unit: unit}, nil
}

// IntervalOf returns the cadence a rate bills on, with the override laid
// over it field by field: either override field alone wins.
//
// source: app/services/billing/interval.rb:17-22
func IntervalOf(r *Rate, o *Override) (Interval, error) {
	count, unit := r.IntervalCount, r.IntervalUnit
	if o != nil {
		if o.IntervalCount != nil {
			count = *o.IntervalCount
		}
		if o.IntervalUnit != "" {
			unit = o.IntervalUnit
		}
	}
	return NewInterval(count, unit)
}

// Advance moves t by steps whole intervals (steps may be negative) on the
// wall clock of t's location, clamping month ends: 31 Jan + 1 month is
// 28 Feb. Each step is derived from t, so month ends do not drift.
//
// source: app/services/billing/interval.rb:39-48
func (i Interval) Advance(t time.Time, steps int) time.Time {
	n := i.Count * steps
	y, m, d := t.Date()
	switch i.Unit {
	case Day:
		d += n
	case Week:
		d += 7 * n
	case Month:
		y, m, d = addMonths(y, m, d, n)
	case Year:
		y, m, d = addMonths(y, m, d, 12*n)
	}
	h, mi, s := t.Clock()
	return wall(y, m, d, h, mi, s, t.Nanosecond(), t.Location())
}

// StepsBetween returns the whole intervals between from and to, which may
// be negative: from advanced by the answer never passes to, and one more
// step does. Dates are read in each instant's own location.
//
// source: app/services/billing/interval.rb:61-69
func (i Interval) StepsBetween(from, to time.Time) int {
	estimate := i.calendarStepsBetween(from, to)
	if i.Advance(from, estimate).After(to) {
		return estimate - 1
	}
	return estimate
}

// calendarStepsBetween counts whole units by the calendar, then how many
// intervals fit in them; it overshoots by at most one.
//
// source: app/services/billing/interval.rb:80-89
func (i Interval) calendarStepsBetween(from, to time.Time) int {
	var units int
	switch i.Unit {
	case Day:
		units = daysApart(from, to)
	case Week:
		units = floorDiv(daysApart(from, to), 7)
	case Month:
		units = (to.Year()-from.Year())*12 + int(to.Month()-from.Month())
	case Year:
		units = to.Year() - from.Year()
	}
	return floorDiv(units, i.Count)
}

// daysApart returns to's date minus from's date, each in its own location.
func daysApart(from, to time.Time) int {
	fy, fm, fd := from.Date()
	ty, tm, td := to.Date()
	a := time.Date(fy, fm, fd, 0, 0, 0, 0, time.UTC)
	b := time.Date(ty, tm, td, 0, 0, 0, 0, time.UTC)
	return int(b.Sub(a).Hours() / 24)
}

// floorDiv divides rounding toward negative infinity, as Ruby's Integer#/.
func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// addMonths moves the date by n months and clamps the day to the target
// month's last day (Ruby Date#>>).
func addMonths(y int, m time.Month, d, n int) (int, time.Month, int) {
	total := y*12 + int(m-1) + n
	ny, nm := floorDiv(total, 12), time.Month(total-floorDiv(total, 12)*12+1)
	if last := time.Date(ny, nm+1, 0, 0, 0, 0, 0, time.UTC).Day(); d > last {
		d = last
	}
	return ny, nm, d
}

// wall returns the instant of a wall-clock time in loc. A time skipped by
// a daylight saving change moves forward one hour until it exists, as
// ActiveSupport::TimeWithZone resolves it.
func wall(y int, m time.Month, d, h, mi, s, ns int, loc *time.Location) time.Time {
	want := time.Date(y, m, d, h, mi, s, ns, time.UTC)
	for range 48 {
		t := time.Date(want.Year(), want.Month(), want.Day(), want.Hour(), want.Minute(), want.Second(), want.Nanosecond(), loc)
		if sameWall(t, want) {
			return t
		}
		want = want.Add(time.Hour)
	}
	return time.Date(y, m, d, h, mi, s, ns, loc)
}

func sameWall(t, want time.Time) bool {
	ty, tm, td := t.Date()
	wy, wm, wd := want.Date()
	return ty == wy && tm == wm && td == wd && t.Hour() == want.Hour() && t.Minute() == want.Minute()
}
