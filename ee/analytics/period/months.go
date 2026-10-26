// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package period

import "time"

// Months measures the days from from (included) to to (excluded) in
// calendar months, as whole months and a fraction: whole months are
// stepped from from, keeping its day of the month (the last day of a
// shorter month stands for it), and the days left over are divided by the
// length of the month that follows the last whole month. A span from
// January 15 to March 1, 2026 is one month and 14/28 (the 14 days from
// February 15 over the 28 days to March 15). It returns the whole months,
// the days left over and the days of the following month; a span that
// does not end after it starts is zero months.
func Months(from, to time.Time) (whole, days, monthDays int) {
	if !to.After(from) {
		return 0, 0, 0
	}
	// Start from an estimate and correct it, the estimate being off by one
	// at most.
	whole = (to.Year()-from.Year())*12 + int(to.Month()) - int(from.Month())
	for whole > 0 && addMonths(from, whole).After(to) {
		whole--
	}
	for !addMonths(from, whole+1).After(to) {
		whole++
	}
	start := addMonths(from, whole)
	end := addMonths(from, whole+1)
	return whole, dayCount(start, to), dayCount(start, end)
}

// addMonths returns the date n months after d, on d's day of the month or
// on the last day of a shorter month.
func addMonths(d time.Time, n int) time.Time {
	first := Date(d.Year(), d.Month(), 1).AddDate(0, n, 0)
	last := first.AddDate(0, 1, -1).Day()
	return Date(first.Year(), first.Month(), min(d.Day(), last))
}

// dayCount returns the days from a to b.
func dayCount(a, b time.Time) int {
	return int(b.Sub(a).Hours() / 24)
}
