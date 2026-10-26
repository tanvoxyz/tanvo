// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package period

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func d(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestSplit(t *testing.T) {
	for _, tt := range []struct {
		name     string
		from, to string
		g        Granularity
		want     []string
	}{
		{name: "days", from: "2026-02-27", to: "2026-03-01", g: Daily, want: []string{"2026-02-27/2026-02-27", "2026-02-28/2026-02-28", "2026-03-01/2026-03-01"}},
		{name: "weeks clipped", from: "2026-10-07", to: "2026-10-20", g: Weekly, want: []string{"2026-10-07/2026-10-11", "2026-10-12/2026-10-18", "2026-10-19/2026-10-20"}},
		{name: "week starting sunday", from: "2026-10-11", to: "2026-10-12", g: Weekly, want: []string{"2026-10-11/2026-10-11", "2026-10-12/2026-10-12"}},
		{name: "months", from: "2026-01-01", to: "2026-03-31", g: Monthly, want: []string{"2026-01-01/2026-01-31", "2026-02-01/2026-02-28", "2026-03-01/2026-03-31"}},
		{name: "months clipped", from: "2025-12-15", to: "2026-01-10", g: Monthly, want: []string{"2025-12-15/2025-12-31", "2026-01-01/2026-01-10"}},
		{name: "one day", from: "2026-05-05", to: "2026-05-05", g: Monthly, want: []string{"2026-05-05/2026-05-05"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ps, err := Split(d(tt.from), d(tt.to), tt.g)
			if err != nil {
				t.Fatalf("Split() error = %v", err)
			}
			var got []string
			for _, p := range ps {
				got = append(got, p.Start.Format(time.DateOnly)+"/"+p.End.Format(time.DateOnly))
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Split() got = %v, want = %v", got, tt.want)
			}
		})
	}
}

func TestSplitErrors(t *testing.T) {
	for _, tt := range []struct {
		name     string
		from, to string
		g        Granularity
	}{
		{name: "reversed", from: "2026-02-01", to: "2026-01-01", g: Monthly},
		{name: "too many", from: "2020-01-01", to: "2026-01-01", g: Daily},
	} {
		if _, err := Split(d(tt.from), d(tt.to), tt.g); !errors.Is(err, ErrRange) {
			t.Errorf("%s: Split() error got = %v, want = %v", tt.name, err, ErrRange)
		}
	}
	if _, err := Split(d("2026-01-01"), d("2026-01-01").AddDate(0, 0, MaxPeriods-1), Daily); err != nil {
		t.Errorf("Split() of %d days error got = %v, want = nil", MaxPeriods, err)
	}
}

func TestDefaultFrom(t *testing.T) {
	for _, tt := range []struct {
		to   string
		g    Granularity
		want string
	}{
		{to: "2026-10-27", g: Monthly, want: "2025-11-01"},
		{to: "2026-10-27", g: Weekly, want: "2026-08-10"},
		{to: "2026-10-27", g: Daily, want: "2026-09-28"},
	} {
		if got := DefaultFrom(d(tt.to), tt.g).Format(time.DateOnly); got != tt.want {
			t.Errorf("DefaultFrom(%s, %s) got = %s, want = %s", tt.to, tt.g, got, tt.want)
		}
		ps, _ := Split(DefaultFrom(d(tt.to), tt.g), d(tt.to), tt.g)
		want := DefaultPeriods
		if tt.g == Daily {
			want = DefaultDailyPeriods
		}
		if len(ps) != want {
			t.Errorf("DefaultFrom(%s, %s) periods got = %d, want = %d", tt.to, tt.g, len(ps), want)
		}
	}
}

func TestMonths(t *testing.T) {
	for _, tt := range []struct {
		from, to               string
		whole, days, monthDays int
	}{
		{from: "2026-01-01", to: "2026-02-01", whole: 1, days: 0, monthDays: 28},
		{from: "2026-01-01", to: "2027-01-01", whole: 12, days: 0, monthDays: 31},
		{from: "2026-01-15", to: "2026-03-01", whole: 1, days: 14, monthDays: 28},
		{from: "2026-01-31", to: "2026-02-28", whole: 1, days: 0, monthDays: 31},
		{from: "2026-01-31", to: "2026-03-31", whole: 2, days: 0, monthDays: 30},
		{from: "2026-03-01", to: "2026-03-08", whole: 0, days: 7, monthDays: 31},
		{from: "2026-03-01", to: "2026-03-01", whole: 0, days: 0, monthDays: 0},
		{from: "2028-02-29", to: "2029-02-28", whole: 12, days: 0, monthDays: 29},
	} {
		w, dd, md := Months(d(tt.from), d(tt.to))
		if w != tt.whole || dd != tt.days || md != tt.monthDays {
			t.Errorf("Months(%s, %s) got = %d %d/%d, want = %d %d/%d", tt.from, tt.to, w, dd, md, tt.whole, tt.days, tt.monthDays)
		}
	}
}
