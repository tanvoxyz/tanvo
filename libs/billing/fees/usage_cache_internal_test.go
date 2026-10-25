package fees

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
)

func TestUsageCacheStamp(t *testing.T) {
	t.Parallel()
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	boundary := at("2026-07-27T10:00:00Z")
	tests := []struct {
		name        string
		watermark   time.Time
		now         time.Time
		wantStamp   time.Time
		wantSettled bool
	}{
		// source: spec/services/cache_service_spec.rb:152
		// source: spec/services/subscriptions/charge_cache_middleware_spec.rb:78
		{"stamped with the watermark, not the computation time", at("2026-07-27T10:00:00.123Z"), at("2026-07-27T10:05:00Z"), at("2026-07-27T10:00:00.123Z"), true},
		// source: spec/services/cache_service_spec.rb:168
		{"keeps sub-second precision", at("2026-07-27T10:00:00.75Z"), at("2026-07-27T11:00:00.75Z"), at("2026-07-27T10:00:00.75Z"), true},
		// source: spec/services/cache_service_spec.rb:183
		// source: spec/services/cache_service_spec.rb:272
		// source: spec/services/subscriptions/charge_cache_middleware_spec.rb:65
		{"no event: the settle bound", time.Time{}, at("2026-07-27T10:00:00.5Z"), at("2026-07-27T09:59:59.5Z"), true},
		// source: spec/services/cache_service_spec.rb:221
		{"unsettled boundary", boundary, boundary.Add(400 * time.Millisecond), boundary, false},
		// source: spec/services/cache_service_spec.rb:233
		{"at the edge of the window", boundary, boundary.Add(time.Second), boundary, false},
		// source: spec/services/cache_service_spec.rb:243
		{"older than the window", boundary, boundary.Add(2 * time.Second), boundary, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stamp, settled := usageCacheStamp(tt.watermark, tt.now)
			if !stamp.Equal(tt.wantStamp) || settled != tt.wantSettled {
				t.Errorf("got = %v %v, want = %v %v", stamp, settled, tt.wantStamp, tt.wantSettled)
			}
		})
	}
}

func TestUsageCacheValid(t *testing.T) {
	t.Parallel()
	last := time.Date(2026, 7, 27, 10, 0, 0, 123_000_000, time.UTC)
	period := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		cachedAt  time.Time
		watermark time.Time
		want      bool
	}{
		// source: spec/services/cache_service_spec.rb:384
		{"no newer event", last, last.Add(-time.Hour), true},
		// source: spec/services/cache_service_spec.rb:343
		// source: spec/services/cache_service_spec.rb:353
		{"the same last event", last, last, true},
		// source: spec/services/cache_service_spec.rb:365
		{"a recurring charge seeded with the period start", period, period, true},
		// source: spec/services/cache_service_spec.rb:402
		{"no event read", last, time.Time{}, true},
		// source: spec/services/cache_service_spec.rb:394
		// source: spec/services/cache_service_spec.rb:314
		{"an event ingested since", last, last.Add(2 * time.Second), false},
		// source: spec/services/cache_service_spec.rb:330
		{"an event floored to an earlier second than the computation", time.Date(2026, 7, 27, 5, 30, 0, 0, time.UTC), time.Date(2026, 7, 27, 5, 31, 15, 0, time.UTC), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := usageCacheValid(kernel.UsageCacheEntry{CachedAt: tt.cachedAt}, tt.watermark); got != tt.want {
				t.Errorf("got = %v, want = %v", got, tt.want)
			}
		})
	}
}
