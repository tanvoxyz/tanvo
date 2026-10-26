package kafka

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// rejectingStore stores the events of a write unless one of them is
// poison, which it rejects as the ClickHouse store rejects a value its
// column cannot hold; failAt fails the write of that number with an error
// that is not a rejection. Stored events are deduplicated by transaction
// ID, as the stores do.
type rejectingStore struct {
	mu     sync.Mutex
	poison map[string]bool
	failAt int
	writes int
	stored map[string]int
}

func (s *rejectingStore) Write(_ context.Context, rows []metering.Event) ([]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	if s.writes == s.failAt {
		return nil, errors.New("clickhouse: connection refused")
	}
	for _, r := range rows {
		if s.poison[r.TransactionID] {
			return nil, fmt.Errorf("eventstore: clickhouse: write: %w: value overflows Decimal256", core.ErrInvalidValue)
		}
	}
	var dups []int
	for i, r := range rows {
		if s.stored[r.TransactionID] > 0 {
			dups = append(dups, i)
		}
		s.stored[r.TransactionID]++
	}
	return dups, nil
}

func TestWriterIsolatesRejectedEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var recs []*kgo.Record
	var txs []string
	for i := range 9 {
		tx := fmt.Sprintf("e%d", i)
		txs = append(txs, tx)
		recs = append(recs, enrichedRecord(t, enriched(tx, false)))
	}
	recs = slices.Insert(recs, 4, &kgo.Record{Value: []byte("garbage")})
	store := &rejectingStore{poison: map[string]bool{"e2": true, "e7": true}, stored: map[string]int{}}
	signals := &sinkFake{}
	res := Writer(store, signals, nil)(ctx, recs)
	for i, r := range res {
		want, code := Done, ""
		switch i {
		case 4:
			want, code = Dead, ingest.ErrorDecode
		case 2, 8: // e2, and e7 after the garbage
			want, code = Dead, ingest.ErrorReject
		}
		if r.Disposition != want || r.Code != code {
			t.Errorf("result %d got = %v %q, want = %v %q", i, r.Disposition, r.Code, want, code)
		}
		if want == Dead && code == ingest.ErrorReject && !errors.Is(r.Err, core.ErrInvalidValue) {
			t.Errorf("result %d error got = %v, want = the rejection", i, r.Err)
		}
	}
	for _, tx := range txs {
		want := 1
		if store.poison[tx] {
			want = 0
		}
		if got := store.stored[tx]; got != want {
			t.Errorf("stored %s got = %d, want = %d", tx, got, want)
		}
	}
	if len(signals.signals) != 7 {
		t.Errorf("signals got = %d, want = 7 (the stored events)", len(signals.signals))
	}
}

func TestWriterRetriesAfterAFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var recs []*kgo.Record
	for i := range 8 {
		recs = append(recs, enrichedRecord(t, enriched(fmt.Sprintf("e%d", i), false)))
	}
	// Write 1 is rejected (e0), write 2 (e0..e3) too, write 3 (e0, e1)
	// fails: every record not stored yet is retried.
	store := &rejectingStore{poison: map[string]bool{"e0": true}, failAt: 3, stored: map[string]int{}}
	res := Writer(store, nil, nil)(ctx, recs)
	for i, r := range res {
		if r.Disposition != Retry || r.Code != ingest.ErrorWrite || errors.Is(r.Err, core.ErrInvalidValue) || r.IngestedAt.IsZero() {
			t.Errorf("result %d got = %+v, want = retry %s", i, r, ingest.ErrorWrite)
		}
	}
	if store.writes != 3 {
		t.Errorf("writes got = %d, want = 3 (no write after the failure)", store.writes)
	}
	if len(store.stored) != 0 {
		t.Errorf("stored got = %v, want = none", store.stored)
	}
}

func TestWriterRejectsBatchOfOne(t *testing.T) {
	t.Parallel()
	store := &rejectingStore{poison: map[string]bool{"p": true}, stored: map[string]int{}}
	res := Writer(store, nil, nil)(context.Background(), []*kgo.Record{enrichedRecord(t, enriched("p", false))})
	if res[0].Disposition != Dead || res[0].Code != ingest.ErrorReject {
		t.Errorf("result got = %+v, want = dead %s", res[0], ingest.ErrorReject)
	}
	if store.writes != 1 {
		t.Errorf("writes got = %d, want = 1", store.writes)
	}
}
