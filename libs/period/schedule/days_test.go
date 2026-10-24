package schedule

import (
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/period"
)

// source: spec/services/billing/days_spec.rb:9-69
func TestDaysBetween(t *testing.T) {
	ny := zone(t, "America/New_York")
	for _, c := range []struct {
		source   string
		from, to time.Time
		loc      *time.Location
		want     int
	}{
		{"spec/services/billing/days_spec.rb:10", utc(2026, 6, 1), utc(2026, 7, 1), time.UTC, 30},
		{"spec/services/billing/days_spec.rb:14", utc(2026, 6, 1, 9), utc(2026, 6, 1, 17), time.UTC, 0},
		{"spec/services/billing/days_spec.rb:20", utc(2026, 6, 16, 9, 30), utc(2026, 7, 1), time.UTC, 14},
		{"spec/services/billing/days_spec.rb:24", utc(2026, 6, 16), utc(2026, 7, 1), time.UTC, 15},
		{"spec/services/billing/days_spec.rb:35", utc(2026, 6, 1, 4), utc(2026, 6, 15, 12), ny, 15},
		{"spec/services/billing/days_spec.rb:35", utc(2026, 6, 15, 12), utc(2026, 7, 1, 4), ny, 15},
		{"spec/services/billing/days_spec.rb:64", utc(2026, 3, 1, 5), utc(2026, 4, 1, 4), ny, 31},
		{"spec/services/billing/days_spec.rb:64", utc(2026, 11, 1, 4), utc(2026, 12, 1, 5), ny, 30},
	} {
		if got := DaysBetween(c.from, c.to, c.loc); got != c.want {
			t.Errorf("%s: got = %d, want = %d", c.source, got, c.want)
		}
	}
	// source: spec/services/billing/days_spec.rb:47
	whole := DaysBetween(utc(2026, 3, 1), utc(2026, 4, 1), time.UTC)
	for _, h := range []int{0, 9, 14, 23} {
		cut := utc(2026, 3, 15, h)
		if got := DaysBetween(utc(2026, 3, 1), cut, time.UTC) + DaysBetween(cut, utc(2026, 4, 1), time.UTC); got != whole {
			t.Errorf("cut at %d: got = %d, want = %d", h, got, whole)
		}
	}
}

// source: spec/services/billing/phase_spec.rb:5-56
func TestPhase(t *testing.T) {
	d := DefaultPhase()
	if !d.Unbounded() || d.Override != nil || d.Code != "" {
		t.Errorf("default: got = %+v, want = unbounded, no override, no code", d)
	}
	if p, _ := NewPhase("tail", nil, nil); !p.Unbounded() {
		t.Errorf("tail: got = bounded, want = unbounded")
	}
	if p, _ := NewPhase("intro", intp(1), nil); p.Unbounded() {
		t.Errorf("intro: got = unbounded, want = bounded")
	}
	if _, err := NewPhase("empty", intp(0), nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty: err = %v, want = ErrInvalid", err)
	}
}

// source: spec/services/billing/terms_spec.rb:5-57
func TestTerms(t *testing.T) {
	if _, err := NewTerms("upfront", true); !errors.Is(err, ErrInvalid) {
		t.Errorf("upfront: err = %v, want = ErrInvalid", err)
	}
	if _, err := NewTerms("", true); !errors.Is(err, ErrInvalid) {
		t.Errorf("nil timing: err = %v, want = ErrInvalid", err)
	}
	w := period.Period{From: utc(2026, 8, 10), To: utc(2026, 9, 10)}
	arrears, _ := NewTerms(Arrears, true)
	advance, _ := NewTerms(Advance, true)
	eqTime(t, "arrears", arrears.BillingAt(w), w.To)
	eqTime(t, "advance", advance.BillingAt(w), w.From)
	eqTime(t, "slice", arrears.BillingAt(period.Period{From: w.From, To: utc(2026, 8, 25)}), utc(2026, 8, 25))
}

// source: spec/services/billing/segments_spec.rb:20-111
func TestWithin(t *testing.T) {
	w := period.Period{From: utc(2026, 9, 10), To: utc(2026, 10, 10)}
	named := func(code string, at time.Time) *Rate { return &Rate{ID: code, EffectiveFrom: at} }
	type win struct{ from, to, rate string }
	render := func(ss []Slice) []win {
		var out []win
		for _, s := range ss {
			code := ""
			if s.Rate != nil {
				code = s.Rate.ID
			}
			out = append(out, win{s.From.Format(time.DateOnly), s.To.Format(time.DateOnly), code})
		}
		return out
	}
	for _, c := range []struct {
		source string
		rates  []*Rate
		want   []win
	}{
		{
			"spec/services/billing/segments_spec.rb:21",
			[]*Rate{named("v1", utc(2026, 1, 1))},
			[]win{{"2026-09-10", "2026-10-10", "v1"}},
		},
		{
			"spec/services/billing/segments_spec.rb:28",
			[]*Rate{named("v1", utc(2026, 1, 1)), named("v2", utc(2026, 9, 25))},
			[]win{{"2026-09-10", "2026-09-25", "v1"}, {"2026-09-25", "2026-10-10", "v2"}},
		},
		{
			"spec/services/billing/segments_spec.rb:39",
			[]*Rate{named("v1", utc(2026, 1, 1)), named("v2", utc(2026, 9, 20)), named("v3", utc(2026, 9, 30))},
			[]win{{"2026-09-10", "2026-09-20", "v1"}, {"2026-09-20", "2026-09-30", "v2"}, {"2026-09-30", "2026-10-10", "v3"}},
		},
		{
			"spec/services/billing/segments_spec.rb:57",
			[]*Rate{named("v1", utc(2026, 1, 1)), named("v2", utc(2026, 9, 10))},
			[]win{{"2026-09-10", "2026-10-10", "v2"}},
		},
		{
			"spec/services/billing/segments_spec.rb:63",
			[]*Rate{named("v1", utc(2026, 1, 1)), named("v2", utc(2026, 10, 10))},
			[]win{{"2026-09-10", "2026-10-10", "v1"}},
		},
		{
			"spec/services/billing/segments_spec.rb:69",
			[]*Rate{named("v1", utc(2027, 1, 1))},
			[]win{{"2026-09-10", "2026-10-10", ""}},
		},
		{
			"spec/services/billing/segments_spec.rb:75",
			[]*Rate{named("v1", utc(2026, 9, 25))},
			[]win{{"2026-09-10", "2026-09-25", ""}, {"2026-09-25", "2026-10-10", "v1"}},
		},
		{
			"spec/services/billing/segments_spec.rb:84",
			[]*Rate{named("v1", w.To)},
			[]win{{"2026-09-10", "2026-10-10", ""}},
		},
		{
			"spec/services/billing/segments_spec.rb:90",
			[]*Rate{named("v2", utc(2026, 9, 25)), named("v1", utc(2026, 1, 1))},
			[]win{{"2026-09-10", "2026-09-25", "v1"}, {"2026-09-25", "2026-10-10", "v2"}},
		},
		{
			"spec/services/billing/segments_spec.rb:96", nil,
			[]win{{"2026-09-10", "2026-10-10", ""}},
		},
	} {
		got := render(Within(w, c.rates))
		if len(got) != len(c.want) {
			t.Errorf("%s: got = %v, want = %v", c.source, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: got = %v, want = %v", c.source, got, c.want)
				break
			}
		}
	}
	// source: spec/services/billing/segments_spec.rb:100
	ss := Within(w, []*Rate{named("v1", utc(2026, 9, 15)), named("v2", utc(2026, 9, 20)), named("v3", utc(2026, 9, 30))})
	if !ss[0].From.Equal(w.From) || !ss[len(ss)-1].To.Equal(w.To) {
		t.Errorf("cover: got = %v, want = the whole window", ss)
	}
	for i := 1; i < len(ss); i++ {
		if !ss[i-1].To.Equal(ss[i].From) {
			t.Errorf("cover: got = gap at %d, want = contiguous", i)
		}
	}
}
