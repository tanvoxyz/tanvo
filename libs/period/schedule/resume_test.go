package schedule

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

type rateArg struct {
	at    time.Time
	count int
	unit  Unit
}

type phaseArg struct {
	cycles *int
	count  int
	unit   Unit
	code   string
}

type shape struct {
	name     string
	anchor   Date
	startsAt time.Time
	endsAt   *time.Time
	tz       string
	timing   Timing
	prorated *bool
	rates    []rateArg
	phases   []phaseArg
}

func (s shape) loc(t *testing.T) *time.Location { return zone(t, s.tz) }

func (s shape) rateList() []*Rate {
	var out []*Rate
	for _, r := range s.rates {
		out = append(out, rate(r.at, r.count, r.unit))
	}
	return out
}

func (s shape) phaseList() []Phase {
	var out []Phase
	for _, p := range s.phases {
		ph := Phase{Code: p.code, CycleCount: p.cycles}
		if p.count > 0 {
			ph.Override = &Override{IntervalCount: intp(p.count), IntervalUnit: p.unit}
		}
		out = append(out, ph)
	}
	return out
}

func (s shape) walker(t *testing.T) *Walker {
	t.Helper()
	w, err := NewWalker(s.rateList(), s.phaseList(), s.startsAt, s.anchor, s.loc(t), s.endsAt)
	if err != nil {
		t.Fatalf("%s: %v", s.name, err)
	}
	return w
}

func (s shape) schedule(t *testing.T, resumeAt *time.Time) *Schedule {
	t.Helper()
	prorated := true
	if s.prorated != nil {
		prorated = *s.prorated
	}
	out, err := New(Params{
		Rates: s.rateList(), Terms: Terms{Timing: s.timing, Prorated: prorated}, Phases: s.phaseList(),
		StartsAt: s.startsAt, EndsAt: s.endsAt, Anchor: s.anchor, Location: s.loc(t), ResumeAt: resumeAt,
	})
	if err != nil {
		t.Fatalf("%s: %v", s.name, err)
	}
	return out
}

var resumeAskedAt = utc(2027, 6, 1)

func walkShape(t *testing.T, s shape, to time.Time, from *time.Time) []*Cycle {
	return s.walker(t).WalkTo(to, from)
}

func stateOf(c *Cycle) string {
	return fmt.Sprintf("%s %s %v %d", c.StartedAt.UTC(), c.Calendar.AnchorDate(), c.Calendar.Interval(), c.Index)
}

func fingerprint(cs []*Cycle) string {
	var out string
	for _, c := range cs {
		n := -1
		if c.Phase.CycleCount != nil {
			n = *c.Phase.CycleCount
		}
		out += fmt.Sprintf("[%d %s %s %s %d %v %s]", c.Index, c.StartedAt.UTC(), c.EndedAt.UTC(), c.Phase.Code, n, c.Calendar.Interval(), c.Calendar.AnchorDate())
	}
	return out
}

func segmentValues(segs []Segment, minIndex int) string {
	var out string
	for _, s := range segs {
		if s.CycleIndex < minIndex {
			continue
		}
		var unit Unit
		if s.Override != nil {
			unit = s.Override.IntervalUnit
		}
		out += fmt.Sprintf("[%d %s %s %s %s %d/%d %s %s %s]", s.CycleIndex, s.CycleStartedAt.UTC(), s.StartedAt.UTC(), s.EndedAt.UTC(),
			s.BillingAt.UTC(), s.Proration.Days, s.Proration.Total, s.PhaseCode, s.Rate.EffectiveFrom.UTC(), unit)
	}
	return out
}

func resumeShapes(t *testing.T) []shape {
	anchors := []struct {
		name string
		d    Date
	}{
		{"month-end", date(2026, 1, 31)},
		{"leap day", date(2024, 2, 29)},
		{"mid-month", date(2026, 3, 15)},
		{"a day whose midnight does not exist", date(2026, 3, 8)},
	}
	rateSets := []struct {
		name  string
		rates []rateArg
	}{
		{"one rate", []rateArg{{utc(2025, 1, 1), 1, Month}}},
		{"two rates, same cadence", []rateArg{{utc(2025, 1, 1), 1, Month}, {utc(2026, 4, 10), 1, Month}}},
		{"monthly then weekly", []rateArg{{utc(2025, 1, 1), 1, Month}, {utc(2026, 4, 10), 1, Week}}},
		{"monthly, weekly, monthly", []rateArg{{utc(2025, 1, 1), 1, Month}, {utc(2026, 3, 10), 1, Week}, {utc(2026, 6, 20), 1, Month}}},
		{"three cadences and a rate change", []rateArg{{utc(2025, 1, 1), 2, Week}, {utc(2026, 2, 5), 1, Month}, {utc(2026, 5, 17), 3, Day}, {utc(2026, 6, 2), 3, Day}}},
		{"first rate lands after the card opens", []rateArg{{utc(2026, 5, 1), 1, Month}}},
	}
	phaseSets := []struct {
		name   string
		phases []phaseArg
	}{
		{"one open phase", []phaseArg{{nil, 1, Month, ""}}},
		{"weekly intro then monthly", []phaseArg{{intp(3), 1, Week, "intro"}, {nil, 1, Month, ""}}},
		{"two bounded then open", []phaseArg{{intp(2), 1, Week, "a"}, {intp(2), 1, Month, "b"}, {nil, 1, Month, ""}}},
		{"phase override differs from every rate", []phaseArg{{intp(4), 2, Week, "odd"}, {nil, 1, Year, ""}}},
	}
	zones := []struct {
		tz     string
		timing Timing
	}{{"UTC", Arrears}, {"America/New_York", Advance}, {"Asia/Kolkata", Arrears}, {"America/Havana", Arrears}}
	var out []shape
	for _, a := range anchors {
		for _, r := range rateSets {
			for _, p := range phaseSets {
				for _, z := range zones {
					loc := zone(t, z.tz)
					out = append(out, shape{
						name:   fmt.Sprintf("%s / %s / %s / %s %s", a.name, r.name, p.name, z.tz, z.timing),
						anchor: a.d, tz: z.tz, timing: z.timing, rates: r.rates, phases: p.phases,
						startsAt: Interval{1, Day}.Advance(a.d.Start(loc), 2),
					})
				}
			}
		}
	}
	return out
}

func TestScheduleResume(t *testing.T) {
	shapes := resumeShapes(t)
	// source: spec/services/billing/rate_cards/schedule_resume_spec.rb:109
	for _, s := range shapes {
		w := s.walker(t)
		for _, c := range walkShape(t, s, resumeAskedAt, nil) {
			at := c.StartedAt
			if got, want := stateOf(w.Resume(&at)), stateOf(c); got != want {
				t.Errorf("%s: jumping to %s gave %s, walk had %s", s.name, c.StartedAt.UTC(), got, want)
			}
		}
	}
	// source: spec/services/billing/rate_cards/schedule_resume_spec.rb:126
	asc := shape{
		name: "ordered", anchor: date(2026, 1, 31), startsAt: utc(2026, 1, 31), tz: "UTC", timing: Arrears,
		phases: []phaseArg{{nil, 0, "", ""}},
		rates:  []rateArg{{utc(2025, 1, 1), 1, Month}, {utc(2026, 4, 12), 1, Week}, {utc(2026, 8, 3), 3, Day}},
	}
	back := asc
	back.rates = []rateArg{asc.rates[2], asc.rates[1], asc.rates[0]}
	ascending := walkShape(t, asc, resumeAskedAt, nil)
	if len(ascending) <= 20 || fingerprint(walkShape(t, back, resumeAskedAt, nil)) != fingerprint(ascending) {
		t.Errorf("order: got = a different walk for reversed rates")
	}
	for i, c := range ascending {
		at := c.StartedAt
		if fingerprint(walkShape(t, back, resumeAskedAt, &at)) != fingerprint(ascending[i:]) {
			t.Errorf("order: resuming at %d gave a different tail", i)
		}
	}
	// source: spec/services/billing/rate_cards/schedule_resume_spec.rb:146
	first := shapes[0]
	full := walkShape(t, first, resumeAskedAt, nil)
	inside := full[2].StartedAt.Add(full[2].EndedAt.Sub(full[2].StartedAt) / 2)
	if fingerprint(walkShape(t, first, resumeAskedAt, &inside)) != fingerprint(full[2:]) {
		t.Errorf("mid-cycle: got = a different tail")
	}
	// source: spec/services/billing/rate_cards/schedule_resume_spec.rb:155-188
	fullS := first.schedule(t, nil)
	resumed := first.schedule(t, &full[3].StartedAt)
	for _, c := range full[1:5] {
		want, ok := fullS.BillingAtCovering(c.StartedAt)
		got, ok2 := resumed.BillingAtCovering(c.StartedAt)
		if !ok || !ok2 || !got.Equal(want) {
			t.Errorf("covering %s: got = %s, want = %s", c.StartedAt, got, want)
		}
	}
	at := full[1].StartedAt.AddDate(0, 0, 1)
	got, _ := resumed.BillingAtCovering(at)
	eqTime(t, "historical covering", got, full[1].EndedAt)
	got, _ = resumed.NextBillingAt(at)
	eqTime(t, "historical next", got, full[1].EndedAt)
	resumed.BillingAtCovering(full[1].StartedAt)
	eqInts(t, "listing stays resumed", cycleIndexes(resumed.SegmentsDueBy(full[4].EndedAt)), []int{3, 4})
	got, _ = resumed.BillingAtCovering(first.startsAt.AddDate(-1, 0, 0))
	eqTime(t, "before the card", got, full[0].EndedAt)
	// source: spec/services/billing/rate_cards/schedule_resume_spec.rb:192
	p := Params{
		Rates: first.rateList(), Terms: Terms{Timing: first.timing, Prorated: true}, Phases: first.phaseList(),
		StartsAt: first.startsAt, Anchor: first.anchor, Location: first.loc(t), ResumeAt: tp(first.startsAt.AddDate(-1, 0, 0)),
	}
	if _, err := New(p); !errors.Is(err, ErrInvalid) {
		t.Errorf("resume before start: err = %v, want = ErrInvalid", err)
	}

	// source: spec/services/billing/rate_cards/schedule_resume_spec.rb:202-280
	named := []shape{
		{name: "X5", anchor: date(2026, 1, 31), tz: "UTC", timing: Arrears, rates: []rateArg{{utc(2025, 1, 1), 1, Month}}, phases: []phaseArg{{nil, 1, Month, ""}}},
		{name: "X6b", anchor: date(2024, 2, 29), tz: "UTC", timing: Arrears, rates: []rateArg{{utc(2023, 1, 1), 1, Year}}, phases: []phaseArg{{nil, 1, Year, ""}}},
		{name: "AN1", anchor: date(2026, 2, 20), startsAt: utc(2026, 2, 3), tz: "UTC", timing: Arrears, rates: []rateArg{{utc(2025, 1, 1), 1, Month}}, phases: []phaseArg{{nil, 1, Month, ""}}},
		{name: "longer than a year", anchor: date(2018, 1, 1), tz: "UTC", timing: Arrears, rates: []rateArg{{utc(2017, 1, 1), 2, Year}}, phases: []phaseArg{{nil, 2, Year, ""}}},
		{name: "decision #56", anchor: date(2026, 1, 1), startsAt: utc(2026, 1, 15), tz: "UTC", timing: Arrears, rates: []rateArg{{utc(2026, 4, 1), 1, Month}}, phases: []phaseArg{{nil, 1, Month, ""}}},
		{name: "termination mid-cycle", anchor: date(2026, 1, 31), tz: "UTC", timing: Arrears, endsAt: tp(utc(2026, 6, 17, 14, 30)), rates: []rateArg{{utc(2025, 1, 1), 1, Month}}, phases: []phaseArg{{nil, 1, Month, ""}}},
		{name: "termination on boundary", anchor: date(2026, 1, 31), tz: "UTC", timing: Arrears, endsAt: tp(utc(2026, 5, 31)), rates: []rateArg{{utc(2025, 1, 1), 1, Month}}, phases: []phaseArg{{nil, 1, Month, ""}}},
		{name: "not prorated", anchor: date(2026, 1, 31), tz: "UTC", timing: Arrears, prorated: boolp(false), endsAt: tp(utc(2026, 6, 17)), rates: []rateArg{{utc(2025, 1, 1), 1, Month}}, phases: []phaseArg{{nil, 1, Month, ""}}},
		{name: "spring-forward anchor", anchor: date(2026, 3, 8), tz: "America/New_York", timing: Advance, rates: []rateArg{{utc(2025, 1, 1), 1, Day}}, phases: []phaseArg{{nil, 1, Day, ""}}},
		{name: "fall-back anchor", anchor: date(2026, 11, 1), tz: "America/New_York", timing: Arrears, rates: []rateArg{{utc(2025, 1, 1), 1, Day}}, phases: []phaseArg{{nil, 1, Day, ""}}},
		{name: "Lord Howe", anchor: date(2026, 4, 5), tz: "Australia/Lord_Howe", timing: Arrears, rates: []rateArg{{utc(2025, 1, 1), 1, Week}}, phases: []phaseArg{{nil, 1, Week, ""}}},
		{name: "rate change mid-cycle", anchor: date(2026, 1, 1), tz: "UTC", timing: Arrears, rates: []rateArg{{utc(2025, 1, 1), 1, Month}, {time.Date(2026, 3, 14, 7, 5, 0, 0, time.UTC), 1, Month}}, phases: []phaseArg{{nil, 1, Month, ""}}},
		{name: "cadence change month-end", anchor: date(2026, 1, 31), tz: "UTC", timing: Arrears, rates: []rateArg{{utc(2025, 1, 1), 1, Month}, {utc(2026, 4, 12), 1, Week}}, phases: []phaseArg{{nil, 0, "", ""}}},
	}
	for _, s := range named {
		if s.startsAt.IsZero() {
			s.startsAt = s.anchor.Start(s.loc(t))
		}
		full := walkShape(t, s, resumeAskedAt, nil)
		if len(full) == 0 {
			t.Errorf("%s: got = no cycles", s.name)
		}
		segs := s.schedule(t, nil).SegmentsDueBy(resumeAskedAt)
		for n := range full {
			at := full[n].StartedAt
			if fingerprint(walkShape(t, s, resumeAskedAt, &at)) != fingerprint(full[n:]) {
				t.Errorf("%s: cycles at %d", s.name, n)
			}
			if segmentValues(s.schedule(t, &at).SegmentsDueBy(resumeAskedAt), 0) != segmentValues(segs, full[n].Index) {
				t.Errorf("%s: segments at %d", s.name, n)
			}
		}
	}

	// source: spec/services/billing/rate_cards/schedule_resume_spec.rb:282, :311
	for _, s := range shapes {
		full := walkShape(t, s, resumeAskedAt, nil)
		segs := s.schedule(t, nil).SegmentsDueBy(resumeAskedAt)
		for n, c := range full {
			at := c.StartedAt
			if fingerprint(walkShape(t, s, resumeAskedAt, &at)) != fingerprint(full[n:]) {
				t.Errorf("%s: resuming at %d of %d gave a different tail", s.name, n, len(full))
			}
			if segmentValues(s.schedule(t, &at).SegmentsDueBy(resumeAskedAt), 0) != segmentValues(segs, c.Index) {
				t.Errorf("%s: resuming at %d gave different segments", s.name, c.Index)
			}
		}
	}

	// source: spec/services/billing/rate_cards/schedule_resume_spec.rb:329
	plain := shape{
		name: "plain", anchor: date(2026, 1, 31), startsAt: utc(2026, 1, 31), tz: "UTC", timing: Arrears,
		phases: []phaseArg{{nil, 1, Month, ""}}, rates: []rateArg{{utc(2025, 1, 1), 1, Month}},
	}
	all := walkShape(t, plain, utc(2028, 1, 1), nil)
	tail := walkShape(t, plain, utc(2028, 1, 1), &all[len(all)-3].StartedAt)
	if len(all) <= 20 || len(tail) != 3 || tail[0].Index != all[len(all)-3].Index {
		t.Errorf("walks only the tail: got = %d of %d", len(tail), len(all))
	}
	// source: spec/services/billing/rate_cards/schedule_resume_spec.rb:341
	century := shape{
		name: "century", anchor: date(1926, 1, 1), startsAt: utc(1926, 1, 1), tz: "UTC", timing: Arrears,
		phases: []phaseArg{{nil, 0, "", ""}}, rates: []rateArg{{utc(1926, 1, 1), 1, Day}},
	}
	cs := walkShape(t, century, utc(2026, 1, 3), tp(utc(2026, 1, 1)))
	eqInts(t, "century", indexes(cs), []int{36525, 36526, 36527})
	eqTime(t, "century start", cs[0].StartedAt, utc(2026, 1, 1))
	// source: spec/services/billing/rate_cards/schedule_resume_spec.rb:361
	feb := walkShape(t, plain, resumeAskedAt, tp(utc(2026, 2, 28)))
	eqTime(t, "feb start", feb[0].StartedAt, utc(2026, 2, 28))
	eqStrings(t, "month-end kept", rfc(feb[0].EndedAt, feb[1].EndedAt, feb[2].EndedAt), rfc(utc(2026, 3, 31), utc(2026, 4, 30), utc(2026, 5, 31)))
	// source: spec/services/billing/rate_cards/schedule_resume_spec.rb:374
	if _, err := New(Params{
		Rates: []*Rate{rate(utc(2025, 1, 1), 1, Month)}, Terms: Terms{Timing: Arrears, Prorated: true},
		Phases: []Phase{{Override: &Override{IntervalCount: intp(1), IntervalUnit: Month}}}, StartsAt: utc(2026, 1, 31, 8),
		EndsAt: tp(utc(2026, 1, 31, 6)), Anchor: date(2026, 1, 31), Location: time.UTC,
	}); !errors.Is(err, ErrInvalid) {
		t.Errorf("end before raw start: err = %v, want = ErrInvalid", err)
	}
}
