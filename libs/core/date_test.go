package core_test

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// TestDateMidnight checks that a date whose local midnight is skipped by
// daylight saving time starts at the end of the gap: America/Santiago moves
// from 00:00 -04 to 01:00 -03 on 2026-09-06, so the day begins at 04:00 UTC,
// as ActiveSupport's beginning_of_day resolves it.
//
// source: app/services/utils/datetime.rb:70 (beginning_of_day)
func TestDateMidnight(t *testing.T) {
	loc, err := time.LoadLocation("America/Santiago")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		date core.Date
		want time.Time
	}{
		{core.NewDate(2026, time.September, 6), time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC)},
		{core.NewDate(2026, time.September, 7), time.Date(2026, 9, 7, 3, 0, 0, 0, time.UTC)},
		{core.NewDate(2026, time.September, 5), time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		if got := tt.date.Midnight(loc); !got.Equal(tt.want) {
			t.Errorf("%v.Midnight() = %v, want = %v", tt.date, got, tt.want)
		}
	}
}
