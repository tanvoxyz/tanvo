package kafka

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
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
