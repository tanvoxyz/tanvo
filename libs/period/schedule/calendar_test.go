package schedule

import (
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/period"
)

func calendarFor(t *testing.T, anchor Date, count int, unit Unit, tz string) *Calendar {
	t.Helper()
	return NewCalendar(anchor, mustInterval(t, count, unit), zone(t, tz))
}

func eqPeriod(t *testing.T, label string, got, want period.Period) {
	t.Helper()
	if !got.From.Equal(want.From) || !got.To.Equal(want.To) {
		t.Errorf("%s: got = %s, want = %s", label, got.UTC(), want.UTC())
	}
}

func eqRatio(t *testing.T, label string, got Ratio, days, total int) {
	t.Helper()
	if got.Float() != float64(days)/float64(total) {
		t.Errorf("%s: got = %d/%d, want = %d/%d", label, got.Days, got.Total, days, total)
	}
}

func TestCalendarAnchor(t *testing.T) {
	// source: spec/services/billing/calendar_spec.rb:23
	eqTime(t, "utc", calendarFor(t, date(2022, 2, 1), 1, Month, "UTC").IntervalContaining(utc(2022, 2, 15)).From, utc(2022, 2, 1))
	// source: spec/services/billing/calendar_spec.rb:30
	eqTime(t, "ny", calendarFor(t, date(2022, 2, 1), 1, Month, "America/New_York").IntervalContaining(utc(2022, 2, 15)).From, utc(2022, 2, 1, 5))

	// source: spec/services/billing/calendar_spec.rb:40-53
	monthEnd := calendarFor(t, date(2022, 1, 31), 1, Month, "UTC")
	for _, c := range []struct{ at, opens time.Time }{
		{utc(2022, 1, 30), utc(2021, 12, 31)},
		{utc(2022, 1, 31), utc(2022, 1, 31)},
		{utc(2022, 2, 15), utc(2022, 1, 31)},
		{utc(2022, 2, 27), utc(2022, 1, 31)},
		{utc(2022, 2, 28), utc(2022, 2, 28)},
		{utc(2022, 3, 31), utc(2022, 3, 31)},
	} {
		eqTime(t, c.at.String(), monthEnd.IntervalContaining(c.at).From, c.opens)
	}
	// source: spec/services/billing/calendar_spec.rb:60-70
	at := utc(2022, 3, 1, 2)
	eqTime(t, "reached in utc", calendarFor(t, date(2022, 2, 1), 1, Month, "UTC").IntervalContaining(at).From, utc(2022, 3, 1))
	eqTime(t, "not reached in ny", calendarFor(t, date(2022, 2, 1), 1, Month, "America/New_York").IntervalContaining(at).From, utc(2022, 2, 1, 5))
}

// source: spec/services/billing/calendar_spec.rb:74-116
func TestCalendarIntervalContaining(t *testing.T) {
	for _, c := range []struct {
		anchor Date
		count  int
		unit   Unit
		at     time.Time
		want   period.Period
	}{
		{date(2022, 2, 1), 1, Month, utc(2022, 2, 10), period.Period{From: utc(2022, 2, 1), To: utc(2022, 3, 1)}},
		{date(2022, 2, 1), 1, Month, utc(2022, 1, 10), period.Period{From: utc(2022, 1, 1), To: utc(2022, 2, 1)}},
		{date(2022, 1, 31), 1, Month, utc(2022, 2, 15), period.Period{From: utc(2022, 1, 31), To: utc(2022, 2, 28)}},
		{date(2022, 1, 31), 1, Month, utc(2022, 3, 1), period.Period{From: utc(2022, 2, 28), To: utc(2022, 3, 31)}},
		{date(2022, 1, 1), 3, Month, utc(2022, 8, 1), period.Period{From: utc(2022, 7, 1), To: utc(2022, 10, 1)}},
		{date(2022, 1, 1), 1, Week, utc(2022, 1, 25), period.Period{From: utc(2022, 1, 22), To: utc(2022, 1, 29)}},
		{date(2022, 1, 1), 1, Day, utc(2022, 2, 15, 12), period.Period{From: utc(2022, 2, 15), To: utc(2022, 2, 16)}},
		{date(2024, 2, 29), 1, Year, utc(2025, 3, 1), period.Period{From: utc(2025, 2, 28), To: utc(2026, 2, 28)}},
	} {
		eqPeriod(t, c.at.String(), calendarFor(t, c.anchor, c.count, c.unit, "UTC").IntervalContaining(c.at), c.want)
	}
	// source: spec/services/billing/calendar_spec.rb:111
	w := calendarFor(t, date(2022, 2, 1), 1, Month, "UTC").IntervalContaining(utc(2022, 2, 10))
	if !w.Contains(utc(2022, 2, 28, 23, 59, 59)) || w.Contains(utc(2022, 3, 1)) {
		t.Errorf("half-open: got = %s, want = [Feb 1, Mar 1)", w)
	}
}

// source: spec/services/billing/calendar_spec.rb:122-180
func TestCalendarPositions(t *testing.T) {
	m := calendarFor(t, date(2026, 1, 31), 1, Month, "UTC")
	for _, c := range []struct {
		source string
		got    int
		want   int
	}{
		{"spec/services/billing/calendar_spec.rb:128", m.IntervalsBetween(utc(2026, 2, 28), utc(2026, 3, 31)), 1},
		{"spec/services/billing/calendar_spec.rb:128", m.IntervalsBetween(utc(2026, 2, 28), utc(2026, 3, 28)), 0},
		{"spec/services/billing/calendar_spec.rb:133", m.IntervalsBetween(utc(2026, 2, 28), utc(2026, 5, 31)), 3},
		{"spec/services/billing/calendar_spec.rb:133", m.IntervalsBetween(utc(2026, 5, 31), utc(2026, 2, 28)), -3},
		{"spec/services/billing/calendar_spec.rb:133", m.IntervalsBetween(utc(2026, 2, 28), utc(2026, 3, 31)) + m.IntervalsBetween(utc(2026, 3, 31), utc(2026, 5, 31)), 3},
		{"spec/services/billing/calendar_spec.rb:143", calendarFor(t, date(2026, 3, 1), 1, Day, "America/New_York").IntervalsBetween(utc(2026, 3, 8, 5), utc(2026, 3, 9, 4)), 1},
	} {
		if c.got != c.want {
			t.Errorf("%s: got = %d, want = %d", c.source, c.got, c.want)
		}
	}
	// source: spec/services/billing/calendar_spec.rb:151
	eqTime(t, "after 1", m.BoundaryAfter(utc(2026, 2, 10), 1), utc(2026, 2, 28))
	eqTime(t, "after 2", m.BoundaryAfter(utc(2026, 2, 10), 2), utc(2026, 3, 31))
	// source: spec/services/billing/calendar_spec.rb:156
	eqTime(t, "containing", m.BoundaryAfter(utc(2026, 3, 1), 1), m.BoundaryAfter(utc(2026, 3, 30), 1))
	// source: spec/services/billing/calendar_spec.rb:163-170
	eqTime(t, "on boundary", m.BoundaryAtOrAfter(utc(2026, 2, 28)), utc(2026, 2, 28))
	eqTime(t, "inside", m.BoundaryAtOrAfter(utc(2026, 2, 28, 0, 0, 1)), utc(2026, 3, 31))
	eqTime(t, "inside late", m.BoundaryAtOrAfter(utc(2026, 3, 30)), utc(2026, 3, 31))
	// source: spec/services/billing/calendar_spec.rb:174
	for _, at := range []time.Time{utc(2026, 2, 28), utc(2026, 3, 15), utc(2026, 3, 31), utc(2026, 4, 30, 23, 59, 59)} {
		if m.BoundaryAtOrAfter(at).Before(at) {
			t.Errorf("at %s: got = %s, want = at or after", at, m.BoundaryAtOrAfter(at))
		}
	}
}

// source: spec/services/billing/calendar_spec.rb:183-276
func TestCalendarProrationRatio(t *testing.T) {
	c := calendarFor(t, date(2022, 6, 1), 1, Month, "UTC")
	ratio := func(from, to time.Time) Ratio {
		t.Helper()
		r, err := c.ProrationRatio(from, to)
		if err != nil {
			t.Fatalf("[%s, %s): %v", from, to, err)
		}
		return r
	}
	eqRatio(t, "whole", ratio(utc(2022, 6, 1), utc(2022, 7, 1)), 1, 1)
	eqRatio(t, "start", ratio(utc(2022, 6, 1), utc(2022, 6, 16)), 1, 2)
	eqRatio(t, "both", ratio(utc(2022, 6, 28), utc(2022, 7, 1)), 1, 10)
	for _, w := range [][2]time.Time{
		{utc(2022, 6, 29), utc(2022, 7, 2)},
		{utc(2022, 6, 1), utc(2022, 8, 1)},
		{utc(2022, 6, 20), utc(2022, 6, 10)},
	} {
		if _, err := c.ProrationRatio(w[0], w[1]); !errors.Is(err, ErrInvalid) {
			t.Errorf("[%s, %s): err = %v, want = ErrInvalid", w[0], w[1], err)
		}
	}
	eqRatio(t, "empty", ratio(utc(2022, 6, 1), utc(2022, 6, 1)), 0, 30)
	for _, x := range []struct {
		anchor Date
		count  int
		unit   Unit
		at     time.Time
		days   int
	}{
		{date(2022, 2, 1), 1, Month, utc(2022, 2, 10), 28},
		{date(2024, 2, 1), 1, Month, utc(2024, 2, 10), 29},
		{date(2022, 6, 1), 1, Month, utc(2022, 6, 10), 30},
		{date(2022, 7, 1), 1, Month, utc(2022, 7, 10), 31},
		{date(2022, 1, 1), 3, Month, utc(2022, 2, 10), 90},
		{date(2022, 1, 1), 2, Week, utc(2022, 1, 10), 14},
	} {
		cal := calendarFor(t, x.anchor, x.count, x.unit, "UTC")
		w := cal.IntervalContaining(x.at)
		r, _ := cal.ProrationRatio(w.From, w.From.AddDate(0, 0, 1))
		eqRatio(t, "one day", r, 1, x.days)
		r, _ = cal.ProrationRatio(w.From, w.To)
		eqRatio(t, "whole", r, 1, 1)
	}
	eqRatio(t, "few hours", ratio(utc(2022, 6, 1), utc(2022, 6, 1, 9)), 1, 30)
	eqRatio(t, "started day", ratio(utc(2022, 6, 1), utc(2022, 6, 15, 14, 30)), 15, 30)
	eqRatio(t, "midnight", ratio(utc(2022, 6, 1), utc(2022, 6, 15)), 14, 30)
	ny := zone(t, "America/New_York")
	spring := calendarFor(t, date(2026, 3, 1), 1, Month, "America/New_York")
	r, _ := spring.ProrationRatio(local(ny, 2026, 3, 1), local(ny, 2026, 4, 1))
	eqRatio(t, "spring forward", r, 1, 1)
	fall := calendarFor(t, date(2026, 11, 1), 1, Month, "America/New_York")
	r, _ = fall.ProrationRatio(local(ny, 2026, 11, 1), local(ny, 2026, 12, 1))
	eqRatio(t, "fall back", r, 1, 1)
}

func walkWindows(c *Calendar, from time.Time, n int) []period.Period {
	out := make([]period.Period, 0, n)
	for range n {
		w := c.IntervalContaining(from)
		out = append(out, w)
		from = w.To
	}
	return out
}

// source: spec/services/billing/calendar_spec.rb:281-358
func TestCalendarQAScenarios(t *testing.T) {
	for _, c := range []struct {
		label  string
		count  int
		unit   Unit
		bounds [4]string
	}{
		{"day x 1", 1, Day, [4]string{"2026-08-10", "2026-08-10", "2026-08-11", "2026-08-11"}},
		{"day x 45", 45, Day, [4]string{"2026-08-10", "2026-09-23", "2026-09-24", "2026-11-07"}},
		{"week x 1", 1, Week, [4]string{"2026-08-10", "2026-08-16", "2026-08-17", "2026-08-23"}},
		{"week x 4", 4, Week, [4]string{"2026-08-10", "2026-09-06", "2026-09-07", "2026-10-04"}},
		{"month x 3", 3, Month, [4]string{"2026-08-10", "2026-11-09", "2026-11-10", "2027-02-09"}},
		{"month x 6", 6, Month, [4]string{"2026-08-10", "2027-02-09", "2027-02-10", "2027-08-09"}},
		{"year x 1", 1, Year, [4]string{"2026-08-10", "2027-08-09", "2027-08-10", "2028-08-09"}},
		{"year x 2", 2, Year, [4]string{"2026-08-10", "2028-08-09", "2028-08-10", "2030-08-09"}},
	} {
		// source: spec/services/billing/calendar_spec.rb:304
		ws := walkWindows(calendarFor(t, date(2026, 8, 10), c.count, c.unit, "UTC"), utc(2026, 8, 10), 2)
		got := [4]string{
			ws[0].From.Format(time.DateOnly), ws[0].To.AddDate(0, 0, -1).Format(time.DateOnly),
			ws[1].From.Format(time.DateOnly), ws[1].To.AddDate(0, 0, -1).Format(time.DateOnly),
		}
		if got != c.bounds {
			t.Errorf("BC %s: got = %v, want = %v", c.label, got, c.bounds)
		}
	}
	starts := func(ws []period.Period) []string {
		var out []string
		for _, w := range ws {
			out = append(out, w.From.Format(time.DateOnly))
		}
		return out
	}
	for _, c := range []struct {
		source string
		got    []string
		want   []string
	}{
		{
			"spec/services/billing/calendar_spec.rb:315", starts(walkWindows(calendarFor(t, date(2026, 8, 31), 1, Month, "UTC"), utc(2026, 8, 31), 4)),
			[]string{"2026-08-31", "2026-09-30", "2026-10-31", "2026-11-30"},
		},
		{
			"spec/services/billing/calendar_spec.rb:324", starts(walkWindows(calendarFor(t, date(2028, 1, 31), 1, Month, "UTC"), utc(2028, 1, 31), 2)),
			[]string{"2028-01-31", "2028-02-29"},
		},
		{
			"spec/services/billing/calendar_spec.rb:332", starts(walkWindows(calendarFor(t, date(2028, 2, 29), 1, Year, "UTC"), utc(2028, 2, 29), 5)),
			[]string{"2028-02-29", "2029-02-28", "2030-02-28", "2031-02-28", "2032-02-29"},
		},
	} {
		if len(c.got) != len(c.want) {
			t.Errorf("%s: got = %v, want = %v", c.source, c.got, c.want)
			continue
		}
		for i := range c.got {
			if c.got[i] != c.want[i] {
				t.Errorf("%s: got = %v, want = %v", c.source, c.got, c.want)
				break
			}
		}
	}
	// source: spec/services/billing/calendar_spec.rb:349
	an := calendarFor(t, date(2026, 9, 1), 1, Month, "UTC")
	stub := an.IntervalContaining(utc(2026, 8, 20))
	eqPeriod(t, "stub", stub, period.Period{From: utc(2026, 8, 1), To: utc(2026, 9, 1)})
	r, _ := an.ProrationRatio(utc(2026, 8, 20), stub.To)
	eqRatio(t, "stub ratio", r, 12, 31)
	eqPeriod(t, "after stub", an.IntervalContaining(stub.To), period.Period{From: utc(2026, 9, 1), To: utc(2026, 10, 1)})
}

// source: spec/services/billing/calendar_spec.rb:360-381
func TestCalendarTimeZones(t *testing.T) {
	ny := zone(t, "America/New_York")
	c := calendarFor(t, date(2026, 1, 1), 1, Month, "America/New_York")
	offsets := map[int]bool{}
	for m := time.January; m <= time.December; m++ {
		s := c.IntervalContaining(utc(2026, m, 15, 12)).From.In(ny)
		if s.Hour() != 0 || s.Minute() != 0 {
			t.Errorf("%s: got = %s, want = local midnight", m, s)
		}
		_, off := s.Zone()
		offsets[off] = true
	}
	if len(offsets) != 2 || !offsets[-5*3600] || !offsets[-4*3600] {
		t.Errorf("offsets: got = %v, want = -5h and -4h", offsets)
	}
	kolkata := zone(t, "Asia/Kolkata")
	w := calendarFor(t, date(2022, 6, 1), 1, Month, "Asia/Kolkata").IntervalContaining(utc(2022, 6, 15))
	b := w.From.In(kolkata)
	_, off := b.Zone()
	if b.Hour() != 0 || b.Minute() != 0 || off != 5*3600+1800 || w.To.Sub(w.From) != 30*24*time.Hour {
		t.Errorf("kolkata: got = %s, want = local midnight, +05:30, 30 days", w)
	}
}

// source: spec/services/billing/calendar_spec.rb:387-454
func TestCalendarCutDays(t *testing.T) {
	c := calendarFor(t, date(2022, 6, 1), 1, Month, "UTC")
	w := c.IntervalContaining(utc(2022, 6, 15))
	sum := func(cal *Calendar, cuts ...time.Time) float64 {
		var total float64
		for i := range len(cuts) - 1 {
			r, err := cal.ProrationRatio(cuts[i], cuts[i+1])
			if err != nil {
				t.Fatal(err)
			}
			total += r.Float()
		}
		return total
	}
	// source: spec/services/billing/calendar_spec.rb:392
	r1, _ := c.ProrationRatio(w.From, utc(2022, 6, 16, 14))
	r2, _ := c.ProrationRatio(utc(2022, 6, 16, 14), w.To)
	eqRatio(t, "before cut", r1, 16, 30)
	eqRatio(t, "after cut", r2, 14, 30)
	// source: spec/services/billing/calendar_spec.rb:399
	for _, cut := range []time.Time{utc(2022, 6, 16), utc(2022, 6, 16, 0, 0, 1), utc(2022, 6, 16, 14), utc(2022, 6, 30, 23, 59, 59)} {
		if got := sum(c, w.From, cut, w.To); got != 1 {
			t.Errorf("cut %s: got = %v, want = 1", cut, got)
		}
	}
	// source: spec/services/billing/calendar_spec.rb:414
	if got := sum(c, w.From, utc(2022, 6, 6, 7), utc(2022, 6, 20, 22), w.To); got != 1 {
		t.Errorf("two cuts: got = %v, want = 1", got)
	}
	// source: spec/services/billing/calendar_spec.rb:428
	for _, tz := range []string{"Europe/Paris", "America/New_York", "Asia/Kolkata", "Australia/Lord_Howe"} {
		cal := calendarFor(t, date(2022, 6, 1), 1, Month, tz)
		win := cal.IntervalContaining(utc(2022, 6, 15))
		if got := sum(cal, win.From, utc(2022, 6, 16), win.To); got != 1 {
			t.Errorf("%s: got = %v, want = 1", tz, got)
		}
	}
	// source: spec/services/billing/calendar_spec.rb:441
	ny := zone(t, "America/New_York")
	for _, x := range []struct {
		anchor Date
		cut    time.Time
	}{
		{date(2026, 3, 1), local(ny, 2026, 3, 8, 3, 30)},
		{date(2026, 11, 1), local(ny, 2026, 11, 1, 1, 30)},
	} {
		cal := calendarFor(t, x.anchor, 1, Month, "America/New_York")
		win := cal.IntervalContaining(x.cut)
		if got := sum(cal, win.From, x.cut, win.To); got != 1 {
			t.Errorf("dst %s: got = %v, want = 1", x.cut, got)
		}
	}
}

// source: spec/services/billing/calendar_spec.rb:458-554
func TestCalendarInvariants(t *testing.T) {
	anchors := []Date{date(2022, 1, 31), date(2024, 2, 29), date(2022, 7, 15), date(2021, 12, 1)}
	zones := []string{"UTC", "America/New_York", "Asia/Tokyo"}
	offsets := []int{-400, -95, -31, -30, -1, 0, 1, 27, 28, 30, 31, 59, 90, 365, 366}
	times := []time.Duration{0, 7*time.Hour + 31*time.Minute, 23*time.Hour + 59*time.Minute}
	for _, unit := range Units() {
		for _, count := range []int{1, 3} {
			for _, tz := range zones {
				loc := zone(t, tz)
				for _, a := range anchors {
					c := calendarFor(t, a, count, unit, tz)
					for _, off := range offsets {
						for _, tod := range times {
							at := Interval{1, Day}.Advance(a.Start(loc), off).Add(tod)
							w := c.IntervalContaining(at)
							// source: spec/services/billing/calendar_spec.rb:468
							if !w.Contains(at) {
								t.Errorf("%d %s on %s in %s at %s: got = %s", count, unit, a, tz, at, w)
							}
							// source: spec/services/billing/calendar_spec.rb:483
							r1, err1 := c.ProrationRatio(w.From, at)
							r2, err2 := c.ProrationRatio(at, w.To)
							if err1 != nil || err2 != nil || abs(r1.Float()+r2.Float()-1) > 1e-9 {
								t.Errorf("%d %s on %s in %s cut at %s: got = %v + %v", count, unit, a, tz, at, r1, r2)
							}
							// source: spec/services/billing/calendar_spec.rb:525
							if !c.IntervalContaining(w.To).From.Equal(w.To) {
								t.Errorf("%d %s on %s in %s at %s: gap after %s", count, unit, a, tz, at, w.To)
							}
						}
					}
				}
			}
		}
	}
	// source: spec/services/billing/calendar_spec.rb:500
	fractions := []float64{0.11, 0.37, 0.5, 0.5001, 0.83, 0.9999}
	for _, tz := range zones {
		loc := zone(t, tz)
		for _, iv := range []Interval{{1, Month}, {3, Month}, {1, Week}, {1, Year}} {
			for _, a := range anchors {
				c := NewCalendar(a, iv, loc)
				w := c.IntervalContaining(a.Start(loc).AddDate(0, 0, 2))
				span := w.To.Sub(w.From)
				cuts := []time.Time{w.From}
				for _, f := range fractions {
					cuts = append(cuts, w.From.Add(time.Duration(float64(span)*f)))
				}
				cuts = append(cuts, w.To)
				var total float64
				for i := range len(cuts) - 1 {
					r, err := c.ProrationRatio(cuts[i], cuts[i+1])
					if err != nil {
						t.Fatal(err)
					}
					total += r.Float()
				}
				if abs(total-1) > 1e-9 {
					t.Errorf("%v on %s in %s: got = %v, want = 1", iv, a, tz, total)
				}
			}
		}
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
