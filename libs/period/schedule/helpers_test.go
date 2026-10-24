package schedule

import (
	"testing"
	"time"
)

func utc(y int, m time.Month, d int, hms ...int) time.Time {
	h := make([]int, 3)
	copy(h, hms)
	return time.Date(y, m, d, h[0], h[1], h[2], 0, time.UTC)
}

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func local(loc *time.Location, y int, m time.Month, d int, hm ...int) time.Time {
	h := make([]int, 2)
	copy(h, hm)
	return wall(y, m, d, h[0], h[1], 0, 0, loc)
}

func date(y int, m time.Month, d int) Date { return Date{y, m, d} }

func intp(n int) *int { return &n }

func tp(t time.Time) *time.Time { return &t }

func rate(at time.Time, count int, unit Unit) *Rate {
	return &Rate{ID: at.Format(time.RFC3339), EffectiveFrom: at, IntervalCount: count, IntervalUnit: unit}
}

func mustInterval(t *testing.T, count int, unit Unit) Interval {
	t.Helper()
	i, err := NewInterval(count, unit)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func eqTime(t *testing.T, label string, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Errorf("%s: got = %s, want = %s", label, got.UTC(), want.UTC())
	}
}
