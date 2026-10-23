package jobs

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func TestBackoffs(t *testing.T) {
	t.Parallel()
	for attempt := 1; attempt <= 6; attempt++ {
		base := time.Duration(attempt*attempt*attempt*attempt) * time.Second
		for range 50 {
			if got := Polynomial(0.15)(attempt); got < base+2*time.Second || got > base+2*time.Second+base*15/100 {
				t.Fatalf("Polynomial(0.15)(%d) = %v, want in [%v, %v]", attempt, got, base+2*time.Second, base+2*time.Second+base*15/100)
			}
			if got := RandomDelay(LockRetryMaxDelay)(attempt); got < 0 || got >= LockRetryMaxDelay || got%time.Second != 0 {
				t.Fatalf("RandomDelay(16s)(%d) = %v, want whole seconds in [0, 16s)", attempt, got)
			}
			got := LinearDelay(5*time.Second, 12*time.Second)(attempt)
			lo := min(5*time.Second*time.Duration(attempt), 12*time.Second)
			if got < lo || got >= lo+5*time.Second {
				t.Fatalf("LinearDelay(5s, 12s)(%d) = %v, want in [%v, %v)", attempt, got, lo, lo+5*time.Second)
			}
		}
	}
}

func TestKindRule(t *testing.T) {
	t.Parallel()
	errLock := errors.New("lock")
	k := Kind{Name: "x.y", Retry: []RetryRule{LockRetry(errLock), SequenceRetry(core.ErrConflict)}}
	tests := []struct {
		err      error
		ok       bool
		attempts int
	}{
		{fmt.Errorf("wrapped: %w", errLock), true, LockRetryAttempts},
		{core.ErrConflict, true, SequenceRetryAttempts},
		{fmt.Errorf("x: %w", ErrRetriable), true, RetriableAttempts},
		{errors.New("other"), false, 0},
	}
	for _, tt := range tests {
		r, ok := k.rule(tt.err)
		if ok != tt.ok || r.Attempts != tt.attempts {
			t.Errorf("rule(%v) = %d, %v, want = %d, %v", tt.err, r.Attempts, ok, tt.attempts, tt.ok)
		}
	}
	if got := k.maxRuleAttempts(); got != LockRetryAttempts {
		t.Errorf("maxRuleAttempts() = %d, want = %d", got, LockRetryAttempts)
	}
}

func TestRegister(t *testing.T) {
	t.Parallel()
	h := HandlerFunc[struct{}](nil)
	r := NewRegistry()
	if err := Register(r, Kind{Name: "a.b"}, h); err != nil {
		t.Fatal(err)
	}
	if err := Register(r, Kind{Name: "a.b"}, h); !errors.Is(err, core.ErrConflict) {
		t.Errorf("duplicate Register() = %v, want = %v", err, core.ErrConflict)
	}
	for _, k := range []Kind{{Name: ""}, {Name: "bad kind"}, {Name: "a.c", MaxAttempts: -1}, {Name: "a.d", Retry: []RetryRule{{Attempts: 1}}}} {
		if err := Register(r, k, h); !errors.Is(err, core.ErrInvalidValue) {
			t.Errorf("Register(%+v) = %v, want = %v", k, err, core.ErrInvalidValue)
		}
	}
	r.seal()
	if err := Register(r, Kind{Name: "a.e"}, h); !errors.Is(err, core.ErrNotAllowed) {
		t.Errorf("Register() after seal = %v, want = %v", err, core.ErrNotAllowed)
	}
}

func TestShardAndQueueNames(t *testing.T) {
	t.Parallel()
	if got := Shard(core.OrganizationID{}, 8); got != 0 {
		t.Errorf("Shard(zero) = %d, want = 0", got)
	}
	counts := make([]int, 4)
	for range 400 {
		o := core.NewID[core.OrganizationID](core.UUIDv7Generator{})
		s := Shard(o, 4)
		if s != Shard(o, 4) {
			t.Fatal("Shard is not stable")
		}
		counts[s]++
	}
	for i, n := range counts {
		if n < 50 {
			t.Errorf("shard %d got %d of 400 organizations", i, n)
		}
	}
	for _, tt := range []struct {
		q    port.Queue
		i    int
		name string
	}{{port.QueueBilling, 0, "billing"}, {port.QueueBilling, 3, "billing_3"}, {port.QueueDefault, 1, "default_1"}} {
		if got := RiverQueue(tt.q, tt.i); got != tt.name {
			t.Errorf("RiverQueue(%s, %d) = %s, want = %s", tt.q, tt.i, got, tt.name)
		}
		if got := queueOf(tt.name); got != tt.q {
			t.Errorf("queueOf(%s) = %s, want = %s", tt.name, got, tt.q)
		}
	}
}

func TestFairnessDelay(t *testing.T) {
	t.Parallel()
	f := newFairness()
	f.limit = 2
	c := QueueConfig{Rate: 2, Burst: 3}
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	a := core.NewID[core.OrganizationID](core.UUIDv7Generator{})
	b := core.NewID[core.OrganizationID](core.UUIDv7Generator{})
	want := []time.Duration{0, 0, 0, 500 * time.Millisecond, time.Second}
	for i, w := range want {
		if got := f.delay(port.QueueBilling, c, a, now); got != w {
			t.Errorf("delay #%d = %v, want = %v", i, got, w)
		}
	}
	if got := f.delay(port.QueueBilling, c, b, now); got != 0 {
		t.Errorf("other organization's delay = %v, want = 0", got)
	}
	if got := f.delay(port.QueueWebhooks, c, a, now); got != 0 {
		t.Errorf("other queue's delay = %v, want = 0", got)
	}
	// The bucket refills at the rate.
	if got := f.delay(port.QueueBilling, c, a, now.Add(10*time.Second)); got != 0 {
		t.Errorf("delay after refill = %v, want = 0", got)
	}
	if got := f.delay(port.QueueBilling, QueueConfig{}, a, now); got != 0 {
		t.Errorf("delay without rate = %v, want = 0", got)
	}
	if len(f.buckets) > 3 {
		t.Errorf("buckets = %d, want eviction to bound them", len(f.buckets))
	}
}
