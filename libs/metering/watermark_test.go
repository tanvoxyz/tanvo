package metering_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/metering"
)

// noWatermarks is a store that reports no ingestion watermark.
type noWatermarks struct{ metering.Store }

// notImplemented is a store routing to one without watermarks.
type notImplemented struct{ *metering.MemoryStore }

func (notImplemented) LastIngestedAt(context.Context, metering.Query) (time.Time, error) {
	return time.Time{}, metering.ErrNotImplemented
}

// withBuckets is a store with usage buckets.
type withBuckets struct{ *metering.MemoryStore }

func (withBuckets) ReadBuckets(context.Context, metering.BucketRead) ([]metering.BucketTotal, bool, error) {
	return nil, false, nil
}

func TestWatermark(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 31, 23, 59, 59, 0, time.UTC)
	ingested := time.Date(2026, 3, 5, 10, 0, 0, 123_000, time.UTC)
	ev := func(ts, at time.Time) metering.Event {
		return metering.Event{
			OrganizationID: "org", SubscriptionID: "sub", Code: "calls", TransactionID: ts.String(),
			Timestamp: ts, IngestedAt: at, Properties: metering.Properties{},
		}
	}
	mem := metering.NewMemoryStore([]metering.Event{
		ev(from.AddDate(0, 0, 2), ingested),
		ev(from.AddDate(0, -1, 2), ingested.Add(time.Hour)),
	})
	req := func(fn ...func(*metering.Request)) metering.Request {
		r := metering.Request{
			OrganizationID: "org", SubscriptionID: "sub", From: from, To: to,
			Metric: metering.Metric{Code: "calls", Aggregation: metering.AggregationSum, FieldName: "value"},
		}
		for _, f := range fn {
			f(&r)
		}
		return r
	}
	tests := []struct {
		name   string
		store  metering.Store
		req    metering.Request
		want   time.Time
		wantOK bool
	}{
		{"the latest ingestion in the period", mem, req(), ingested, true},
		{"no event", mem, req(func(r *metering.Request) { r.Metric.Code = "other" }), time.Time{}, true},
		{"a recurring metric reads every period", mem, req(func(r *metering.Request) { r.Metric.Recurring = true }), ingested.Add(time.Hour), true},
		{"a recurring metric without event: the period start", mem, req(func(r *metering.Request) {
			r.Metric.Recurring = true
			r.Metric.Code = "other"
		}), from, true},
		{"capped by an instant", mem, req(func(r *metering.Request) { r.MaxTimestamp = to }), time.Time{}, false},
		{"bound to an event", mem, req(func(r *metering.Request) { r.Event = &metering.Event{} }), time.Time{}, false},
		{"a store without watermarks", noWatermarks{mem}, req(), time.Time{}, false},
		{"a routed store without watermarks", notImplemented{mem}, req(), time.Time{}, false},
		// source: spec/services/subscriptions/charge_cache_middleware_spec.rb:41
		{"answered from the usage buckets", withBuckets{mem}, req(func(r *metering.Request) { r.UsageBuckets = true }), time.Time{}, false},
		{"usage buckets not asked for", withBuckets{mem}, req(), ingested, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok, err := metering.Watermark(t.Context(), tt.store, tt.req)
			if err != nil || ok != tt.wantOK || !got.Equal(tt.want) {
				t.Errorf("got = %v %v %v, want = %v %v", got, ok, err, tt.want, tt.wantOK)
			}
		})
	}
}
