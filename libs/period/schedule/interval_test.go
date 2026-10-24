package schedule

import (
	"errors"
	"testing"
	"time"
)

// source: spec/services/billing/interval_spec.rb:11-43
func TestNewIntervalRejects(t *testing.T) {
	for _, c := range []struct {
		source string
		count  int
		unit   Unit
	}{
		{"spec/services/billing/interval_spec.rb:11", 1, "fortnight"},
		{"spec/services/billing/interval_spec.rb:16", 0, Month},
		{"spec/services/billing/interval_spec.rb:21", -1, Month},
		{"spec/services/billing/interval_spec.rb:40", 1, ""},
	} {
		if _, err := NewInterval(c.count, c.unit); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want = ErrInvalid", c.source, err)
		}
	}
}

// source: spec/services/billing/interval_spec.rb:49-78
func TestIntervalOf(t *testing.T) {
	r := &Rate{IntervalCount: 3, IntervalUnit: Month}
	for _, c := range []struct {
		source string
		o      *Override
		want   Interval
	}{
		{"spec/services/billing/interval_spec.rb:52", nil, Interval{3, Month}},
		{"spec/services/billing/interval_spec.rb:56", &Override{IntervalCount: intp(2), IntervalUnit: Week}, Interval{2, Week}},
		{"spec/services/billing/interval_spec.rb:62", &Override{IntervalUnit: Week}, Interval{3, Week}},
		{"spec/services/billing/interval_spec.rb:68", &Override{IntervalCount: intp(2)}, Interval{2, Month}},
		{"spec/services/billing/interval_spec.rb:74", &Override{}, Interval{3, Month}},
	} {
		got, err := IntervalOf(r, c.o)
		if err != nil || got != c.want {
			t.Errorf("%s: got = %v (%v), want = %v", c.source, got, err, c.want)
		}
	}
}

// source: spec/services/billing/interval_spec.rb:81-131
func TestIntervalAdvance(t *testing.T) {
	for _, c := range []struct {
		count int
		unit  Unit
		from  time.Time
		steps int
		want  time.Time
	}{
		{1, Month, utc(2022, 1, 31), 1, utc(2022, 2, 28)},
		{1, Month, utc(2022, 1, 31), 2, utc(2022, 3, 31)},
		{1, Month, utc(2022, 1, 31), -1, utc(2021, 12, 31)},
		{1, Month, utc(2022, 1, 31), 0, utc(2022, 1, 31)},
		{3, Month, utc(2022, 1, 31), 1, utc(2022, 4, 30)},
		{3, Month, utc(2022, 1, 1), 2, utc(2022, 7, 1)},
		{1, Day, utc(2022, 1, 31), 10, utc(2022, 2, 10)},
		{2, Week, utc(2022, 1, 31), 1, utc(2022, 2, 14)},
		{1, Year, utc(2024, 2, 29), 1, utc(2025, 2, 28)},
	} {
		// source: spec/services/billing/interval_spec.rb:93
		eqTime(t, c.from.String(), mustInterval(t, c.count, c.unit).Advance(c.from, c.steps), c.want)
	}

	paris := zone(t, "Europe/Paris")
	midnight := local(paris, 2026, 3, 1)
	monthly := mustInterval(t, 1, Month)
	// source: spec/services/billing/interval_spec.rb:107
	eqTime(t, "local month", monthly.Advance(midnight, 1), local(paris, 2026, 4, 1))
	eqTime(t, "utc month", monthly.Advance(midnight.UTC(), 1), utc(2026, 3, 28, 23))
	// source: spec/services/billing/interval_spec.rb:114
	got := mustInterval(t, 1, Week).Advance(midnight, 5)
	eqTime(t, "week across DST", got, local(paris, 2026, 4, 5))
	if h, m, s := got.In(paris).Clock(); h != 0 || m != 0 || s != 0 {
		t.Errorf("week across DST: got = %s, want = local midnight", got.In(paris))
	}
	// source: spec/services/billing/interval_spec.rb:125
	origin := utc(2022, 1, 31)
	for i, want := range []time.Time{utc(2022, 1, 31), utc(2022, 2, 28), utc(2022, 3, 31), utc(2022, 4, 30)} {
		eqTime(t, "no drift", monthly.Advance(origin, i), want)
	}
}

// source: spec/services/billing/interval_spec.rb:134-197
func TestIntervalStepsBetween(t *testing.T) {
	for _, c := range []struct {
		count    int
		unit     Unit
		from, to time.Time
		want     int
	}{
		{1, Month, utc(2022, 1, 31), utc(2022, 1, 31), 0},
		{1, Month, utc(2022, 1, 31), utc(2022, 2, 15), 0},
		{1, Month, utc(2022, 1, 31), utc(2022, 2, 28), 1},
		{1, Month, utc(2022, 1, 31), utc(2021, 12, 15), -2},
		{3, Month, utc(2022, 1, 1), utc(2022, 8, 1), 2},
		{3, Month, utc(2022, 1, 31), utc(2022, 4, 15), 0},
		{3, Month, utc(2022, 1, 1), utc(2021, 11, 1), -1},
		{1, Day, utc(2022, 1, 1), utc(2022, 1, 10), 9},
		{3, Day, utc(2022, 1, 11), utc(2022, 1, 1), -4},
		{1, Week, utc(2022, 1, 1), utc(2022, 1, 15), 2},
		{1, Week, utc(2022, 1, 15), utc(2022, 1, 1), -2},
		{1, Year, utc(2022, 6, 1), utc(2025, 1, 1), 2},
	} {
		// source: spec/services/billing/interval_spec.rb:151
		if got := mustInterval(t, c.count, c.unit).StepsBetween(c.from, c.to); got != c.want {
			t.Errorf("%d %s from %s to %s: got = %d, want = %d", c.count, c.unit, c.from, c.to, got, c.want)
		}
	}

	// source: spec/services/billing/interval_spec.rb:166
	paris := zone(t, "Europe/Paris")
	froms := []time.Time{
		utc(2022, 1, 31), utc(2024, 2, 29), utc(2022, 7, 15), utc(2021, 12, 1), utc(2022, 6, 30),
		local(paris, 2026, 3, 29), local(paris, 2026, 10, 25),
	}
	offsets := []int{-400, -95, -31, -30, -29, -1, 0, 1, 27, 28, 29, 30, 31, 32, 59, 90, 365, 366, 730}
	for _, unit := range Units() {
		for _, count := range []int{1, 3} {
			i := mustInterval(t, count, unit)
			for _, from := range froms {
				for _, off := range offsets {
					to := Interval{1, Day}.Advance(from, off)
					steps := i.StepsBetween(from, to)
					if i.Advance(from, steps).After(to) || !i.Advance(from, steps+1).After(to) {
						t.Errorf("%d %s from %s to %s gave %d", count, unit, from, to, steps)
					}
				}
			}
		}
	}
}
