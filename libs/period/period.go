package period

import (
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// Period is the half-open interval of instants [From, To).
//
// A period ending at the end of a day has To at the next local midnight; a
// period cut by a termination at instant T has To = T, so T itself is
// outside the period.
type Period struct {
	From time.Time
	To   time.Time
}

// IsZero reports whether p is the zero Period.
func (p Period) IsZero() bool { return p.From.IsZero() && p.To.IsZero() }

// Empty reports whether p contains no instant.
func (p Period) Empty() bool { return !p.From.Before(p.To) }

// Contains reports whether t is in p.
func (p Period) Contains(t time.Time) bool { return !t.Before(p.From) && t.Before(p.To) }

// Days returns the number of days p spans in loc, see [DaysBetween].
func (p Period) Days(loc *time.Location) int { return DaysBetween(p.From, p.To, loc) }

// UTC returns p with both ends in UTC.
func (p Period) UTC() Period { return Period{p.From.UTC(), p.To.UTC()} }

// String formats p as "[from, to)" in RFC 3339.
func (p Period) String() string {
	return fmt.Sprintf("[%s, %s)", p.From.Format(time.RFC3339Nano), p.To.Format(time.RFC3339Nano))
}

// DaysBetween returns the number of days from from to the exclusive end to,
// measured on the wall clock of loc and rounded up: a partial day counts as a
// whole day, and a day made 23 or 25 hours long by a daylight saving change
// still counts as one day.
//
// From local midnight to local midnight it is the number of calendar days.
// It may be zero or negative when to is not after from.
func DaysBetween(from, to time.Time, loc *time.Location) int {
	loc = orUTC(loc)
	_, fromOffset := from.In(loc).Zone()
	_, toOffset := to.In(loc).Zone()
	wall := to.Sub(from) + time.Duration(toOffset-fromOffset)*time.Second
	const day = 24 * time.Hour
	days := wall / day
	if wall%day > 0 {
		days++
	}
	return int(days)
}

// ProrationRatio returns days / total as an exact decimal (rounded to
// [money.DivisionPrecision] significant digits when it does not terminate).
// It returns zero when total is not positive.
func ProrationRatio(days, total int) money.Decimal {
	if total <= 0 {
		return money.Zero
	}
	return money.NewFromInt(int64(days)).Div(money.NewFromInt(int64(total)))
}

func orUTC(loc *time.Location) *time.Location {
	if loc == nil {
		return time.UTC
	}
	return loc
}
