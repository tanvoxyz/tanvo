package metering

import (
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// source: app/services/billable_metrics/aggregations/base_service.rb:223
// (where.not drops states without a transaction ID).
func TestMemoryCacheLatestInPeriodExclude(t *testing.T) {
	ts := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	one := money.One
	c := MemoryCache{
		{Timestamp: ts, TransactionID: "a", CurrentAggregation: &one},
		{Timestamp: ts, TransactionID: "b", CurrentAggregation: &one},
		{Timestamp: ts, CurrentAggregation: &one},
	}
	tests := []struct {
		name    string
		exclude string
		want    string
	}{
		{name: "no exclusion keeps the latest state", want: ""},
		{name: "exclusion drops states without a transaction", exclude: "z", want: "b"},
		{name: "exclusion drops the excluded transaction", exclude: "b", want: "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := c.LatestInPeriod(t.Context(), ts.Add(-time.Hour), ts.Add(time.Hour), nil, tt.exclude)
			if err != nil {
				t.Fatalf("LatestInPeriod() error = %v", err)
			}
			if got == nil {
				t.Fatalf("LatestInPeriod() got = nil, want = %q", tt.want)
			}
			if got.TransactionID != tt.want {
				t.Errorf("LatestInPeriod().TransactionID got = %q, want = %q", got.TransactionID, tt.want)
			}
		})
	}
}

func TestCachedValue(t *testing.T) {
	two := money.NewFromInt(2)
	if got, err := cachedValue(&two, "max_aggregation"); err != nil || !got.Equal(two) {
		t.Errorf("cachedValue(2) got = %v, %v, want = 2, nil", got, err)
	}
	if _, err := cachedValue(nil, "max_aggregation"); !errors.Is(err, ErrMissingCachedValue) {
		t.Errorf("cachedValue(nil) error got = %v, want = %v", err, ErrMissingCachedValue)
	}
}
