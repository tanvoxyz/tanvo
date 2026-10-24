package schedule

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/period"
)

// Schedule answers when a rate card's segments fall due.
type Schedule struct {
	rates    []*Rate
	terms    Terms
	loc      *time.Location
	resumeAt *time.Time
	walker   *Walker
}

// Params are the inputs of [New].
type Params struct {
	// Rates are the card's rates, in any order.
	Rates []*Rate
	// Terms are the card's timing and proration.
	Terms Terms
	// Phases are the card's phases, the last unbounded.
	Phases []Phase
	// StartsAt is the card's start; the walk starts at its local day.
	StartsAt time.Time
	// EndsAt is the end of billing; nil when open-ended.
	EndsAt *time.Time
	// Anchor is the card's billing anchor date.
	Anchor Date
	// Location is the customer's time zone.
	Location *time.Location
	// ResumeAt is the start of the latest persisted cycle; the walk
	// resumes there instead of at the start. Nil walks from the start.
	ResumeAt *time.Time
}

// New returns the schedule, failing with [ErrInvalid] when the walk
// cannot be built or ResumeAt precedes the card's start.
//
// source: app/services/billing/rate_cards/schedule.rb:6-17
func New(p Params) (*Schedule, error) {
	loc := p.Location
	if loc == nil {
		loc = time.UTC
	}
	if p.ResumeAt != nil && p.ResumeAt.Before(StartOfDay(p.StartsAt, loc)) {
		return nil, invalid("resume_at %s precedes the card's start", p.ResumeAt.UTC())
	}
	w, err := NewWalker(p.Rates, p.Phases, p.StartsAt, p.Anchor, loc, p.EndsAt)
	if err != nil {
		return nil, err
	}
	return &Schedule{rates: p.Rates, terms: p.Terms, loc: loc, resumeAt: p.ResumeAt, walker: w}, nil
}

// SegmentsDueBy returns the priced segments due at or before t, from the
// resume point; a rate change can make a segment due before its cycle
// ends.
//
// source: app/services/billing/rate_cards/schedule.rb:20-26
func (s *Schedule) SegmentsDueBy(t time.Time) []Segment {
	var out []Segment
	for _, seg := range s.segmentsOf(s.walker.WalkTo(t, s.resumeAt)) {
		if !seg.BillingAt.After(t) {
			out = append(out, seg)
		}
	}
	return out
}

// NextBillingAt returns the next billing instant strictly after after;
// false when billing has ended.
//
// source: app/services/billing/rate_cards/schedule.rb:29-44
func (s *Schedule) NextBillingAt(after time.Time) (time.Time, bool) {
	c := s.walker.Resume(&after)
	for c != nil {
		for _, seg := range s.segmentsOf([]*Cycle{c}) {
			if seg.BillingAt.After(after) {
				return seg.BillingAt, true
			}
		}
		c = s.walker.Advance()
	}
	return time.Time{}, false
}

// BillingAtCovering returns the billing instant of the segment serving t,
// or the next one when pricing has not started; false when billing has
// ended.
//
// source: app/services/billing/rate_cards/schedule.rb:47-51
func (s *Schedule) BillingAtCovering(t time.Time) (time.Time, bool) {
	if seg := s.SegmentCovering(t); seg != nil {
		return seg.BillingAt, true
	}
	return s.NextBillingAt(t)
}

// SegmentCovering returns the priced segment serving t, whatever has been
// persisted; nil when none does.
//
// source: app/services/billing/rate_cards/schedule.rb:55-63
func (s *Schedule) SegmentCovering(t time.Time) *Segment {
	c := s.walker.Resume(&t)
	if c == nil {
		return nil
	}
	for _, seg := range s.segmentsOf([]*Cycle{c}) {
		if seg.Period().Contains(t) {
			return &seg
		}
	}
	return nil
}

// ConsumedRatio returns the share of seg's days consumed at at, measured
// against the segment itself; a segment of no day is fully consumed.
//
// source: app/services/billing/rate_cards/schedule.rb:66-77
func (s *Schedule) ConsumedRatio(seg Segment, at time.Time) Ratio {
	days := DaysBetween(seg.StartedAt, seg.EndedAt, s.loc)
	if days == 0 {
		return One
	}
	until := at
	if until.Before(seg.StartedAt) {
		until = seg.StartedAt
	}
	if until.After(seg.EndedAt) {
		until = seg.EndedAt
	}
	return Ratio{Days: DaysBetween(seg.StartedAt, until, s.loc), Total: days}
}

// segmentsOf returns the priced slices of the cycles.
//
// source: app/services/billing/rate_cards/schedule.rb:83-93
func (s *Schedule) segmentsOf(cycles []*Cycle) []Segment {
	var out []Segment
	for _, c := range cycles {
		for _, sl := range Within(cyclePeriod(c), s.rates) {
			if sl.Rate == nil {
				continue
			}
			ratio := One
			if s.terms.Prorated {
				// The slice lies inside the cycle, which lies inside its
				// calendar interval, so the ratio is always defined.
				ratio, _ = c.Calendar.ProrationRatio(sl.From, sl.To)
			}
			out = append(out, Segment{
				CycleIndex:     c.Index,
				CycleStartedAt: c.StartedAt,
				StartedAt:      sl.From,
				EndedAt:        sl.To,
				BillingAt:      s.terms.BillingAt(sl.Period()),
				Rate:           sl.Rate,
				Override:       c.Phase.Override,
				Proration:      ratio,
				PhaseCode:      c.Phase.Code,
			})
		}
	}
	return out
}

func cyclePeriod(c *Cycle) period.Period { return period.Period{From: c.StartedAt, To: c.EndedAt} }
