package period

import "time"

// date is a civil date: a day of the proleptic Gregorian calendar with no
// time of day and no location. Billing periods are computed on dates in the
// subscription's location and turned into instants only at the end.
type date struct {
	year  int
	month time.Month
	day   int
}

// dateOf returns the civil date of t in its own location.
func dateOf(t time.Time) date {
	y, m, d := t.Date()
	return date{y, m, d}
}

// newDate normalizes an out-of-range month or day the way [time.Date] does.
func newDate(year int, month time.Month, day int) date {
	return dateOf(time.Date(year, month, day, 0, 0, 0, 0, time.UTC))
}

// daysIn returns the number of days of month in year.
func daysIn(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// start returns the instant d begins in loc.
func (d date) start(loc *time.Location) time.Time {
	return time.Date(d.year, d.month, d.day, 0, 0, 0, 0, loc)
}

// end returns the instant d ends in loc, which is the instant the next day
// begins.
func (d date) end(loc *time.Location) time.Time {
	return d.addDays(1).start(loc)
}

func (d date) utc() time.Time { return d.start(time.UTC) }

func (d date) addDays(n int) date { return newDate(d.year, d.month, d.day+n) }

// addMonths moves d by n months and clamps the day to the last day of the
// target month (31 Jan + 1 month = 28 or 29 Feb).
func (d date) addMonths(n int) date {
	first := newDate(d.year, d.month+time.Month(n), 1)
	return date{first.year, first.month, min(d.day, daysIn(first.year, first.month))}
}

func (d date) before(e date) bool { return d.utc().Before(e.utc()) }

func (d date) weekday() time.Weekday { return d.utc().Weekday() }

// sub returns the number of days from e to d.
func (d date) sub(e date) int { return int(d.utc().Sub(e.utc()).Hours() / 24) }

func (d date) lastDayOfMonth() bool { return d.day == daysIn(d.year, d.month) }

func (d date) endOfMonth() date { return date{d.year, d.month, daysIn(d.year, d.month)} }

// beginningOfWeek returns the Monday of d's week; weeks run Monday to Sunday.
func (d date) beginningOfWeek() date {
	return d.addDays(-((int(d.weekday()) + 6) % 7))
}

func (d date) endOfWeek() date { return d.beginningOfWeek().addDays(6) }

// beginningOfMonths returns the first day of the block of n months (3 for a
// quarter, 6 for a half year, 12 for a year) holding d, blocks starting in
// January.
func (d date) beginningOfMonths(n int) date {
	m := (int(d.month)-1)/n*n + 1
	return date{d.year, time.Month(m), 1}
}

func (d date) endOfMonths(n int) date {
	return d.beginningOfMonths(n).addMonths(n - 1).endOfMonth()
}

// prevOccurring returns the closest date strictly before d falling on wd.
func (d date) prevOccurring(wd time.Weekday) date {
	diff := (int(d.weekday()) - int(wd) + 7) % 7
	if diff == 0 {
		diff = 7
	}
	return d.addDays(-diff)
}

// nextOccurring returns the closest date strictly after d falling on wd.
func (d date) nextOccurring(wd time.Weekday) date {
	diff := (int(wd) - int(d.weekday()) + 7) % 7
	if diff == 0 {
		diff = 7
	}
	return d.addDays(diff)
}

// buildDate returns the given date, clamping the day to the month's length.
// Day 0 stands for the last day of the previous month, as in lago.
func buildDate(year int, month time.Month, day int) date {
	if day == 0 {
		day = 31
		month--
		if month == 0 {
			month = 12
			year--
		}
	}
	return date{year, month, min(day, daysIn(year, month))}
}
