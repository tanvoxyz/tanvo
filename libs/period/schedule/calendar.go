package schedule

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/period"
)

// Calendar is a ruler: boundaries every interval from the start of the
// anchor day in loc. Anchored 1 Feb in America/New_York, boundary 0 is
// 1 Feb 05:00 UTC. Boundary i is derived from the anchor, never from
// boundary i-1, so clamped month ends return to the anchor's day.
type Calendar struct {
	anchorDate Date
	anchor     time.Time
	interval   Interval
	loc        *time.Location
	boundaries map[int]time.Time
}

// NewCalendar returns the calendar of interval anchored on anchor in loc.
//
// source: app/services/billing/calendar.rb:9-14
func NewCalendar(anchor Date, interval Interval, loc *time.Location) *Calendar {
	if loc == nil {
		loc = time.UTC
	}
	return &Calendar{
		anchorDate: anchor,
		anchor:     anchor.Start(loc),
		interval:   interval,
		loc:        loc,
		boundaries: map[int]time.Time{},
	}
}

// AnchorDate returns the anchor day.
func (c *Calendar) AnchorDate() Date { return c.anchorDate }

// Interval returns the cadence.
func (c *Calendar) Interval() Interval { return c.interval }

// IntervalContaining returns the half-open interval t falls in.
//
// source: app/services/billing/calendar.rb:17-21
func (c *Calendar) IntervalContaining(t time.Time) period.Period {
	i := c.index(t)
	return period.Period{From: c.boundary(i), To: c.boundary(i + 1)}
}

// IntervalsBetween returns how many intervals separate from and to; it is
// signed and additive.
//
// source: app/services/billing/calendar.rb:29-31
func (c *Calendar) IntervalsBetween(from, to time.Time) int {
	return c.index(to) - c.index(from)
}

// BoundaryAfter returns the boundary steps intervals after the one
// containing t.
//
// source: app/services/billing/calendar.rb:37-39
func (c *Calendar) BoundaryAfter(t time.Time, steps int) time.Time {
	return c.boundary(c.index(t) + steps)
}

// BoundaryAtOrAfter returns the first boundary at or after t.
//
// source: app/services/billing/calendar.rb:46-50
func (c *Calendar) BoundaryAtOrAfter(t time.Time) time.Time {
	w := c.IntervalContaining(t)
	if w.From.Equal(t) {
		return w.From
	}
	return w.To
}

// ProrationRatio returns the share of its containing interval that the
// window [from, to) covers, in days: the denominator is the interval
// containing from. A window ending before it starts, or crossing the end
// of that interval, fails with [ErrInvalid].
//
// source: app/services/billing/calendar.rb:59-66
func (c *Calendar) ProrationRatio(from, to time.Time) (Ratio, error) {
	if to.Before(from) {
		return Ratio{}, invalid("window end %s precedes its start %s", to.UTC(), from.UTC())
	}
	w := c.IntervalContaining(from)
	if to.After(w.To) {
		return Ratio{}, invalid("window [%s, %s) crosses the boundary at %s", from.UTC(), to.UTC(), w.To.UTC())
	}
	return Ratio{Days: DaysBetween(from, to, c.loc), Total: DaysBetween(w.From, w.To, c.loc)}, nil
}

// index returns which interval t falls in; instants before the anchor
// have negative indices.
//
// source: app/services/billing/calendar.rb:74-76
func (c *Calendar) index(t time.Time) int {
	return c.interval.StepsBetween(c.anchor, t.In(c.loc))
}

func (c *Calendar) boundary(i int) time.Time {
	if b, ok := c.boundaries[i]; ok {
		return b
	}
	b := c.interval.Advance(c.anchor, i)
	c.boundaries[i] = b
	return b
}
