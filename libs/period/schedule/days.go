package schedule

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// Date is a civil date.
type Date struct {
	// Year is the year.
	Year int
	// Month is the month.
	Month time.Month
	// Day is the day of the month.
	Day int
}

// DateOf returns the date of t on the wall clock of loc.
func DateOf(t time.Time, loc *time.Location) Date {
	y, m, d := t.In(loc).Date()
	return Date{y, m, d}
}

// Start returns the instant d begins in loc: local midnight, or the end
// of the gap when a daylight saving change skips it (ActiveSupport
// beginning_of_day).
func (d Date) Start(loc *time.Location) time.Time {
	return wall(d.Year, d.Month, d.Day, 0, 0, 0, 0, loc)
}

// AddDays returns d moved by n days.
func (d Date) AddDays(n int) Date {
	y, m, day := time.Date(d.Year, d.Month, d.Day+n, 0, 0, 0, 0, time.UTC).Date()
	return Date{y, m, day}
}

// Sub returns the number of days from e to d.
func (d Date) Sub(e Date) int {
	a := time.Date(e.Year, e.Month, e.Day, 0, 0, 0, 0, time.UTC)
	b := time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
	return int(b.Sub(a).Hours() / 24)
}

// String returns the date as YYYY-MM-DD.
func (d Date) String() string {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
}

// StartOfDay returns the instant the local day of t begins in loc.
func StartOfDay(t time.Time, loc *time.Location) time.Time {
	return DateOf(t, loc).Start(loc)
}

// DaysBetween returns the whole billable days of the half-open window
// [from, to) in loc: a day belongs to the window holding its local
// midnight, so a daylight saving day counts as one and the two sides of a
// cut always sum to the whole.
//
// source: app/services/billing/days.rb:13-15
func DaysBetween(from, to time.Time, loc *time.Location) int {
	return openingDate(to, loc).Sub(openingDate(from, loc))
}

// openingDate returns the first local date whose midnight is at or after
// t: tomorrow when t is part-way through a day.
//
// source: app/services/billing/days.rb:31-35
func openingDate(t time.Time, loc *time.Location) Date {
	d := DateOf(t, loc)
	if t.Equal(d.Start(loc)) {
		return d
	}
	return d.AddDays(1)
}

// Ratio is a share of days: Days of Total. A zero Total is the share of an
// empty window, 1.
type Ratio struct {
	// Days is the numerator.
	Days int
	// Total is the denominator.
	Total int
}

// One is the whole share.
var One = Ratio{Days: 1, Total: 1}

// RatioPlaces is the scale ratios are stored at (numeric(30,10)).
const RatioPlaces = 10

// Decimal returns the share rounded half up to [RatioPlaces] places, as it
// is stored on a billing segment; a zero Total is 1.
func (r Ratio) Decimal() money.Decimal {
	if r.Total == 0 {
		return money.NewFromInt(1)
	}
	return money.NewFromInt(int64(r.Days)).Div(money.NewFromInt(int64(r.Total))).Round(RatioPlaces, money.HalfUp)
}

// Float returns the share as a float (Ruby Integer#fdiv).
func (r Ratio) Float() float64 {
	if r.Total == 0 {
		return 1
	}
	return float64(r.Days) / float64(r.Total)
}
