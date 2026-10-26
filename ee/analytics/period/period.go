// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

// Package period cuts the range of a report into days, ISO weeks or
// calendar months, and measures spans of days in calendar months.
//
// Dates are [time.Time] values at midnight UTC: the calendar day, whose
// time zone was applied before (a customer's, for invoices' issuing
// dates).
package period

import (
	"errors"
	"fmt"
	"time"
)

// Granularity is the length of the periods of a report.
type Granularity int

const (
	// Daily periods are single days.
	Daily Granularity = iota + 1
	// Weekly periods are ISO weeks, Monday to Sunday.
	Weekly
	// Monthly periods are calendar months.
	Monthly
)

// String returns the name of g.
func (g Granularity) String() string {
	switch g {
	case Daily:
		return "daily"
	case Weekly:
		return "weekly"
	case Monthly:
		return "monthly"
	}
	return fmt.Sprintf("Granularity(%d)", int(g))
}

// MaxPeriods bounds the periods of a range.
const MaxPeriods = 1000

// DefaultPeriods is the number of periods of a range without a start.
const DefaultPeriods = 12

// DefaultDailyPeriods is the number of daily periods of a range without
// a start.
const DefaultDailyPeriods = 30

// ErrRange reports a range that ends before it starts or holds more than
// [MaxPeriods] periods.
var ErrRange = errors.New("period: invalid range")

// Period is a span of days, both ends included.
type Period struct {
	// Start is the first day.
	Start time.Time
	// End is the last day.
	End time.Time
}

// Date returns the date of y, m, d.
func Date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Day returns the date of t in loc.
func Day(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return Date(y, m, d)
}

// Truncate returns the first day of the period of g holding d.
func Truncate(d time.Time, g Granularity) time.Time {
	switch g {
	case Weekly:
		// Monday is the first day: time.Sunday is 0.
		back := (int(d.Weekday()) + 6) % 7
		return d.AddDate(0, 0, -back)
	case Monthly:
		return Date(d.Year(), d.Month(), 1)
	default:
		return d
	}
}

// next returns the first day of the period of g after the one starting
// on start.
func next(start time.Time, g Granularity) time.Time {
	switch g {
	case Weekly:
		return start.AddDate(0, 0, 7)
	case Monthly:
		return start.AddDate(0, 1, 0)
	default:
		return start.AddDate(0, 0, 1)
	}
}

// DefaultFrom returns the start of a range ending on to without a start,
// so that it holds [DefaultPeriods] periods of g, or [DefaultDailyPeriods]
// daily ones.
func DefaultFrom(to time.Time, g Granularity) time.Time {
	switch g {
	case Weekly:
		return Truncate(to, Weekly).AddDate(0, 0, -7*(DefaultPeriods-1))
	case Monthly:
		return Truncate(to, Monthly).AddDate(0, -(DefaultPeriods - 1), 0)
	default:
		return to.AddDate(0, 0, -(DefaultDailyPeriods - 1))
	}
}

// Split cuts the days from from to to into the periods of g, oldest
// first. The first and the last periods are clipped to the range.
func Split(from, to time.Time, g Granularity) ([]Period, error) {
	if to.Before(from) {
		return nil, fmt.Errorf("%w: to_date %s is before from_date %s", ErrRange, to.Format(time.DateOnly), from.Format(time.DateOnly))
	}
	var out []Period
	for start := Truncate(from, g); !start.After(to); start = next(start, g) {
		if len(out) == MaxPeriods {
			return nil, fmt.Errorf("%w: more than %d %s periods", ErrRange, MaxPeriods, g)
		}
		p := Period{Start: start, End: next(start, g).AddDate(0, 0, -1)}
		if p.Start.Before(from) {
			p.Start = from
		}
		if p.End.After(to) {
			p.End = to
		}
		out = append(out, p)
	}
	return out, nil
}
