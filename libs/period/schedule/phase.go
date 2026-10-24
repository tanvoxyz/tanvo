package schedule

import (
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/period"
)

// Rate is one rate of a rate card's timeline, as far as the schedule reads
// it.
type Rate struct {
	// ID identifies the rate to the caller.
	ID string
	// EffectiveFrom is when the rate takes effect.
	EffectiveFrom time.Time
	// IntervalCount is the count of the rate's billing interval.
	IntervalCount int
	// IntervalUnit is the unit of the rate's billing interval.
	IntervalUnit Unit
}

// Override is a phase's rate override, as far as the schedule reads it.
type Override struct {
	// ID identifies the override to the caller.
	ID string
	// IntervalCount overrides the rate's interval count; nil keeps it.
	IntervalCount *int
	// IntervalUnit overrides the rate's interval unit; empty keeps it.
	IntervalUnit Unit
}

// Phase is a stretch of consecutive cycles priced under one override. It
// carries no dates: a phase starts where the previous one ended.
type Phase struct {
	// Code is carried onto every segment the phase prices; empty for the
	// default phase, which nothing persisted.
	Code string
	// CycleCount is the number of cycles; nil runs to the end of the card.
	CycleCount *int
	// Override prices the phase's cycles; nil bills the card's rates.
	Override *Override
}

// DefaultPhase returns the card's own rate and cadence, unbounded.
//
// source: app/services/billing/phase.rb:18
func DefaultPhase() Phase { return Phase{} }

// NewPhase returns the phase, failing with [ErrInvalid] when its cycle
// count is set and not positive.
//
// source: app/services/billing/phase.rb:20-28
func NewPhase(code string, count *int, o *Override) (Phase, error) {
	if count != nil && *count <= 0 {
		return Phase{}, invalid("a phase lasts a positive whole number of cycles, or nil to run to the end, got %d", *count)
	}
	return Phase{Code: code, CycleCount: count, Override: o}, nil
}

// Unbounded reports whether the phase runs to the end of the card.
func (p Phase) Unbounded() bool { return p.CycleCount == nil }

// Timing says which end of a window falls due.
type Timing string

// Timing values.
const (
	// Advance bills a window at its start.
	Advance Timing = "advance"
	// Arrears bills a window at its end.
	Arrears Timing = "arrears"
)

// Terms are a rate card's billing timing and proration.
type Terms struct {
	// Timing is advance or arrears.
	Timing Timing
	// Prorated prorates partial windows.
	Prorated bool
}

// NewTerms returns the terms, failing with [ErrInvalid] for an unknown
// timing.
//
// source: app/services/billing/terms.rb:7-14
func NewTerms(timing Timing, prorated bool) (Terms, error) {
	if timing != Advance && timing != Arrears {
		return Terms{}, invalid("unknown billing timing %q, expected one of advance, arrears", timing)
	}
	return Terms{Timing: timing, Prorated: prorated}, nil
}

// BillingAt returns the start of w in advance, its end in arrears.
//
// source: app/services/billing/terms.rb:20-26
func (t Terms) BillingAt(w period.Period) time.Time {
	if t.Timing == Advance {
		return w.From
	}
	return w.To
}

// Slice is a part of a window priced by one rate; Rate is nil while no
// rate is in force.
type Slice struct {
	// From is the inclusive start.
	From time.Time
	// To is the exclusive end.
	To time.Time
	// Rate is the rate in force at From, or nil.
	Rate *Rate
}

// Period returns the slice's window.
func (s Slice) Period() period.Period { return period.Period{From: s.From, To: s.To} }

// Within splits the window w at each rate change inside it, keeping the
// unpriced parts with a nil rate. The rates need not be ordered.
//
// source: app/services/billing/segments.rb:8-13
func Within(w period.Period, rates []*Rate) []Slice {
	cuts := []time.Time{w.From}
	var inside []time.Time
	for _, r := range rates {
		if r.EffectiveFrom.After(w.From) && r.EffectiveFrom.Before(w.To) &&
			!slices.ContainsFunc(inside, r.EffectiveFrom.Equal) {
			inside = append(inside, r.EffectiveFrom)
		}
	}
	slices.SortFunc(inside, time.Time.Compare)
	cuts = append(cuts, inside...)
	cuts = append(cuts, w.To)
	out := make([]Slice, 0, len(cuts)-1)
	for i := range len(cuts) - 1 {
		out = append(out, Slice{From: cuts[i], To: cuts[i+1], Rate: rateAt(rates, cuts[i])})
	}
	return out
}

// rateAt returns the latest rate in effect at t, or nil.
func rateAt(rates []*Rate, t time.Time) *Rate {
	var out *Rate
	for _, r := range rates {
		if !r.EffectiveFrom.After(t) && (out == nil || r.EffectiveFrom.After(out.EffectiveFrom)) {
			out = r
		}
	}
	return out
}

// Segment is one slice of one cycle: everything a billing segment row
// needs.
type Segment struct {
	// CycleIndex is the cycle's index from the card's start.
	CycleIndex int
	// CycleStartedAt is the start of the cycle.
	CycleStartedAt time.Time
	// StartedAt is the inclusive start of the slice.
	StartedAt time.Time
	// EndedAt is the exclusive end of the slice.
	EndedAt time.Time
	// BillingAt is when the slice falls due.
	BillingAt time.Time
	// Rate is the rate in force.
	Rate *Rate
	// Override is the phase's override, which prices the slice when set.
	Override *Override
	// Proration is the share of the cycle's interval the slice covers;
	// [One] when the card does not prorate.
	Proration Ratio
	// PhaseCode is the code of the phase pricing the slice.
	PhaseCode string
}

// Period returns the segment's window.
func (s Segment) Period() period.Period { return period.Period{From: s.StartedAt, To: s.EndedAt} }
