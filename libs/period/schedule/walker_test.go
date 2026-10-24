package schedule

import (
	"testing"
	"time"
)

type walkerOpts struct {
	anchor   Date
	startsAt time.Time
	endsAt   *time.Time
	tz       string
	phases   []Phase
	rates    []*Rate
}

func newWalker(t *testing.T, o walkerOpts) *Walker {
	t.Helper()
	if o.anchor == (Date{}) {
		o.anchor = date(2026, 1, 31)
	}
	if o.startsAt.IsZero() {
		o.startsAt = utc(2026, 1, 31)
	}
	if o.tz == "" {
		o.tz = "UTC"
	}
	if o.phases == nil {
		o.phases = []Phase{DefaultPhase()}
	}
	if o.rates == nil {
		o.rates = []*Rate{rate(utc(2026, 1, 1), 1, Month)}
	}
	w, err := NewWalker(o.rates, o.phases, o.startsAt, o.anchor, zone(t, o.tz), o.endsAt)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

type span struct {
	index    int
	from, to time.Time
}

func spanOf(c *Cycle) span { return span{c.Index, c.StartedAt.UTC(), c.EndedAt.UTC()} }

func eqSpan(t *testing.T, label string, c *Cycle, want span) {
	t.Helper()
	if c == nil {
		t.Errorf("%s: got = nil, want = %v", label, want)
		return
	}
	if got := spanOf(c); got.index != want.index || !got.from.Equal(want.from) || !got.to.Equal(want.to) {
		t.Errorf("%s: got = %v, want = %v", label, got, want)
	}
}

func weeklyIntro(n int) []Phase {
	return []Phase{{Code: "intro", CycleCount: intp(n), Override: &Override{IntervalUnit: Week}}, DefaultPhase()}
}

func TestWalkerStart(t *testing.T) {
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:36
	w := newWalker(t, walkerOpts{
		anchor: date(2026, 1, 1), startsAt: utc(2026, 1, 1),
		rates: []*Rate{rate(utc(2026, 6, 1), 1, Week), rate(utc(2026, 9, 1), 1, Month)},
	})
	cycles := w.WalkTo(utc(2026, 3, 1), nil)
	eqSpan(t, "earliest 0", cycles[0], span{0, utc(2026, 1, 1), utc(2026, 1, 8)})
	eqSpan(t, "earliest 1", cycles[1], span{1, utc(2026, 1, 8), utc(2026, 1, 15)})
	eqSpan(t, "earliest 2", cycles[2], span{2, utc(2026, 1, 15), utc(2026, 1, 22)})
	if len(cycles) != 9 || cycles[8].Index != 8 {
		t.Errorf("earliest: got = %d cycles, want = 9", len(cycles))
	}

	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:49
	w = newWalker(t, walkerOpts{})
	c := w.Start()
	eqSpan(t, "start", c, span{0, utc(2026, 1, 31), utc(2026, 2, 28)})
	if w.Current() != c {
		t.Errorf("current: got = %v, want = %v", w.Current(), c)
	}
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:60
	eqSpan(t, "inside", newWalker(t, walkerOpts{startsAt: utc(2026, 2, 10, 14, 30)}).Start(), span{0, utc(2026, 2, 10), utc(2026, 2, 28)})
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:70
	eqTime(t, "clip", newWalker(t, walkerOpts{endsAt: tp(utc(2026, 2, 15, 12))}).Start().EndedAt, utc(2026, 2, 15, 12))
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:79
	eqSpan(t, "west", newWalker(t, walkerOpts{startsAt: utc(2026, 2, 1, 2), tz: "America/New_York"}).Start(), span{0, utc(2026, 1, 31, 5), utc(2026, 2, 28, 5)})
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:92
	eqTime(t, "phase unit", newWalker(t, walkerOpts{phases: weeklyIntro(2)}).Start().EndedAt, utc(2026, 2, 7))
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:104
	eqTime(t, "rate in force", newWalker(t, walkerOpts{rates: []*Rate{
		rate(utc(2026, 2, 1), 1, Year), rate(utc(2025, 12, 1), 1, Week), rate(utc(2026, 1, 1), 1, Month),
	}}).Start().EndedAt, utc(2026, 2, 28))
}

func TestWalkerAdvance(t *testing.T) {
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:111
	if c := newWalker(t, walkerOpts{}).Advance(); c != nil {
		t.Errorf("before start: got = %v, want = nil", c)
	}
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:115
	w := newWalker(t, walkerOpts{})
	w.Start()
	eqSpan(t, "advance", w.Advance(), span{1, utc(2026, 2, 28), utc(2026, 3, 31)})
	eqTime(t, "advance 2", w.Advance().EndedAt, utc(2026, 4, 30))
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:128
	w = newWalker(t, walkerOpts{rates: []*Rate{rate(utc(2026, 1, 1), 1, Month), rate(utc(2026, 2, 15), 1, Week)}})
	eqTime(t, "rate change first", w.Start().EndedAt, utc(2026, 2, 28))
	eqSpan(t, "rate change", w.Advance(), span{1, utc(2026, 2, 28), utc(2026, 3, 7)})
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:140
	w = newWalker(t, walkerOpts{rates: []*Rate{rate(utc(2026, 1, 1), 1, Month), rate(utc(2026, 2, 28), 1, Month)}})
	w.Start()
	eqTime(t, "same interval", w.Advance().EndedAt, utc(2026, 3, 31))
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:153
	phases := weeklyIntro(2)
	w = newWalker(t, walkerOpts{phases: phases})
	w.Start()
	second := w.Advance()
	monthly := w.Advance()
	if second.Index != 1 || !second.EndedAt.Equal(utc(2026, 2, 14)) || second.Phase.Code != "intro" {
		t.Errorf("intro: got = %v %s, want = 1 2026-02-14 intro", spanOf(second), second.Phase.Code)
	}
	eqSpan(t, "monthly", monthly, span{2, utc(2026, 2, 14), utc(2026, 3, 14)})
	if monthly.Phase.Code != "" || monthly.Phase.Override != nil {
		t.Errorf("monthly phase: got = %+v, want = default", monthly.Phase)
	}
	eqTime(t, "monthly next", w.Advance().EndedAt, utc(2026, 4, 14))
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:171
	w = newWalker(t, walkerOpts{phases: []Phase{{Code: "intro", CycleCount: intp(1)}, DefaultPhase()}})
	w.Start()
	c := w.Advance()
	if c.Phase.Code != "" || !c.EndedAt.Equal(utc(2026, 3, 31)) {
		t.Errorf("phase same interval: got = %v %q, want = default ending 2026-03-31", spanOf(c), c.Phase.Code)
	}
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:182
	end := utc(2026, 3, 15, 12)
	w = newWalker(t, walkerOpts{endsAt: &end})
	w.Start()
	eqTime(t, "clip last", w.Advance().EndedAt, end)
	if w.Advance() != nil || w.Current() != nil || w.Advance() != nil {
		t.Errorf("exhausted: got = a cycle, want = nil")
	}
	if w.Start().Index != 0 {
		t.Errorf("restart: got = %d, want = 0", w.Start().Index)
	}
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:196
	w = newWalker(t, walkerOpts{endsAt: tp(utc(2026, 2, 28))})
	w.Start()
	if w.Advance() != nil || w.Current() != nil {
		t.Errorf("end on boundary: got = a cycle, want = nil")
	}
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:208
	w = newWalker(t, walkerOpts{tz: "America/New_York", startsAt: utc(2026, 1, 31, 5)})
	w.Start()
	c = w.Advance()
	eqSpan(t, "dst", c, span{1, utc(2026, 2, 28, 5), utc(2026, 3, 31, 4)})
}

func TestWalkerResume(t *testing.T) {
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:218
	w := newWalker(t, walkerOpts{})
	first := w.Resume(tp(utc(2026, 3, 15)))
	again := w.Resume(tp(utc(2026, 3, 15)))
	if again.Calendar != first.Calendar || spanOf(again) != spanOf(first) {
		t.Errorf("reuse: got = %v, want = %v on the same calendar", spanOf(again), spanOf(first))
	}
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:230
	eqSpan(t, "long jump", newWalker(t, walkerOpts{}).Resume(tp(utc(2126, 3, 15))), span{1201, utc(2126, 2, 28), utc(2126, 3, 31)})
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:240
	c := newWalker(t, walkerOpts{}).Resume(tp(utc(2026, 3, 31)))
	if c.Index != 2 || !c.StartedAt.Equal(utc(2026, 3, 31)) {
		t.Errorf("exact: got = %v, want = 2 at 2026-03-31", spanOf(c))
	}
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:246
	c = newWalker(t, walkerOpts{}).Resume(tp(utc(2025, 1, 1)))
	if c.Index != 0 || !c.StartedAt.Equal(utc(2026, 1, 31)) {
		t.Errorf("before: got = %v, want = 0 at start", spanOf(c))
	}
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:255
	eqSpan(t, "partial first", newWalker(t, walkerOpts{startsAt: utc(2026, 2, 10)}).Resume(tp(utc(2026, 4, 10))), span{2, utc(2026, 3, 31), utc(2026, 4, 30)})
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:266
	eqSpan(t, "rate change skipped", newWalker(t, walkerOpts{rates: []*Rate{rate(utc(2026, 1, 1), 1, Month), rate(utc(2026, 7, 15), 1, Week)}}).Resume(tp(utc(2026, 7, 31))),
		span{6, utc(2026, 7, 31), utc(2026, 8, 7)})
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:284
	phases := weeklyIntro(3)
	w = newWalker(t, walkerOpts{phases: phases, rates: []*Rate{rate(utc(2026, 1, 1), 1, Month), rate(utc(2026, 2, 5), 2, Month)}})
	intro := w.Resume(tp(utc(2026, 2, 10)))
	eqSpan(t, "intro", intro, span{1, utc(2026, 2, 7), utc(2026, 2, 21)})
	regular := w.Resume(tp(utc(2026, 3, 10)))
	eqSpan(t, "regular", regular, span{3, utc(2026, 3, 7), utc(2026, 5, 7)})
	if intro.Phase.Code != "intro" || regular.Phase.Code != "" {
		t.Errorf("phases: got = %q %q, want = intro and default", intro.Phase.Code, regular.Phase.Code)
	}
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:310
	mixed := walkerOpts{
		rates: []*Rate{
			rate(utc(2026, 1, 1), 1, Month), rate(utc(2026, 2, 15), 2, Month), rate(utc(2026, 2, 20), 3, Month),
			rate(utc(2026, 5, 28), 1, Week), rate(utc(2026, 7, 1), 1, Month),
		},
		phases: []Phase{
			{Code: "intro", CycleCount: intp(3), Override: &Override{IntervalCount: intp(1), IntervalUnit: Month}},
			{Code: "discount", CycleCount: intp(2)},
			DefaultPhase(),
		},
	}
	w = newWalker(t, mixed)
	cycles := w.WalkTo(utc(2027, 1, 1), nil)
	for _, want := range cycles {
		for _, at := range []time.Time{want.StartedAt, want.StartedAt.Add(time.Hour)} {
			got := w.Resume(&at)
			if spanOf(got) != spanOf(want) || got.Phase.Code != want.Phase.Code ||
				got.Calendar.AnchorDate() != want.Calendar.AnchorDate() || got.Calendar.Interval() != want.Calendar.Interval() {
				t.Errorf("resume at %s: got = %v %s, want = %v %s", at, spanOf(got), got.Phase.Code, spanOf(want), want.Phase.Code)
			}
		}
	}
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:325
	repeated := w.WalkTo(utc(2027, 1, 1), nil)
	anchors := map[Date]bool{}
	for i := range cycles {
		if repeated[i].Calendar != cycles[i].Calendar {
			t.Errorf("calendar %d: got = a new calendar, want = reused", i)
		}
		if cycles[i].Calendar.Interval() == cycles[0].Calendar.Interval() {
			anchors[cycles[i].Calendar.AnchorDate()] = true
		}
	}
	if len(anchors) <= 1 {
		t.Errorf("anchors: got = %d, want = > 1", len(anchors))
	}
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:345
	w = newWalker(t, walkerOpts{rates: []*Rate{rate(utc(2026, 3, 15), 1, Month), rate(utc(2026, 5, 15), 1, Week)}})
	expected := w.WalkTo(utc(2026, 6, 10), nil)
	if got, want := w.Resume(tp(utc(2026, 6, 10))), expected[len(expected)-1]; spanOf(got) != spanOf(want) {
		t.Errorf("unpriced: got = %v, want = %v", spanOf(got), spanOf(want))
	}
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:359
	eqSpan(t, "daily dst", newWalker(t, walkerOpts{tz: "America/New_York", startsAt: utc(2026, 1, 31, 5), rates: []*Rate{rate(utc(2026, 1, 1), 1, Day)}}).Resume(tp(utc(2026, 3, 10, 12))),
		span{38, utc(2026, 3, 10, 4), utc(2026, 3, 11, 4)})
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:370
	w = newWalker(t, walkerOpts{endsAt: tp(utc(2026, 3, 15))})
	if w.Resume(tp(utc(2026, 4, 1))) != nil || w.Current() != nil {
		t.Errorf("after termination: got = a cycle, want = nil")
	}
}

func indexes(cs []*Cycle) []int {
	out := []int{}
	for _, c := range cs {
		out = append(out, c.Index)
	}
	return out
}

func eqInts(t *testing.T, label string, got, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: got = %v, want = %v", label, got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s: got = %v, want = %v", label, got, want)
			return
		}
	}
}

func TestWalkerWalkTo(t *testing.T) {
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:378
	w := newWalker(t, walkerOpts{})
	cs := w.WalkTo(utc(2026, 4, 10), nil)
	eqSpan(t, "walk 0", cs[0], span{0, utc(2026, 1, 31), utc(2026, 2, 28)})
	eqSpan(t, "walk 1", cs[1], span{1, utc(2026, 2, 28), utc(2026, 3, 31)})
	eqSpan(t, "walk 2", cs[2], span{2, utc(2026, 3, 31), utc(2026, 4, 30)})
	if w.Current() != cs[2] {
		t.Errorf("current: got = %v, want = the last cycle", w.Current())
	}
	eqTime(t, "advance after walk", w.Advance().StartedAt, utc(2026, 4, 30))
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:390
	eqInts(t, "exact", indexes(newWalker(t, walkerOpts{}).WalkTo(utc(2026, 2, 28), nil)), []int{0, 1})
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:397
	eqInts(t, "before", indexes(newWalker(t, walkerOpts{}).WalkTo(utc(2026, 1, 30), nil)), []int{})
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:401
	eqInts(t, "at start", indexes(newWalker(t, walkerOpts{}).WalkTo(utc(2026, 1, 31), nil)), []int{0})
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:405
	w = newWalker(t, walkerOpts{})
	w.WalkTo(utc(2026, 4, 10), nil)
	eqInts(t, "fresh 1", indexes(w.WalkTo(utc(2026, 3, 10), nil)), []int{0, 1})
	eqInts(t, "fresh 2", indexes(w.WalkTo(utc(2026, 4, 10), nil)), []int{0, 1, 2})
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:415
	end := utc(2026, 3, 15, 12)
	w = newWalker(t, walkerOpts{endsAt: &end})
	cs = w.WalkTo(utc(2026, 4, 10), nil)
	eqInts(t, "ended", indexes(cs), []int{0, 1})
	eqTime(t, "ended at", cs[1].EndedAt, end)
	if w.Current() != nil {
		t.Errorf("ended: got = a current cycle, want = nil")
	}
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:427
	eqInts(t, "ends at start", indexes(newWalker(t, walkerOpts{endsAt: tp(utc(2026, 1, 31))}).WalkTo(utc(2026, 1, 31), nil)), []int{})
}

func TestWalkerRepositioning(t *testing.T) {
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:433
	w := newWalker(t, walkerOpts{})
	eqTime(t, "resume inside", w.Resume(tp(utc(2026, 2, 10))).StartedAt, utc(2026, 1, 31))
	eqTime(t, "then advance", w.Advance().EndedAt, utc(2026, 3, 31))
	if w.Current().Index != 1 {
		t.Errorf("index: got = %d, want = 1", w.Current().Index)
	}
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:439
	w = newWalker(t, walkerOpts{})
	w.Resume(tp(utc(2026, 4, 1)))
	w.Advance()
	eqSpan(t, "backwards", w.Resume(tp(utc(2026, 2, 10))), span{0, utc(2026, 1, 31), utc(2026, 2, 28)})
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:449
	w = newWalker(t, walkerOpts{})
	w.Resume(tp(utc(2026, 4, 1)))
	w.Advance()
	eqInts(t, "from nil", indexes(w.WalkTo(utc(2026, 4, 30), nil)), []int{0, 1, 2, 3})
	eqInts(t, "from date", indexes(w.WalkTo(utc(2026, 4, 30), tp(utc(2026, 3, 31)))), []int{2, 3})
	if w.Advance().Index != 4 {
		t.Errorf("advance: got = %d, want = 4", w.Current().Index)
	}
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:459
	w = newWalker(t, walkerOpts{})
	w.Resume(tp(utc(2026, 4, 1)))
	w.Advance()
	if w.Resume(nil).Index != 0 || w.Advance().Index != 1 {
		t.Errorf("restart: got = %d, want = 1", w.Current().Index)
	}
	// source: spec/services/billing/rate_cards/cycle_walker_spec.rb:470
	end := utc(2026, 4, 15)
	w = newWalker(t, walkerOpts{endsAt: &end})
	eqTime(t, "ends inside", w.Resume(tp(utc(2026, 4, 1))).EndedAt, end)
	first, second := w.Advance(), w.Advance()
	if first != nil || second != nil || w.Resume(&end) != nil || w.Current() != nil {
		t.Errorf("ends inside: got = a cycle, want = nil")
	}
	if w.Resume(tp(utc(2026, 2, 1))).Index != 0 {
		t.Errorf("earlier: got = %d, want = 0", w.Current().Index)
	}
}
