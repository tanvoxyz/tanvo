package kafka

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/tanvoxyz/tanvo/libs/ingest"
)

var now = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func rec(partition int32, offset int64) *kgo.Record {
	return &kgo.Record{Topic: "t", Partition: partition, Offset: offset, Key: fmt.Appendf(nil, "key%d", offset), Value: []byte(`{}`), Timestamp: now}
}

func offsetOf(r *kgo.Record) int64 {
	if r == nil {
		return -1
	}
	return r.Offset
}

// source: events-processor/config/kafka/consumer_test.go:18 (lago commits
// the highest record of the processed prefix; Tanvo also rewinds to the
// first unprocessed record instead of skipping it)
func TestDecide(t *testing.T) {
	t.Parallel()
	retry := Result{Disposition: Retry, Err: errors.New("x")}
	cases := []struct {
		name       string
		offsets    []int64
		dispo      []Result
		wantCommit int64
		wantRewind int64
		wantDead   int
	}{
		{"with continuous offsets", []int64{1, 2, 3, 4}, []Result{{}, {}, retry, retry}, 2, 3, 0},
		{"with non-continuous offsets", []int64{1, 3, 5, 7}, []Result{{}, retry, {}, retry}, 1, 3, 0},
		{"with empty processed records", []int64{1, 3, 5, 7}, []Result{retry, retry, retry, retry}, -1, 1, 0},
		{"all records processed", []int64{1, 2, 3, 4}, []Result{{}, {}, {}, {}}, 4, -1, 0},
		{"only one processed record - not first", []int64{1, 3, 5, 7}, []Result{retry, retry, {}, retry}, -1, 1, 0},
		{"only one processed record - first", []int64{1, 3, 5, 7}, []Result{{}, retry, retry, retry}, 1, 3, 0},
		{"dead records count as processed", []int64{1, 2, 3}, []Result{{}, {Disposition: Dead, Code: "x"}, {}}, 3, -1, 1},
		{"stale retries are dead", []int64{1, 2}, []Result{{Disposition: Retry, IngestedAt: now.Add(-13 * time.Hour)}, {}}, 2, -1, 1},
		{"recent retries by ingestion time", []int64{1, 2}, []Result{{Disposition: Retry, IngestedAt: now.Add(-11 * time.Hour)}, {}}, -1, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var recs []*kgo.Record
			for _, o := range tc.offsets {
				recs = append(recs, rec(0, o))
			}
			// Shuffle: decision must follow offsets, not batch order.
			recs[0], recs[len(recs)-1] = recs[len(recs)-1], recs[0]
			res := append([]Result(nil), tc.dispo...)
			res[0], res[len(res)-1] = res[len(res)-1], res[0]
			p := decide(recs, res, now, MaxRetryAge)
			k := partitionKey{"t", 0}
			if got := offsetOf(p.commit[k]); got != tc.wantCommit {
				t.Errorf("commit got = %d, want = %d", got, tc.wantCommit)
			}
			if got := offsetOf(p.rewind[k]); got != tc.wantRewind {
				t.Errorf("rewind got = %d, want = %d", got, tc.wantRewind)
			}
			if len(p.dead) != tc.wantDead {
				t.Errorf("dead got = %d, want = %d", len(p.dead), tc.wantDead)
			}
		})
	}
}

func TestDecidePartitions(t *testing.T) {
	t.Parallel()
	recs := []*kgo.Record{rec(0, 10), rec(1, 20), rec(0, 11), rec(1, 21)}
	res := []Result{{}, {Disposition: Retry}, {}, {}}
	p := decide(recs, res, now, MaxRetryAge)
	if got := offsetOf(p.commit[partitionKey{"t", 0}]); got != 11 {
		t.Errorf("partition 0 commit got = %d, want = 11", got)
	}
	if _, ok := p.commit[partitionKey{"t", 1}]; ok {
		t.Error("partition 1 commit got = set, want = none")
	}
	if got := offsetOf(p.rewind[partitionKey{"t", 1}]); got != 20 {
		t.Errorf("partition 1 rewind got = %d, want = 20", got)
	}
}

func TestDecideCounts(t *testing.T) {
	t.Parallel()
	recs := []*kgo.Record{rec(0, 1), rec(0, 2), rec(0, 3), rec(0, 4), rec(1, 1), rec(1, 2)}
	res := []Result{
		{},
		{Disposition: Dead, Code: ingest.ErrorReject},
		{Disposition: Retry, Code: ingest.ErrorWrite, IngestedAt: now.Add(-time.Hour)},
		{}, // after the retried record: redelivered
		{Disposition: Retry, Code: ingest.ErrorInAdvance, IngestedAt: now.Add(-13 * time.Hour)},
		{Disposition: Retry, Code: ingest.ErrorCatalog, IngestedAt: now.Add(-2 * time.Hour)},
	}
	p := decide(recs, res, now, MaxRetryAge)
	want := map[outcomeKey]int64{
		{outcome: outcomeDone}:               1,
		{outcomeDead, ingest.ErrorReject}:    1,
		{outcomeDead, ingest.ErrorInAdvance}: 1,
		{outcomeRetry, ingest.ErrorWrite}:    2,
		{outcomeRetry, ingest.ErrorCatalog}:  1,
	}
	if !maps.Equal(p.counts, want) {
		t.Errorf("counts got = %v, want = %v", p.counts, want)
	}
	if p.retryAge != 2*time.Hour {
		t.Errorf("retry age got = %v, want = 2h", p.retryAge)
	}
}

func TestMeters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	m := newMeters(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), "tanvo-write")
	m.count(ctx, map[outcomeKey]int64{
		{outcome: outcomeDone}:              3,
		{outcomeDead, ingest.ErrorReject}:   1,
		{outcomeRetry, "a code of its own"}: 2,
		{outcomeRetry, ""}:                  1,
	})
	m.retrying(ctx, 90*time.Second)
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	var age float64
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			switch d := mt.Data.(type) {
			case metricdata.Sum[int64]:
				for _, p := range d.DataPoints {
					group, _ := p.Attributes.Value(AttrGroup)
					outcome, _ := p.Attributes.Value(AttrOutcome)
					code, _ := p.Attributes.Value(AttrErrorType)
					if group.AsString() != "tanvo-write" {
						t.Errorf("group got = %q, want = tanvo-write", group.AsString())
					}
					got[outcome.AsString()+"/"+code.AsString()] = p.Value
				}
			case metricdata.Gauge[float64]:
				age = d.DataPoints[0].Value
			}
		}
	}
	want := map[string]int64{"done/": 3, "dead/" + ingest.ErrorReject: 1, "retry/other": 2, "retry/unknown": 1}
	if !maps.Equal(got, want) {
		t.Errorf("records got = %v, want = %v", got, want)
	}
	if age != 90 {
		t.Errorf("retry age got = %v, want = 90", age)
	}
}
