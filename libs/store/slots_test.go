package store_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/store"
)

// openSlots returns a store whose slot leases last ttl.
func openSlots(t *testing.T, ttl time.Duration) *store.Store {
	t.Helper()
	return store.New(open(t, true).Pool(), store.WithSlotTTL(ttl))
}

func TestAcquireSlot(t *testing.T) {
	s := openSlots(t, time.Minute)
	ctx := t.Context()
	var held []func()
	for i := range 2 {
		release, ok, err := s.AcquireSlot(ctx, "integrations", 2)
		if err != nil || !ok {
			t.Fatalf("slot %d: got = %v, %v, want = taken", i, ok, err)
		}
		held = append(held, release)
	}
	if _, ok, err := s.AcquireSlot(ctx, "integrations", 2); err != nil || ok {
		t.Fatalf("third slot: got = %v, %v, want = not taken", ok, err)
	}
	if release, ok, err := s.AcquireSlot(ctx, "other", 1); err != nil || !ok {
		t.Fatalf("other name: got = %v, %v, want = taken", ok, err)
	} else {
		release()
	}
	held[0]()
	held[0]() // idempotent
	release, ok, err := s.AcquireSlot(ctx, "integrations", 2)
	if err != nil || !ok {
		t.Fatalf("after release: got = %v, %v, want = taken", ok, err)
	}
	release()
	held[1]()
	var n int
	if err := s.Pool().QueryRow(ctx, "SELECT count(*) FROM concurrency_slots").Scan(&n); err != nil || n != 0 {
		t.Errorf("leases after release got = %d, %v, want = 0, nil", n, err)
	}
	for _, tt := range []struct {
		name  string
		limit int
	}{{"integrations", 0}, {"", 1}} {
		if _, _, err := s.AcquireSlot(ctx, tt.name, tt.limit); !errors.Is(err, core.ErrInvalidValue) {
			t.Errorf("AcquireSlot(%q, %d) error got = %v, want = %v", tt.name, tt.limit, err, core.ErrInvalidValue)
		}
	}
}

// TestAcquireSlotRace checks that concurrent acquirers take exactly the
// limit, and that holders that come and go never exceed it.
func TestAcquireSlotRace(t *testing.T) {
	s := openSlots(t, time.Minute)
	ctx := t.Context()
	const limit = 3
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		releases []func()
	)
	for range 30 {
		wg.Go(func() {
			release, ok, err := s.AcquireSlot(ctx, "race", limit)
			if err != nil {
				t.Errorf("AcquireSlot() error got = %v, want = nil", err)
			}
			if ok {
				mu.Lock()
				releases = append(releases, release)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if got := len(releases); got != limit {
		t.Errorf("slots taken at once got = %d, want = %d", got, limit)
	}
	for _, r := range releases {
		r()
	}

	var holding, peak, taken atomic.Int32
	for range 20 {
		wg.Go(func() {
			for range 5 {
				release, ok, err := s.AcquireSlot(ctx, "churn", limit)
				if err != nil {
					t.Errorf("AcquireSlot() error got = %v, want = nil", err)
					return
				}
				if !ok {
					continue
				}
				taken.Add(1)
				n := holding.Add(1)
				for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
				}
				time.Sleep(2 * time.Millisecond)
				holding.Add(-1)
				release()
			}
		})
	}
	wg.Wait()
	if got := peak.Load(); got > limit {
		t.Errorf("peak holders got = %d, want <= %d", got, limit)
	}
	if taken.Load() == 0 {
		t.Errorf("slots taken got = 0, want > 0")
	}
}

// TestAcquireSlotExpired checks that the expired lease of a crashed
// holder is free and overwritten.
func TestAcquireSlotExpired(t *testing.T) {
	s := openSlots(t, time.Minute)
	ctx := t.Context()
	if _, err := s.Pool().Exec(ctx, `INSERT INTO concurrency_slots (name, slot, holder, expires_at)
		VALUES ('crashed', 0, gen_random_uuid(), now() - interval '1 second')`); err != nil {
		t.Fatalf("insert lease error got = %v, want = nil", err)
	}
	release, ok, err := s.AcquireSlot(ctx, "crashed", 1)
	if err != nil || !ok {
		t.Fatalf("expired slot: got = %v, %v, want = taken", ok, err)
	}
	defer release()
	if _, ok, err := s.AcquireSlot(ctx, "crashed", 1); err != nil || ok {
		t.Errorf("slot retaken: got = %v, %v, want = not taken", ok, err)
	}
}

// TestAcquireSlotRenewal checks that a held lease outlives its TTL.
func TestAcquireSlotRenewal(t *testing.T) {
	s := openSlots(t, 300*time.Millisecond)
	ctx := t.Context()
	release, ok, err := s.AcquireSlot(ctx, "renewed", 1)
	if err != nil || !ok {
		t.Fatalf("slot: got = %v, %v, want = taken", ok, err)
	}
	time.Sleep(time.Second)
	if _, ok, err := s.AcquireSlot(ctx, "renewed", 1); err != nil || ok {
		t.Errorf("slot past its TTL: got = %v, %v, want = not taken", ok, err)
	}
	release()
	if r, ok, err := s.AcquireSlot(ctx, "renewed", 1); err != nil || !ok {
		t.Errorf("slot after release: got = %v, %v, want = taken", ok, err)
	} else {
		r()
	}
}

// TestAcquireSlotStaleHolder checks that a holder whose lease expired and
// was taken neither renews nor deletes the new holder's lease.
func TestAcquireSlotStaleHolder(t *testing.T) {
	s := openSlots(t, 300*time.Millisecond)
	ctx := t.Context()
	stale, ok, err := s.AcquireSlot(ctx, "stale", 1)
	if err != nil || !ok {
		t.Fatalf("first slot: got = %v, %v, want = taken", ok, err)
	}
	// The first holder stalls past its TTL.
	if _, err := s.Pool().Exec(ctx, "UPDATE concurrency_slots SET expires_at = now() - interval '1 second' WHERE name = 'stale'"); err != nil {
		t.Fatalf("expire lease error got = %v, want = nil", err)
	}
	release, ok, err := s.AcquireSlot(ctx, "stale", 1)
	if err != nil || !ok {
		t.Fatalf("expired slot: got = %v, %v, want = taken", ok, err)
	}
	defer release()
	var holder string
	if err := s.Pool().QueryRow(ctx, "SELECT holder::text FROM concurrency_slots WHERE name = 'stale'").Scan(&holder); err != nil {
		t.Fatalf("read holder error got = %v, want = nil", err)
	}
	// Past a few renewals of both holders and the stale one's release, the
	// lease is still the new holder's.
	time.Sleep(300 * time.Millisecond)
	stale()
	var got string
	if err := s.Pool().QueryRow(ctx, "SELECT holder::text FROM concurrency_slots WHERE name = 'stale'").Scan(&got); err != nil {
		t.Fatalf("read holder after stale release error got = %v, want = nil", err)
	}
	if got != holder {
		t.Errorf("holder got = %s, want = %s", got, holder)
	}
	if _, ok, err := s.AcquireSlot(ctx, "stale", 1); err != nil || ok {
		t.Errorf("slot after stale release: got = %v, %v, want = not taken", ok, err)
	}
}
