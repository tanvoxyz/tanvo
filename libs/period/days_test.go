package period

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// daysCase is a period and its number of days.
type daysCase struct {
	Name     string `yaml:"name"`
	Source   string `yaml:"source"`
	Timezone string `yaml:"timezone"`
	From     ts     `yaml:"from"`
	To       ts     `yaml:"to"`
	// Billed counts with [Subscription.BilledDays] instead of [DaysBetween].
	Billed       bool   `yaml:"billed"`
	TerminatedAt ts     `yaml:"terminated_at"`
	Next         string `yaml:"next"`
	Want         int    `yaml:"want"`
}

func TestDays(t *testing.T) {
	for _, c := range loadCases[daysCase](t, "days.yaml", nil) {
		t.Run(c.Name, func(t *testing.T) {
			t.Log("source:", c.Source)
			loc := loadLocation(t, c.Timezone)
			if !c.Billed {
				if got := DaysBetween(c.From.Time, c.To.Time, loc); got != c.Want {
					t.Errorf("DaysBetween: got = %d, want = %d", got, c.Want)
				}
				if got := (Period{c.From.Time, c.To.Time}).Days(loc); got != c.Want {
					t.Errorf("Period.Days: got = %d, want = %d", got, c.Want)
				}
				return
			}
			s := Subscription{TerminatedAt: c.TerminatedAt.Time, Location: loc}
			switch c.Next {
			case "upgrade":
				s.Next = Upgrade
			case "downgrade":
				s.Next = Downgrade
			}
			if got := s.BilledDays(c.From.Time, c.To.Time); got != c.Want {
				t.Errorf("BilledDays: got = %d, want = %d", got, c.Want)
			}
		})
	}
}

func TestProrationRatio(t *testing.T) {
	tests := []struct {
		days, total int
		want        string
	}{
		{days: 15, total: 30, want: "0.5"},
		{days: 31, total: 31, want: "1"},
		{days: 0, total: 31, want: "0"},
		{days: 10, total: 0, want: "0"},
		{days: 1, total: 3, want: "0.3333333333333333333333333333333333333333"},
	}
	for _, tt := range tests {
		got := ProrationRatio(tt.days, tt.total)
		want := money.MustParse(tt.want)
		if !got.Equal(want) {
			t.Errorf("ProrationRatio(%d, %d): got = %s, want = %s", tt.days, tt.total, got, want)
		}
	}
}

func TestPeriod(t *testing.T) {
	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	p := Period{From: from, To: to}
	tests := []struct {
		name string
		got  bool
		want bool
	}{
		{"zero is zero", Period{}.IsZero(), true},
		{"period is not zero", p.IsZero(), false},
		{"zero is empty", Period{}.Empty(), true},
		{"period is not empty", p.Empty(), false},
		{"contains its start", p.Contains(from), true},
		{"does not contain its end", p.Contains(to), false},
		{"contains its last instant", p.Contains(to.Add(-time.Nanosecond)), true},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s: got = %v, want = %v", tt.name, tt.got, tt.want)
		}
	}
	if got, want := p.String(), "[2024-01-01T00:00:00Z, 2024-02-01T00:00:00Z)"; got != want {
		t.Errorf("String: got = %s, want = %s", got, want)
	}
}
