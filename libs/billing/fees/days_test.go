package fees

import (
	"testing"
	"time"
)

// The local day helpers resolve a midnight skipped by DST forward
// (America/Santiago 2026-09-06 begins at 01:00 -03), as ActiveSupport does
// in lago's day counts.
//
// source: app/services/utils/datetime.rb:71
func TestDayHelpersInDSTGap(t *testing.T) {
	santiago, err := time.LoadLocation("America/Santiago")
	if err != nil {
		t.Skipf("tzdata: %v", err)
	}
	at := func(d, h int) time.Time { return time.Date(2026, time.September, d, h, 0, 0, 0, time.UTC) }
	t.Run("calendar days from the gap day", func(t *testing.T) {
		// A unit change at 12:00 local on the gap day, period ending
		// Sep 10 at local midnight: Sep 6, 7, 8 and 9.
		if got, want := calendarDays(at(6, 15), at(10, 3), santiago), 4; got != want {
			t.Errorf("days = %d, want = %d", got, want)
		}
	})
	t.Run("termination at the gap day's start bills that day", func(t *testing.T) {
		end := at(6, 4) // 01:00 -03, the first instant of Sep 6
		if got := billedTo(end, &end, santiago); !got.After(end) {
			t.Errorf("billed to = %v, want = after %v", got, end)
		}
	})
	t.Run("termination at 01:00 of an ordinary day keeps its end", func(t *testing.T) {
		end := at(7, 4) // 01:00 -03
		if got := billedTo(end, &end, santiago); !got.Equal(end) {
			t.Errorf("billed to = %v, want = %v", got, end)
		}
	})
}
