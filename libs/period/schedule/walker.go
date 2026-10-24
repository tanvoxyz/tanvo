package schedule

import (
	"slices"
	"time"
)

// Cycle is one billing period of one rate card. EndedAt is exclusive.
// Calendar is the ruler the cycle was measured on; it changes when a
// cadence change re-anchors the walk.
type Cycle struct {
	// Index is the cycle's number from the card's start.
	Index int
	// StartedAt is the inclusive start.
	StartedAt time.Time
	// EndedAt is the exclusive end.
	EndedAt time.Time
	// Phase is the phase pricing the cycle.
	Phase Phase
	// Calendar is the ruler of the cycle.
	Calendar *Calendar
}

type calendarKey struct {
	anchor   Date
	interval Interval
}

// Walker walks the cycles of one rate card from its start, through its
// phases and rate changes, and resumes from any instant without walking
// the history.
type Walker struct {
	rates     []*Rate
	phases    []Phase
	startsAt  time.Time
	endsAt    *time.Time
	anchor    Date
	loc       *time.Location
	current   *Cycle
	calendars map[calendarKey]*Calendar
}

// NewWalker returns the walker of a card starting at startsAt (moved to
// the start of its local day) and ending at endsAt (nil when open-ended),
// anchored on anchor in loc. It fails with [ErrInvalid] without a rate or
// a phase, when the last phase is bounded or an earlier one unbounded,
// when endsAt precedes startsAt, or when a rate or override gives no
// valid interval.
//
// source: app/services/billing/rate_cards/cycle_walker.rb:8-17, :68-84
func NewWalker(rates []*Rate, phases []Phase, startsAt time.Time, anchor Date, loc *time.Location, endsAt *time.Time) (*Walker, error) {
	if loc == nil {
		loc = time.UTC
	}
	sorted := slices.Clone(rates)
	slices.SortStableFunc(sorted, func(a, b *Rate) int { return a.EffectiveFrom.Compare(b.EffectiveFrom) })
	w := &Walker{
		rates:     sorted,
		phases:    phases,
		startsAt:  StartOfDay(startsAt, loc),
		endsAt:    endsAt,
		anchor:    anchor,
		loc:       loc,
		calendars: map[calendarKey]*Calendar{},
	}
	switch {
	case len(sorted) == 0:
		return nil, invalid("at least one rate is required to determine the billing interval")
	case endsAt != nil && endsAt.Before(startsAt):
		return nil, invalid("ends_at %s precedes starts_at %s", endsAt.UTC(), startsAt.UTC())
	case len(phases) == 0:
		return nil, invalid("at least one phase is required")
	case !phases[len(phases)-1].Unbounded():
		return nil, invalid("the last phase must run to the end of the card")
	}
	for _, p := range phases[:len(phases)-1] {
		if p.Unbounded() {
			return nil, invalid("only the last phase may run to the end of the card")
		}
	}
	for _, r := range sorted {
		if _, err := IntervalOf(r, nil); err != nil {
			return nil, err
		}
	}
	for _, p := range phases {
		if p.Override != nil {
			if _, err := IntervalOf(sorted[0], p.Override); err != nil {
				return nil, err
			}
		}
	}
	return w, nil
}

// Current returns the cycle the walker stands on, or nil past the end.
func (w *Walker) Current() *Cycle { return w.current }

// Start moves to the first cycle.
//
// source: app/services/billing/rate_cards/cycle_walker.rb:19-21
func (w *Walker) Start() *Cycle {
	w.current = w.build(0, w.startsAt, nil)
	return w.current
}

// Advance moves to the next cycle; nil past the end.
//
// source: app/services/billing/rate_cards/cycle_walker.rb:23-31
func (w *Walker) Advance() *Cycle {
	if w.current == nil {
		return nil
	}
	w.current = w.build(w.current.Index+1, w.current.EndedAt, w.current.Calendar)
	return w.current
}

// Resume moves to the cycle containing t, jumping over whole runs of
// cycles that share a phase, a rate and a cadence; nil t starts from the
// beginning, and nil is returned at or past the end.
//
// source: app/services/billing/rate_cards/cycle_walker.rb:33-50
func (w *Walker) Resume(t *time.Time) *Cycle {
	w.Start()
	if t == nil {
		return w.current
	}
	if w.endsAt != nil && !t.Before(*w.endsAt) {
		w.current = nil
	}
	for w.current != nil && !w.current.EndedAt.After(*t) {
		steps := w.stepsToAdvance(*t)
		cal := w.current.Calendar
		w.current = w.build(w.current.Index+steps, cal.BoundaryAfter(w.current.StartedAt, steps), cal)
	}
	return w.current
}

// WalkTo returns the cycles from the one containing from (the start when
// nil) up to and including the one containing t.
//
// source: app/services/billing/rate_cards/cycle_walker.rb:53-65
func (w *Walker) WalkTo(t time.Time, from *time.Time) []*Cycle {
	var out []*Cycle
	w.Resume(from)
	for w.current != nil && !w.current.StartedAt.After(t) {
		out = append(out, w.current)
		if w.current.EndedAt.After(t) {
			break
		}
		w.Advance()
	}
	return out
}

// stepsToAdvance returns how far the walk may jump from the current cycle
// toward t without crossing a phase change or the cycle a later rate's
// cadence takes hold at.
//
// source: app/services/billing/rate_cards/cycle_walker.rb:95-114
func (w *Walker) stepsToAdvance(t time.Time) int {
	cal := w.current.Calendar
	started := w.current.StartedAt
	steps := cal.IntervalsBetween(started, t)
	if i, ok := w.nextPhaseStartIndex(); ok {
		steps = min(steps, i-w.current.Index)
	}
	for _, r := range w.rates {
		if r.EffectiveFrom.After(started) {
			boundary := cal.BoundaryAtOrAfter(r.EffectiveFrom)
			steps = min(steps, cal.IntervalsBetween(started, boundary))
			break
		}
	}
	return steps
}

// nextPhaseStartIndex returns the index the next phase starts at; false
// within the unbounded phase.
//
// source: app/services/billing/rate_cards/cycle_walker.rb:116-129
func (w *Walker) nextPhaseStartIndex() (int, bool) {
	boundary := 0
	for _, p := range w.phases {
		if p.Unbounded() {
			return 0, false
		}
		boundary += *p.CycleCount
		if boundary > w.current.Index {
			return boundary, true
		}
	}
	return 0, false
}

// build returns the cycle index starting at started, measured on prev's
// calendar while the cadence holds and re-anchored on started's local day
// when it changes; nil at or past the end.
//
// source: app/services/billing/rate_cards/cycle_walker.rb:131-155
func (w *Walker) build(index int, started time.Time, prev *Calendar) *Cycle {
	if w.endsAt != nil && !started.Before(*w.endsAt) {
		return nil
	}
	phase := w.phaseByIndex(index)
	rate := w.rateForCycle(started)
	interval, _ := IntervalOf(rate, phase.Override)
	var cal *Calendar
	if prev != nil && interval == prev.Interval() {
		cal = prev
	} else {
		anchor := w.anchor
		if prev != nil {
			anchor = DateOf(started, w.loc)
		}
		key := calendarKey{anchor, interval}
		if cal = w.calendars[key]; cal == nil {
			cal = NewCalendar(anchor, interval, w.loc)
			w.calendars[key] = cal
		}
	}
	end := cal.IntervalContaining(started).To
	if w.endsAt != nil && w.endsAt.Before(end) {
		end = *w.endsAt
	}
	return &Cycle{Index: index, StartedAt: started, EndedAt: end, Phase: phase, Calendar: cal}
}

// phaseByIndex returns the phase pricing cycle index.
//
// source: app/services/billing/rate_cards/cycle_walker.rb:157-165
func (w *Walker) phaseByIndex(index int) Phase {
	for _, p := range w.phases {
		if p.Unbounded() || index < *p.CycleCount {
			return p
		}
		index -= *p.CycleCount
	}
	return w.phases[len(w.phases)-1]
}

// rateForCycle returns the latest rate in effect at t; before the first
// rate, the first one, whose interval shapes the cycle without pricing
// it.
//
// source: app/services/billing/rate_cards/cycle_walker.rb:167-171
func (w *Walker) rateForCycle(t time.Time) *Rate {
	for i := len(w.rates) - 1; i >= 0; i-- {
		if !w.rates[i].EffectiveFrom.After(t) {
			return w.rates[i]
		}
	}
	return w.rates[0]
}
