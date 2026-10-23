package period

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestDateHelpers(t *testing.T) {
	d := func(y int, m time.Month, day int) date { return date{y, m, day} }
	tests := []struct {
		name      string
		got, want date
	}{
		{"add a month clamps the day", d(2024, 1, 31).addMonths(1), d(2024, 2, 29)},
		{"add a month in a non leap year", d(2023, 1, 31).addMonths(1), d(2023, 2, 28)},
		{"add months across a year", d(2023, 11, 30).addMonths(3), d(2024, 2, 29)},
		{"subtract months", d(2024, 3, 31).addMonths(-1), d(2024, 2, 29)},
		{"add days across a month", d(2024, 2, 28).addDays(2), d(2024, 3, 1)},
		{"beginning of week of a Sunday", d(2024, 3, 10).beginningOfWeek(), d(2024, 3, 4)},
		{"beginning of week of a Monday", d(2024, 3, 4).beginningOfWeek(), d(2024, 3, 4)},
		{"end of week", d(2024, 3, 4).endOfWeek(), d(2024, 3, 10)},
		{"beginning of quarter", d(2024, 8, 15).beginningOfMonths(3), d(2024, 7, 1)},
		{"end of quarter", d(2024, 8, 15).endOfMonths(3), d(2024, 9, 30)},
		{"beginning of half year", d(2024, 6, 30).beginningOfMonths(6), d(2024, 1, 1)},
		{"end of half year", d(2024, 7, 1).endOfMonths(6), d(2024, 12, 31)},
		{"end of year", d(2024, 2, 1).endOfMonths(12), d(2024, 12, 31)},
		{"previous Tuesday of a Tuesday", d(2024, 3, 5).prevOccurring(time.Tuesday), d(2024, 2, 27)},
		{"previous Sunday of a Tuesday", d(2024, 3, 5).prevOccurring(time.Sunday), d(2024, 3, 3)},
		{"next Tuesday of a Tuesday", d(2024, 3, 5).nextOccurring(time.Tuesday), d(2024, 3, 12)},
		{"next Monday of a Tuesday", d(2024, 3, 5).nextOccurring(time.Monday), d(2024, 3, 11)},
		{"build date clamps the day", buildDate(2023, 2, 31), d(2023, 2, 28)},
		{"build date of day 0", buildDate(2024, 3, 0), d(2024, 2, 29)},
		{"build date of day 0 in January", buildDate(2024, 1, 0), d(2023, 12, 31)},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s: got = %v, want = %v", tt.name, tt.got, tt.want)
		}
	}
	if got, want := daysIn(2024, 2), 29; got != want {
		t.Errorf("daysIn(2024, 2): got = %d, want = %d", got, want)
	}
	if got, want := d(2024, 3, 1).sub(d(2024, 2, 1)), 29; got != want {
		t.Errorf("sub: got = %d, want = %d", got, want)
	}
}

func TestDateBoundsDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// 2024-03-10 is 23 hours long in New York.
	day := date{2024, 3, 10}
	if got, want := day.start(ny), time.Date(2024, 3, 10, 5, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("start: got = %s, want = %s", got, want)
	}
	if got, want := day.end(ny), time.Date(2024, 3, 11, 4, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("end: got = %s, want = %s", got, want)
	}
	if got, want := dateOf(time.Date(2024, 3, 11, 3, 59, 0, 0, time.UTC).In(ny)), day; got != want {
		t.Errorf("dateOf: got = %v, want = %v", got, want)
	}
}

func TestInterval(t *testing.T) {
	for _, i := range []Interval{Weekly, Monthly, Quarterly, Semiannual, Yearly} {
		b, err := json.Marshal(i)
		if err != nil {
			t.Fatal(err)
		}
		var got Interval
		if err := json.Unmarshal(b, &got); err != nil || got != i {
			t.Errorf("round trip %s: got = %v (%v), want = %v", b, got, err, i)
		}
	}
	if _, err := ParseInterval("daily"); err == nil {
		t.Errorf("ParseInterval(daily): got = nil, want = error")
	}
	var i Interval
	if err := i.UnmarshalText([]byte("daily")); err == nil {
		t.Errorf("UnmarshalText(daily): got = nil, want = error")
	}
	if _, err := Interval(0).MarshalText(); err == nil {
		t.Errorf("MarshalText(0): got = nil, want = error")
	}
	if got, want := Interval(9).String(), "Interval(9)"; got != want {
		t.Errorf("String: got = %s, want = %s", got, want)
	}
	if got := Interval(9).PeriodsPerYear(); got != 0 {
		t.Errorf("PeriodsPerYear: got = %d, want = 0", got)
	}
}

func TestBillingTime(t *testing.T) {
	for _, b := range []BillingTime{Calendar, Anniversary} {
		text, err := b.MarshalText()
		if err != nil {
			t.Fatal(err)
		}
		var got BillingTime
		if err := got.UnmarshalText(text); err != nil || got != b {
			t.Errorf("round trip %s: got = %v (%v), want = %v", text, got, err, b)
		}
	}
	if _, err := ParseBillingTime("weekly"); err == nil {
		t.Errorf("ParseBillingTime(weekly): got = nil, want = error")
	}
	var b BillingTime
	if err := b.UnmarshalText([]byte("weekly")); err == nil {
		t.Errorf("UnmarshalText(weekly): got = nil, want = error")
	}
	if _, err := BillingTime(0).MarshalText(); err == nil {
		t.Errorf("MarshalText(0): got = nil, want = error")
	}
	if got, want := BillingTime(0).String(), "BillingTime(0)"; got != want {
		t.Errorf("String: got = %s, want = %s", got, want)
	}
	if got, want := Change(7).String(), "Change(7)"; got != want {
		t.Errorf("Change.String: got = %s, want = %s", got, want)
	}
	if got, want := NoChange.String(), "none"; got != want {
		t.Errorf("Change.String: got = %s, want = %s", got, want)
	}
}

func TestValidate(t *testing.T) {
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	valid := Subscription{Plan: Plan{Interval: Monthly}, BillingTime: Calendar, SubscriptionAt: at}
	tests := []struct {
		name   string
		modify func(*Subscription)
		want   bool
	}{
		{"valid", func(*Subscription) {}, true},
		{"unknown interval", func(s *Subscription) { s.Plan.Interval = 0 }, false},
		{"unknown billing time", func(s *Subscription) { s.BillingTime = 0 }, false},
		{"missing subscription date", func(s *Subscription) { s.SubscriptionAt = time.Time{} }, false},
		{"negative trial", func(s *Subscription) { s.Plan.TrialDays = -1 }, false},
	}
	for _, tt := range tests {
		s := valid
		tt.modify(&s)
		err := s.Validate()
		if got := err == nil; got != tt.want {
			t.Errorf("%s: got = %v, want = valid %v", tt.name, err, tt.want)
		}
		if err != nil && !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got = %v, want = ErrInvalid", tt.name, err)
		}
		if _, dErr := s.Dates(at, Billing); (dErr == nil) != tt.want {
			t.Errorf("%s: Dates error: got = %v, want = valid %v", tt.name, dErr, tt.want)
		}
	}
}

// A day whose local midnight is skipped by a daylight saving transition
// begins at the end of the gap, on that day, and the day before ends there.
//
// source: activesupport/lib/active_support/values/time_zone.rb (period_for_local resolves a nonexistent time forward)
func TestDateStartInDSTGap(t *testing.T) {
	santiago, err := time.LoadLocation("America/Santiago")
	if err != nil {
		t.Skipf("tzdata: %v", err)
	}
	tests := []struct {
		name string
		got  time.Time
		want time.Time
	}{
		{"start of the gap day", date{2026, time.September, 6}.start(santiago), time.Date(2026, time.September, 6, 4, 0, 0, 0, time.UTC)},
		{"end of the day before", date{2026, time.September, 5}.end(santiago), time.Date(2026, time.September, 6, 4, 0, 0, 0, time.UTC)},
		{"start of an ordinary day", date{2026, time.September, 7}.start(santiago), time.Date(2026, time.September, 7, 3, 0, 0, 0, time.UTC)},
		{"start in UTC", date{2026, time.September, 6}.start(time.UTC), time.Date(2026, time.September, 6, 0, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		if !tt.got.Equal(tt.want) {
			t.Errorf("%s: got = %v, want = %v", tt.name, tt.got.UTC(), tt.want)
		}
	}
}

// StartOfDay and EndOfDay resolve a midnight skipped by DST forward, as
// ActiveSupport's beginning_of_day and end_of_day do.
func TestStartOfDay(t *testing.T) {
	santiago, err := time.LoadLocation("America/Santiago")
	if err != nil {
		t.Skipf("tzdata: %v", err)
	}
	noon6 := time.Date(2026, time.September, 6, 16, 0, 0, 0, time.UTC) // 13:00 -03
	noon5 := time.Date(2026, time.September, 5, 16, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		got  time.Time
		want time.Time
	}{
		{"start of the gap day", StartOfDay(noon6, santiago), time.Date(2026, time.September, 6, 4, 0, 0, 0, time.UTC)},
		{"end of the day before the gap", EndOfDay(noon5, santiago), time.Date(2026, time.September, 6, 4, 0, 0, 0, time.UTC)},
		{"start of the day before", StartOfDay(noon5, santiago), time.Date(2026, time.September, 5, 4, 0, 0, 0, time.UTC)},
		{"start in UTC", StartOfDay(noon6, time.UTC), time.Date(2026, time.September, 6, 0, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		if !tt.got.Equal(tt.want) {
			t.Errorf("%s: got = %v, want = %v", tt.name, tt.got.UTC(), tt.want)
		}
		if tt.got.Location() != time.UTC {
			t.Errorf("%s: location = %v, want = UTC", tt.name, tt.got.Location())
		}
	}
}
