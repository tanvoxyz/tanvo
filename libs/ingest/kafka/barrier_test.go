package kafka

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
)

// fakeAdmin lists offsets from maps of partition to offset.
type fakeAdmin struct {
	after, starts, ends map[string]map[int32]int64
	committed           map[string]map[string]map[int32]int64
	err                 error
	millis              []int64
}

func listed(m map[string]map[int32]int64) kadm.ListedOffsets {
	out := kadm.ListedOffsets{}
	for topic, ps := range m {
		out[topic] = map[int32]kadm.ListedOffset{}
		for p, o := range ps {
			out[topic][p] = kadm.ListedOffset{Topic: topic, Partition: p, Offset: o}
		}
	}
	return out
}

func (f *fakeAdmin) ListOffsetsAfterMilli(_ context.Context, ms int64, _ ...string) (kadm.ListedOffsets, error) {
	f.millis = append(f.millis, ms)
	return listed(f.after), f.err
}

func (f *fakeAdmin) ListStartOffsets(context.Context, ...string) (kadm.ListedOffsets, error) {
	return listed(f.starts), nil
}

func (f *fakeAdmin) ListEndOffsets(context.Context, ...string) (kadm.ListedOffsets, error) {
	return listed(f.ends), nil
}

func (f *fakeAdmin) FetchOffsets(_ context.Context, group string) (kadm.OffsetResponses, error) {
	out := kadm.OffsetResponses{}
	for topic, ps := range f.committed[group] {
		out[topic] = map[int32]kadm.OffsetResponse{}
		for p, o := range ps {
			out[topic][p] = kadm.OffsetResponse{Offset: kadm.Offset{Topic: topic, Partition: p, At: o}}
		}
	}
	return out, nil
}

func TestBarrier(t *testing.T) {
	t.Parallel()
	topics := DefaultTopics()
	at := time.Date(2026, 11, 1, 0, 10, 0, 0, time.UTC)
	now := at.Add(time.Minute)
	f := &fakeAdmin{
		after:  map[string]map[int32]int64{topics.Raw: {0: 5, 1: 3, 2: 4}},
		starts: map[string]map[int32]int64{topics.Raw: {0: 0, 1: 0, 2: 4}, topics.Enriched: {0: 0, 1: 0}},
		ends:   map[string]map[int32]int64{topics.Enriched: {0: 10, 1: 7}},
		committed: map[string]map[string]map[int32]int64{
			// Partition 2 has no commit, but its records before the
			// instant are gone.
			"enrich": {topics.Raw: {0: 5, 1: 2}},
			"write":  {topics.Enriched: {0: 10, 1: 6}},
		},
	}
	b, err := NewBarrier(BarrierConfig{Admin: f, Topics: topics, EnrichGroup: "enrich", WriteGroup: "write", Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	reached := func(step string, want bool) {
		t.Helper()
		got, err := b.Reached(t.Context(), at)
		if err != nil || got != want {
			t.Fatalf("%s: Reached() got = %v, %v, want = %v, nil", step, got, err, want)
		}
	}

	now = at.Add(DefaultBarrierGrace - time.Second)
	reached("within the grace", false)
	if len(f.millis) != 0 {
		t.Errorf("offsets listed within the grace: %v", f.millis)
	}
	now = at.Add(time.Minute)
	reached("enrichment behind", false)
	if f.millis[0] != at.UnixMilli() {
		t.Errorf("ListOffsetsAfterMilli got = %d, want = %d", f.millis[0], at.UnixMilli())
	}
	f.committed["enrich"][topics.Raw][1] = 3
	reached("writer behind", false)
	// The enriched end offsets are those of the moment the enrichment
	// caught up: later records do not hold the barrier.
	f.ends[topics.Enriched] = map[int32]int64{0: 50, 1: 50}
	f.committed["write"][topics.Enriched][1] = 7
	reached("writer past the snapshot", true)
	if want := map[int32]int64{0: 10, 1: 7}; !maps.Equal(b.enriched[at], want) {
		t.Errorf("kept offsets got = %v, want = %v", b.enriched[at], want)
	}

	f.err = errors.New("boom")
	now = at.Add(2 * time.Hour)
	if _, err := b.Reached(t.Context(), at.Add(time.Hour)); err == nil {
		t.Error("Reached() with failing offsets got = nil error, want = an error")
	}
}

func TestNewBarrierInvalid(t *testing.T) {
	t.Parallel()
	if _, err := NewBarrier(BarrierConfig{Topics: DefaultTopics(), EnrichGroup: "e", WriteGroup: "w"}); err == nil {
		t.Error("NewBarrier() without an admin got = nil error, want = an error")
	}
}
