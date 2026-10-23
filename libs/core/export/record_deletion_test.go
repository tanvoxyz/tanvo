package export_test

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/export"
)

func TestRecordDeletionCutoff(t *testing.T) {
	for _, tc := range []struct{ now, want string }{
		{"2026-10-23T01:20:00Z", "2026-09-23T01:20:00Z"},
		{"2026-03-31T12:00:00Z", "2026-02-28T12:00:00Z"},
		{"2028-03-31T12:00:00Z", "2028-02-29T12:00:00Z"},
		{"2026-01-15T00:00:00Z", "2025-12-15T00:00:00Z"},
		{"2026-10-23T03:20:00+02:00", "2026-09-23T01:20:00Z"},
	} {
		now, _ := time.Parse(time.RFC3339, tc.now)
		want, _ := time.Parse(time.RFC3339, tc.want)
		if got := export.RecordDeletionCutoff(now); !got.Equal(want) {
			t.Errorf("RecordDeletionCutoff(%s) got = %s, want = %s", tc.now, got, want)
		}
	}
}
