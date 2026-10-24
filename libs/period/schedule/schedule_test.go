package schedule

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

type sched struct {
	anchor   Date
	phases   []Phase
	rates    []*Rate
	prorated *bool
	tz       string
	startsAt time.Time
	timing   Timing
	endsAt   *time.Time
	resumeAt *time.Time
}

func cardRate(at time.Time, args ...any) *Rate {
	count, unit := 1, Month
	if len(args) == 2 {
		count, unit = args[0].(int), args[1].(Unit)
	}
	return rate(at, count, unit)
}

// phaseOf mirrors the spec's phase helper: every phase carries an
// interval override.
func phaseOf(count *int, every int, unit Unit, code, label string) Phase {
	o := &Override{ID: label, IntervalUnit: unit}
	if every > 0 {
		o.IntervalCount = intp(every)
	}
	if code == "" {
		code = "standard"
	}
	return Phase{Code: code, CycleCount: count, Override: o}
}

func (s sched) params() Params {
	if s.anchor == (Date{}) {
		s.anchor = date(2022, 1, 1)
	}
	if s.phases == nil {
		s.phases = []Phase{phaseOf(nil, 1, Month, "", "")}
	}
	if s.rates == nil {
		s.rates = []*Rate{cardRate(utc(2000, 1, 1))}
	}
	prorated := true
	if s.prorated != nil {
		prorated = *s.prorated
	}
	if s.tz == "" {
		s.tz = "UTC"
	}
	if s.startsAt.IsZero() {
		s.startsAt = utc(2022, 1, 15)
	}
	if s.timing == "" {
		s.timing = Arrears
	}
	loc, _ := time.LoadLocation(s.tz)
	return Params{
		Rates: s.rates, Terms: Terms{Timing: s.timing, Prorated: prorated}, Phases: s.phases,
		StartsAt: s.startsAt, EndsAt: s.endsAt, Anchor: s.anchor, Location: loc, ResumeAt: s.resumeAt,
	}
}

func (s sched) build(t *testing.T) *Schedule {
	t.Helper()
	out, err := New(s.params())
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func windowsOf(segs []Segment) []string {
	out := []string{}
	for _, s := range segs {
		out = append(out, fmt.Sprintf("%s -> %s", s.StartedAt.UTC().Format(time.DateOnly), s.EndedAt.UTC().AddDate(0, 0, -1).Format(time.DateOnly)))
	}
	return out
}

func eqStrings(t *testing.T, label string, got, want []string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("%s: got = %v, want = %v", label, got, want)
	}
}

func cycleIndexes(segs []Segment) []int {
	out := []int{}
	for _, s := range segs {
		out = append(out, s.CycleIndex)
	}
	return out
}

func billingAts(segs []Segment) []string {
	out := []string{}
	for _, s := range segs {
		out = append(out, s.BillingAt.UTC().Format(time.RFC3339))
	}
	return out
}

func rfc(ts ...time.Time) []string {
	out := []string{}
	for _, t := range ts {
		out = append(out, t.UTC().Format(time.RFC3339))
	}
	return out
}

func boolp(b bool) *bool { return &b }

// source: spec/services/billing/rate_cards/schedule_spec.rb:48-82
func TestScheduleValidation(t *testing.T) {
	for _, c := range []struct {
		source string
		s      sched
	}{
		{"spec/services/billing/rate_cards/schedule_spec.rb:52", sched{rates: []*Rate{}}},
		{"spec/services/billing/rate_cards/schedule_spec.rb:57", sched{endsAt: tp(utc(2022, 1, 14))}},
		{"spec/services/billing/rate_cards/schedule_spec.rb:62", sched{phases: []Phase{}}},
		{"spec/services/billing/rate_cards/schedule_spec.rb:69", sched{phases: []Phase{phaseOf(intp(6), 1, Month, "", "")}}},
		{"spec/services/billing/rate_cards/schedule_spec.rb:76", sched{phases: []Phase{phaseOf(nil, 1, Week, "", ""), phaseOf(nil, 1, Month, "", "")}}},
	} {
		if c.s.rates != nil && len(c.s.rates) == 0 {
			p := c.s.params()
			p.Rates = []*Rate{}
			if _, err := New(p); !errors.Is(err, ErrInvalid) {
				t.Errorf("%s: err = %v, want = ErrInvalid", c.source, err)
			}
			continue
		}
		p := c.s.params()
		if c.s.phases != nil && len(c.s.phases) == 0 {
			p.Phases = []Phase{}
		}
		if _, err := New(p); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want = ErrInvalid", c.source, err)
		}
	}
}

// source: spec/services/billing/rate_cards/schedule_spec.rb:84-168
func TestScheduleCycles(t *testing.T) {
	s := sched{}.build(t)
	eqStrings(t, "clamp start", windowsOf(s.SegmentsDueBy(utc(2022, 3, 1))), []string{"2022-01-15 -> 2022-01-31", "2022-02-01 -> 2022-02-28"})
	eqStrings(t, "not closed", windowsOf(s.SegmentsDueBy(utc(2022, 2, 10))), []string{"2022-01-15 -> 2022-01-31"})
	eqInts(t, "numbering", cycleIndexes(s.SegmentsDueBy(utc(2022, 5, 1))), []int{0, 1, 2, 3})
	eqInts(t, "nothing due", cycleIndexes(s.SegmentsDueBy(utc(2022, 1, 15))), []int{})
	adv := sched{timing: Advance}.build(t)
	eqInts(t, "advance first", cycleIndexes(adv.SegmentsDueBy(utc(2022, 1, 15))), []int{0})
	eqInts(t, "advance exact", cycleIndexes(adv.SegmentsDueBy(utc(2022, 2, 1))), []int{0, 1})
	before := sched{startsAt: utc(2021, 12, 20)}.build(t).SegmentsDueBy(utc(2022, 1, 1))
	if len(before) != 1 || before[0].CycleIndex != 0 || !before[0].StartedAt.Equal(utc(2021, 12, 20)) || !before[0].EndedAt.Equal(utc(2022, 1, 1)) {
		t.Errorf("before anchor: got = %v, want = one cycle [2021-12-20, 2022-01-01)", windowsOf(before))
	}
	eqStrings(t, "ends", windowsOf(sched{endsAt: tp(utc(2022, 3, 20))}.build(t).SegmentsDueBy(utc(2023, 1, 1))),
		[]string{"2022-01-15 -> 2022-01-31", "2022-02-01 -> 2022-02-28", "2022-03-01 -> 2022-03-19"})
	eqStrings(t, "ends on boundary", windowsOf(sched{endsAt: tp(utc(2022, 3, 1))}.build(t).SegmentsDueBy(utc(2023, 1, 1))),
		[]string{"2022-01-15 -> 2022-01-31", "2022-02-01 -> 2022-02-28"})

	// source: spec/services/billing/rate_cards/schedule_spec.rb:146-167
	eqStrings(t, "arrears due", billingAts(s.SegmentsDueBy(utc(2022, 4, 1))), rfc(utc(2022, 2, 1), utc(2022, 3, 1), utc(2022, 4, 1)))
	eqStrings(t, "advance due", billingAts(adv.SegmentsDueBy(utc(2022, 4, 1))), rfc(utc(2022, 1, 15), utc(2022, 2, 1), utc(2022, 3, 1), utc(2022, 4, 1)))
	ended := sched{endsAt: tp(utc(2022, 3, 20))}.build(t).SegmentsDueBy(utc(2023, 1, 1))
	eqTime(t, "termination due", ended[len(ended)-1].BillingAt, utc(2022, 3, 20))

	// source: spec/services/billing/rate_cards/schedule_spec.rb:184
	resumed := sched{startsAt: utc(2022, 1, 1), rates: []*Rate{cardRate(utc(2022, 1, 1)), cardRate(utc(2022, 2, 15))}, resumeAt: tp(utc(2022, 2, 1))}.build(t)
	var starts []time.Time
	for _, seg := range resumed.SegmentsDueBy(utc(2022, 3, 1)) {
		starts = append(starts, seg.StartedAt)
	}
	eqStrings(t, "resume replays the cycle", rfc(starts...), rfc(utc(2022, 2, 1), utc(2022, 2, 15)))
}

// source: spec/services/billing/rate_cards/schedule_spec.rb:193-385
func TestScheduleFlatSegments(t *testing.T) {
	cut := utc(2022, 2, 15)
	base := sched{rates: []*Rate{cardRate(utc(2021, 1, 1)), cardRate(cut)}}
	s := base.build(t)
	due := s.SegmentsDueBy(utc(2022, 2, 20))
	// source: spec/services/billing/rate_cards/schedule_spec.rb:203
	var got []string
	for _, seg := range due {
		got = append(got, seg.StartedAt.Format(time.DateOnly)+" "+seg.BillingAt.Format(time.DateOnly))
	}
	eqStrings(t, "release on cut", got, []string{"2022-01-15 2022-02-01", "2022-02-01 2022-02-15"})
	// source: spec/services/billing/rate_cards/schedule_spec.rb:213
	for _, seg := range due {
		if seg.EndedAt.Equal(utc(2022, 3, 1)) {
			t.Errorf("withhold: got = a segment ending 2022-03-01, want = none")
		}
	}
	// source: spec/services/billing/rate_cards/schedule_spec.rb:218
	if due[0].CycleIndex != 0 || due[1].CycleIndex != 1 || !due[1].CycleStartedAt.Equal(utc(2022, 2, 1)) {
		t.Errorf("cycle facts: got = %d %d %s", due[0].CycleIndex, due[1].CycleIndex, due[1].CycleStartedAt)
	}
	// source: spec/services/billing/rate_cards/schedule_spec.rb:226
	var ratios []Ratio
	for _, seg := range s.SegmentsDueBy(utc(2022, 4, 1)) {
		if seg.CycleIndex == 1 {
			ratios = append(ratios, seg.Proration)
		}
	}
	if len(ratios) != 2 || ratios[0].Float() != 0.5 || ratios[1].Float() != 0.5 {
		t.Errorf("cut ratios: got = %v, want = 14/28 twice", ratios)
	}
	// source: spec/services/billing/rate_cards/schedule_spec.rb:238
	adv := base
	adv.timing = Advance
	got = nil
	for _, seg := range adv.build(t).SegmentsDueBy(cut) {
		got = append(got, seg.StartedAt.Format(time.DateOnly)+" "+seg.BillingAt.Format(time.DateOnly))
	}
	eqStrings(t, "advance release", got, []string{"2022-01-15 2022-01-15", "2022-02-01 2022-02-01", "2022-02-15 2022-02-15"})
	// source: spec/services/billing/rate_cards/schedule_spec.rb:253-265
	late := sched{rates: []*Rate{cardRate(cut)}}.build(t)
	if n := len(late.SegmentsDueBy(cut)); n != 0 {
		t.Errorf("unpriced: got = %d segments, want = 0", n)
	}
	one := late.SegmentsDueBy(utc(2022, 3, 1))
	if len(one) != 1 || one[0].CycleIndex != 1 || !one[0].StartedAt.Equal(cut) || !one[0].EndedAt.Equal(utc(2022, 3, 1)) ||
		!one[0].BillingAt.Equal(utc(2022, 3, 1)) || one[0].Proration.Float() != 0.5 {
		t.Errorf("priced part: got = %+v", one)
	}
	// source: spec/services/billing/rate_cards/schedule_spec.rb:271
	flat := base
	flat.prorated = boolp(false)
	for _, seg := range flat.build(t).SegmentsDueBy(utc(2022, 3, 1)) {
		if seg.CycleIndex == 1 && seg.Proration != One {
			t.Errorf("not prorated: got = %v, want = 1", seg.Proration)
		}
	}

	// source: spec/services/billing/rate_cards/schedule_spec.rb:279-307
	segmentAt := func(s *Schedule, start time.Time) Segment {
		for _, seg := range s.SegmentsDueBy(utc(2022, 3, 1)) {
			if seg.StartedAt.Equal(start) {
				return seg
			}
		}
		t.Fatalf("no segment at %s", start)
		return Segment{}
	}
	seg := segmentAt(s, cut)
	eqRatio(t, "consumed", s.ConsumedRatio(seg, utc(2022, 2, 22)), 7, 14)
	eqRatio(t, "at start", s.ConsumedRatio(seg, seg.StartedAt), 0, 14)
	eqRatio(t, "before start", s.ConsumedRatio(seg, seg.StartedAt.AddDate(0, 0, -1)), 0, 14)
	eqRatio(t, "at end", s.ConsumedRatio(seg, seg.EndedAt), 1, 1)
	eqRatio(t, "after end", s.ConsumedRatio(seg, seg.EndedAt.AddDate(0, 0, 1)), 1, 1)
	ny := sched{tz: "America/New_York", rates: []*Rate{cardRate(utc(2021, 1, 1)), cardRate(utc(2022, 2, 15, 5))}}
	nys := ny.build(t)
	var nyseg Segment
	loc := zone(t, "America/New_York")
	for _, x := range nys.SegmentsDueBy(local(loc, 2022, 3, 1)) {
		if x.StartedAt.Equal(utc(2022, 2, 15, 5)) {
			nyseg = x
		}
	}
	eqRatio(t, "west of utc", nys.ConsumedRatio(nyseg, utc(2022, 2, 22, 12)), 8, 14)

	// source: spec/services/billing/rate_cards/schedule_spec.rb:313-356
	x2 := sched{anchor: date(2026, 9, 10), rates: []*Rate{cardRate(utc(2026, 1, 1))}, timing: Advance, startsAt: utc(2026, 9, 10)}.build(t)
	x2seg := x2.SegmentsDueBy(utc(2026, 9, 15))[0]
	unused := 1 - x2.ConsumedRatio(x2seg, utc(2026, 9, 25, 16, 30)).Float()
	if unused != 1-16.0/30 {
		t.Errorf("X2: got = %v, want = 14/30", unused)
	}
	for _, at := range []time.Time{utc(2026, 9, 25, 0, 0, 1), utc(2026, 9, 25, 12, 34, 56), utc(2026, 9, 25, 23, 59, 59)} {
		eqRatio(t, "X1 "+at.String(), x2.ConsumedRatio(x2seg, at), 16, 30)
	}
	eqRatio(t, "X1 midnight", x2.ConsumedRatio(x2seg, utc(2026, 9, 25)), 15, 30)
	eqRatio(t, "X2 boundary", x2.ConsumedRatio(x2seg, utc(2026, 10, 9, 23)), 1, 1)

	// source: spec/services/billing/rate_cards/schedule_spec.rb:362-384
	inday := sched{rates: []*Rate{cardRate(utc(2021, 1, 1)), cardRate(utc(2022, 2, 15, 9)), cardRate(utc(2022, 2, 15, 18))}}.build(t)
	for _, x := range inday.SegmentsDueBy(utc(2022, 4, 1)) {
		if x.StartedAt.Equal(utc(2022, 2, 15, 9)) {
			eqRatio(t, "in-day proration", x.Proration, 0, 28)
			eqRatio(t, "in-day consumed", inday.ConsumedRatio(x, utc(2022, 2, 15, 12)), 1, 1)
		}
	}
}

// source: spec/services/billing/rate_cards/schedule_spec.rb:390-462
func TestScheduleCutAndShare(t *testing.T) {
	march := func(s sched) [][2]time.Time {
		var out [][2]time.Time
		for _, seg := range s.build(t).SegmentsDueBy(utc(2022, 4, 1)) {
			if seg.CycleStartedAt.Equal(utc(2022, 3, 1)) {
				out = append(out, [2]time.Time{seg.StartedAt, seg.EndedAt})
			}
		}
		return out
	}
	ratiosOf := func(s sched) []float64 {
		var out []float64
		for _, seg := range s.build(t).SegmentsDueBy(utc(2022, 4, 1)) {
			if seg.CycleStartedAt.Equal(utc(2022, 3, 1)) {
				out = append(out, seg.Proration.Float())
			}
		}
		return out
	}
	eqF := func(label string, got []float64, want ...float64) {
		t.Helper()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: got = %v, want = %v", label, got, want)
		}
	}
	if b := march(sched{}); len(b) != 1 || !b[0][0].Equal(utc(2022, 3, 1)) || !b[0][1].Equal(utc(2022, 4, 1)) {
		t.Errorf("whole: got = %v", b)
	}
	eqF("whole ratio", ratiosOf(sched{}), 1)
	if b := march(sched{rates: []*Rate{cardRate(utc(2021, 1, 1)), cardRate(utc(2022, 3, 15))}}); len(b) != 2 || !b[0][1].Equal(utc(2022, 3, 15)) {
		t.Errorf("split: got = %v", b)
	}
	eqF("mid-day", ratiosOf(sched{rates: []*Rate{cardRate(utc(2021, 1, 1)), cardRate(utc(2022, 3, 15, 14))}}), 15.0/31, 16.0/31)
	eqF("clamped", ratiosOf(sched{endsAt: tp(utc(2022, 3, 20))}), 19.0/31)
	eqF("not prorated", ratiosOf(sched{prorated: boolp(false), endsAt: tp(utc(2022, 3, 20))}), 1)
	eqF("not prorated cut", ratiosOf(sched{prorated: boolp(false), endsAt: tp(utc(2022, 3, 20)), rates: []*Rate{cardRate(utc(2021, 1, 1)), cardRate(utc(2022, 3, 10))}}), 1, 1)
	eqF("cut and clamped", ratiosOf(sched{endsAt: tp(utc(2022, 3, 20)), rates: []*Rate{cardRate(utc(2021, 1, 1)), cardRate(utc(2022, 3, 10))}}), 9.0/31, 10.0/31)
}

func eqNext(t *testing.T, label string, got time.Time, ok bool, want *time.Time) {
	t.Helper()
	switch {
	case want == nil && ok:
		t.Errorf("%s: got = %s, want = none", label, got.UTC())
	case want != nil && (!ok || !got.Equal(*want)):
		t.Errorf("%s: got = %s (%v), want = %s", label, got.UTC(), ok, want.UTC())
	}
}

// source: spec/services/billing/rate_cards/schedule_spec.rb:470-566
func TestScheduleNextBillingAt(t *testing.T) {
	cutRates := []*Rate{cardRate(utc(2021, 1, 1)), cardRate(utc(2022, 2, 15))}
	late := []*Rate{cardRate(utc(2022, 5, 1))}
	for _, c := range []struct {
		source string
		s      sched
		after  time.Time
		want   *time.Time
	}{
		{"spec/services/billing/rate_cards/schedule_spec.rb:471", sched{}, utc(2022, 1, 15), tp(utc(2022, 2, 1))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:475", sched{}, utc(2022, 3, 10), tp(utc(2022, 4, 1))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:479", sched{}, utc(2022, 3, 1), tp(utc(2022, 4, 1))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:484", sched{}, utc(2022, 1, 1), tp(utc(2022, 2, 1))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:491", sched{timing: Advance}, utc(2022, 1, 1), tp(utc(2022, 1, 15))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:504", sched{rates: cutRates}, utc(2022, 2, 1), tp(utc(2022, 2, 15))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:511", sched{rates: cutRates, timing: Advance}, utc(2022, 2, 10), tp(utc(2022, 2, 15))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:515", sched{rates: cutRates, timing: Advance}, utc(2022, 2, 15), tp(utc(2022, 3, 1))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:525", sched{rates: late}, utc(2022, 1, 15), tp(utc(2022, 6, 1))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:532", sched{rates: late, timing: Advance}, utc(2022, 1, 15), tp(utc(2022, 5, 1))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:540", sched{rates: late, endsAt: tp(utc(2022, 4, 1))}, utc(2022, 1, 15), nil},
		{"spec/services/billing/rate_cards/schedule_spec.rb:555", sched{rates: []*Rate{cardRate(utc(2021, 1, 1), 2, Year)}, phases: []Phase{phaseOf(nil, 2, Year, "", "")}, startsAt: utc(2022, 1, 1)}, utc(2022, 1, 1), tp(utc(2024, 1, 1))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:563", sched{endsAt: tp(utc(2022, 3, 1))}, utc(2023, 1, 1), nil},
	} {
		got, ok := c.s.build(t).NextBillingAt(c.after)
		eqNext(t, c.source, got, ok, c.want)
	}
}

// source: spec/services/billing/rate_cards/schedule_spec.rb:569-652
func TestScheduleBillingAtCovering(t *testing.T) {
	cutRates := []*Rate{cardRate(utc(2021, 1, 1)), cardRate(utc(2022, 2, 15))}
	late := []*Rate{cardRate(utc(2022, 5, 1))}
	for _, c := range []struct {
		source string
		s      sched
		at     time.Time
		want   *time.Time
	}{
		{"spec/services/billing/rate_cards/schedule_spec.rb:574", sched{}, utc(2022, 3, 1), tp(utc(2022, 4, 1))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:578", sched{}, utc(2022, 1, 15), tp(utc(2022, 2, 1))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:585", sched{rates: cutRates}, utc(2022, 2, 10), tp(utc(2022, 2, 15))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:589", sched{rates: cutRates}, utc(2022, 2, 15), tp(utc(2022, 3, 1))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:596", sched{rates: cutRates, timing: Advance}, utc(2022, 2, 10), tp(utc(2022, 2, 1))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:600", sched{rates: cutRates, timing: Advance}, utc(2022, 2, 15), tp(utc(2022, 2, 15))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:604", sched{rates: cutRates, timing: Advance}, utc(2022, 2, 20), tp(utc(2022, 2, 15))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:613", sched{rates: late}, utc(2022, 1, 15), tp(utc(2022, 6, 1))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:620", sched{rates: late, timing: Advance}, utc(2022, 1, 15), tp(utc(2022, 5, 1))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:628", sched{rates: late, endsAt: tp(utc(2022, 4, 1))}, utc(2022, 1, 15), nil},
		{"spec/services/billing/rate_cards/schedule_spec.rb:637", sched{timing: Advance}, utc(2022, 1, 15), tp(utc(2022, 1, 15))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:643", sched{timing: Advance}, utc(2022, 3, 10), tp(utc(2022, 3, 1))},
		{"spec/services/billing/rate_cards/schedule_spec.rb:661", sched{endsAt: tp(utc(2022, 3, 1))}, utc(2023, 1, 1), nil},
		{"spec/services/billing/rate_cards/schedule_spec.rb:665", sched{endsAt: tp(utc(2022, 3, 1))}, utc(2022, 3, 1), nil},
	} {
		got, ok := c.s.build(t).BillingAtCovering(c.at)
		eqNext(t, c.source, got, ok, c.want)
	}
	// source: spec/services/billing/rate_cards/schedule_spec.rb:649
	s := sched{timing: Advance}.build(t)
	got, ok := s.NextBillingAt(utc(2022, 3, 10))
	eqNext(t, "differs", got, ok, tp(utc(2022, 4, 1)))
}

// source: spec/services/billing/rate_cards/schedule_spec.rb:674-684
func TestScheduleCalendarReuse(t *testing.T) {
	s := sched{}.build(t)
	segs := s.SegmentsDueBy(utc(2032, 1, 1))
	if len(segs) != 120 {
		t.Errorf("segments: got = %d, want = 120", len(segs))
	}
	if n := len(s.walker.calendars); n != 1 {
		t.Errorf("calendars: got = %d, want = 1", n)
	}
}

// source: spec/services/billing/rate_cards/schedule_spec.rb:689-799
func TestScheduleIntroPhase(t *testing.T) {
	label := func(l string, at time.Time) *Rate {
		return &Rate{ID: l, EffectiveFrom: at, IntervalCount: 1, IntervalUnit: Month}
	}
	mk := func(timing Timing) sched {
		return sched{
			anchor: date(2026, 8, 10), startsAt: utc(2026, 8, 12), timing: timing,
			rates: []*Rate{label("A", utc(2026, 1, 1)), label("B", utc(2026, 8, 20)), label("C", utc(2026, 9, 15))},
			phases: []Phase{
				phaseOf(intp(3), 1, Week, "weekly_intro", "-50%"),
				phaseOf(nil, 1, Month, "standard", ""),
			},
		}
	}
	at := utc(2026, 12, 1)
	group := func(segs []Segment) [][]Segment {
		var out [][]Segment
		for _, s := range segs {
			if len(out) > 0 && out[len(out)-1][0].CycleStartedAt.Equal(s.CycleStartedAt) {
				out[len(out)-1] = append(out[len(out)-1], s)
				continue
			}
			out = append(out, []Segment{s})
		}
		return out[:4]
	}
	arrears := mk(Arrears).build(t)
	segs := arrears.SegmentsDueBy(at)
	groups := group(segs)
	var windows, billed, due []string
	var totals []float64
	for _, g := range groups {
		windows = append(windows, g[0].CycleStartedAt.Format(time.DateOnly)+" -> "+g[len(g)-1].EndedAt.Format(time.DateOnly)+" "+g[0].PhaseCode)
		var b string
		var total float64
		for _, s := range g {
			b += fmt.Sprintf("%s:%d/%d ", s.Rate.ID, s.Proration.Days, s.Proration.Total)
			total += s.Proration.Float()
		}
		billed = append(billed, b)
		totals = append(totals, total)
		due = append(due, g[len(g)-1].BillingAt.Format(time.DateOnly))
	}
	// source: spec/services/billing/rate_cards/schedule_spec.rb:726
	eqStrings(t, "weekly then monthly", windows, []string{
		"2026-08-12 -> 2026-08-17 weekly_intro", "2026-08-17 -> 2026-08-24 weekly_intro",
		"2026-08-24 -> 2026-08-31 weekly_intro", "2026-08-31 -> 2026-09-30 standard",
	})
	// source: spec/services/billing/rate_cards/schedule_spec.rb:747
	eqStrings(t, "shares", billed, []string{"A:5/7 ", "A:3/7 B:4/7 ", "B:7/7 ", "B:15/30 C:15/30 "})
	// source: spec/services/billing/rate_cards/schedule_spec.rb:761
	if fmt.Sprint(totals) != fmt.Sprint([]float64{5.0 / 7, 1, 1, 1}) {
		t.Errorf("totals: got = %v", totals)
	}
	// source: spec/services/billing/rate_cards/schedule_spec.rb:770
	var attributed []string
	for _, s := range segs[:6] {
		o := ""
		if s.Override != nil {
			o = s.Override.ID
		}
		attributed = append(attributed, s.Rate.ID+" "+s.PhaseCode+" "+o)
	}
	eqStrings(t, "attribution", attributed, []string{
		"A weekly_intro -50%", "A weekly_intro -50%", "B weekly_intro -50%", "B weekly_intro -50%", "B standard ", "C standard ",
	})
	// source: spec/services/billing/rate_cards/schedule_spec.rb:784
	eqStrings(t, "arrears due", due, []string{"2026-08-17", "2026-08-24", "2026-08-31", "2026-09-30"})
	// source: spec/services/billing/rate_cards/schedule_spec.rb:793-807
	advGroups := group(mk(Advance).build(t).SegmentsDueBy(at))
	var advDue []string
	for i, g := range advGroups {
		advDue = append(advDue, g[0].BillingAt.Format(time.DateOnly))
		if !g[0].CycleStartedAt.Equal(groups[i][0].CycleStartedAt) || !g[len(g)-1].EndedAt.Equal(groups[i][len(groups[i])-1].EndedAt) {
			t.Errorf("advance cycle %d: got = a different cycle", i)
		}
	}
	eqStrings(t, "advance due", advDue, []string{"2026-08-12", "2026-08-17", "2026-08-24", "2026-08-31"})
}

// source: spec/services/billing/rate_cards/schedule_spec.rb:816-832
func TestScheduleEarliestCadence(t *testing.T) {
	s := sched{
		anchor: date(2026, 1, 1), startsAt: utc(2026, 1, 1),
		phases: []Phase{phaseOf(nil, 0, "", "", "")},
		rates:  []*Rate{cardRate(utc(2026, 6, 1), 1, Week), cardRate(utc(2026, 9, 1), 1, Month)},
	}.build(t)
	if n := len(s.SegmentsDueBy(utc(2026, 3, 1))); n != 0 {
		t.Errorf("before first rate: got = %d, want = 0", n)
	}
	first := s.SegmentsDueBy(utc(2026, 6, 11))[0]
	if first.CycleIndex != 21 || !first.StartedAt.Equal(utc(2026, 6, 1)) || !first.EndedAt.Equal(utc(2026, 6, 4)) {
		t.Errorf("first priced: got = %d [%s, %s)", first.CycleIndex, first.StartedAt, first.EndedAt)
	}
}

// source: spec/services/billing/rate_cards/schedule_spec.rb:837-891
func TestSchedulePhaseTransition(t *testing.T) {
	s := sched{
		anchor: date(2026, 8, 10), startsAt: utc(2026, 8, 10), rates: []*Rate{cardRate(utc(2000, 1, 1))},
		phases: []Phase{phaseOf(intp(6), 1, Week, "weekly_intro", ""), phaseOf(nil, 1, Month, "standard", "")},
	}.build(t)
	end := utc(2027, 12, 30)
	segs := s.SegmentsDueBy(end)
	eqStrings(t, "published cycles", windowsOf(segs), []string{
		"2026-08-10 -> 2026-08-16", "2026-08-17 -> 2026-08-23", "2026-08-24 -> 2026-08-30",
		"2026-08-31 -> 2026-09-06", "2026-09-07 -> 2026-09-13", "2026-09-14 -> 2026-09-20",
		"2026-09-21 -> 2026-10-20", "2026-10-21 -> 2026-11-20", "2026-11-21 -> 2026-12-20",
		"2026-12-21 -> 2027-01-20", "2027-01-21 -> 2027-02-20", "2027-02-21 -> 2027-03-20",
		"2027-03-21 -> 2027-04-20", "2027-04-21 -> 2027-05-20", "2027-05-21 -> 2027-06-20",
		"2027-06-21 -> 2027-07-20", "2027-07-21 -> 2027-08-20", "2027-08-21 -> 2027-09-20",
		"2027-09-21 -> 2027-10-20", "2027-10-21 -> 2027-11-20", "2027-11-21 -> 2027-12-20",
	})
	want := []int{}
	for i := range 21 {
		want = append(want, i)
	}
	eqInts(t, "numbering", cycleIndexes(segs), want)
	for i, seg := range segs {
		code := "standard"
		if i < 6 {
			code = "weekly_intro"
		}
		if seg.PhaseCode != code {
			t.Errorf("phase %d: got = %s, want = %s", i, seg.PhaseCode, code)
		}
		if i >= 6 && seg.BillingAt.Day() != 21 {
			t.Errorf("billing day %d: got = %d, want = 21", i, seg.BillingAt.Day())
		}
	}
	got, ok := s.NextBillingAt(utc(2026, 9, 25))
	eqNext(t, "next", got, ok, tp(utc(2026, 10, 21)))
}
