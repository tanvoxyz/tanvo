package cache_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/cache"
	"github.com/tanvoxyz/tanvo/libs/cache/cachetest"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// fakeClock is a clock that tests move by hand.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newClock() *fakeClock { return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func TestMemoryConformance(t *testing.T) {
	var clocks sync.Map // port.Cache → *fakeClock
	cachetest.Run(t, func(t *testing.T) port.Cache {
		clock := newClock()
		c := cache.NewMemory(1000, cache.WithClock(clock.Now))
		clocks.Store(port.Cache(c), clock)
		return c
	}, cachetest.WithAdvance(func(t *testing.T, c port.Cache, d time.Duration) {
		clock, ok := clocks.Load(c)
		if !ok {
			t.Fatal("advance: unknown cache")
		}
		clock.(*fakeClock).Advance(d)
	}))
}

func TestMemoryRealClock(t *testing.T) {
	cachetest.Run(t, func(t *testing.T) port.Cache {
		return cache.NewMemory(1000)
	}, cachetest.WithTTL(20*time.Millisecond))
}

func TestMemoryEvictsLeastRecentlyUsed(t *testing.T) {
	ctx := context.Background()
	m := cache.NewMemory(3)
	for _, k := range []string{"a", "b", "c"} {
		m.Set(ctx, k, []byte(k), time.Hour)
	}
	m.Get(ctx, "a")                          // b is now the least recently used
	m.Set(ctx, "c", []byte("c2"), time.Hour) // an overwrite evicts nothing
	m.Set(ctx, "d", []byte("d"), time.Hour)  // evicts b
	tests := []struct {
		key string
		ok  bool
	}{{"a", true}, {"b", false}, {"c", true}, {"d", true}}
	for _, tt := range tests {
		if _, ok := m.Get(ctx, tt.key); ok != tt.ok {
			t.Errorf("Get(%s) ok = %v, want = %v", tt.key, ok, tt.ok)
		}
	}
	if got := m.Len(); got != 3 {
		t.Errorf("Len() = %d, want = 3", got)
	}
}

func TestMemoryExpiredGetRemoves(t *testing.T) {
	ctx := context.Background()
	clock := newClock()
	m := cache.NewMemory(10, cache.WithClock(clock.Now))
	m.Set(ctx, "k", []byte("v"), time.Second)
	clock.Advance(999 * time.Millisecond)
	if _, ok := m.Get(ctx, "k"); !ok {
		t.Fatal("Get(k) before expiry ok = false, want = true")
	}
	clock.Advance(time.Millisecond)
	if _, ok := m.Get(ctx, "k"); ok {
		t.Fatal("Get(k) at expiry ok = true, want = false")
	}
	if got := m.Len(); got != 0 {
		t.Errorf("Len() = %d, want = 0", got)
	}
}

func TestMemoryOff(t *testing.T) {
	ctx := context.Background()
	for _, n := range []int{0, -1} {
		m := cache.NewMemory(n)
		if m != nil {
			t.Fatalf("NewMemory(%d) = %v, want = nil", n, m)
		}
		// A nil *Memory stores nothing.
		m.Set(ctx, "k", []byte("v"), time.Hour)
		if err := m.Delete(ctx, "k"); err != nil {
			t.Fatal(err)
		}
		if _, ok := m.Get(ctx, "k"); ok {
			t.Errorf("nil Memory Get(k) ok = true, want = false")
		}
		if got := m.Len(); got != 0 {
			t.Errorf("nil Memory Len() = %d, want = 0", got)
		}
	}
}
