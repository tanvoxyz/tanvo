package kernel_test

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
)

func TestMemoryUsageCache(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	entry := func(amount int64) kernel.UsageCacheEntry {
		return kernel.UsageCacheEntry{
			Fees:      []*invoice.Fee{{Amount: amount, GroupedBy: map[string]string{"k": "v"}}},
			CachedAt:  now.Add(-time.Hour),
			ExpiresAt: now.Add(time.Hour),
		}
	}

	t.Run("off when not positive", func(t *testing.T) {
		t.Parallel()
		if c := kernel.NewMemoryUsageCache(0); c != nil {
			t.Errorf("got = %v, want = nil", c)
		}
	})
	// source: spec/services/cache_service_spec.rb:30
	// source: spec/services/cache_service_spec.rb:48
	t.Run("get returns what was put", func(t *testing.T) {
		t.Parallel()
		c := kernel.NewMemoryUsageCache(4)
		c.Put("a", entry(10))
		e, ok := c.Get("a", now)
		if !ok || len(e.Fees) != 1 || e.Fees[0].Amount != 10 || !e.CachedAt.Equal(now.Add(-time.Hour)) {
			t.Fatalf("got = %+v %v, want = the entry", e, ok)
		}
		if _, ok := c.Get("b", now); ok {
			t.Errorf("missing key: got = hit, want = miss")
		}
	})
	t.Run("expired entries are dropped", func(t *testing.T) {
		t.Parallel()
		c := kernel.NewMemoryUsageCache(4)
		c.Put("a", entry(10))
		if _, ok := c.Get("a", now.Add(time.Hour)); ok {
			t.Errorf("got = hit at expiry, want = miss")
		}
		if c.Len() != 0 {
			t.Errorf("got = %d entries, want = 0", c.Len())
		}
	})
	t.Run("fees are copied in and out", func(t *testing.T) {
		t.Parallel()
		c := kernel.NewMemoryUsageCache(4)
		in := entry(10)
		c.Put("a", in)
		in.Fees[0].Amount = 99
		in.Fees[0].GroupedBy["k"] = "changed"
		out, _ := c.Get("a", now)
		out.Fees[0].Amount = 77
		again, _ := c.Get("a", now)
		if f := again.Fees[0]; f.Amount != 10 || f.GroupedBy["k"] != "v" {
			t.Errorf("got = %d %v, want = 10 map[k:v]", f.Amount, f.GroupedBy)
		}
	})
	t.Run("least recently used is evicted", func(t *testing.T) {
		t.Parallel()
		c := kernel.NewMemoryUsageCache(2)
		c.Put("a", entry(1))
		c.Put("b", entry(2))
		c.Get("a", now)
		c.Put("c", entry(3))
		if _, ok := c.Get("b", now); ok {
			t.Errorf("b: got = hit, want = evicted")
		}
		for _, k := range []string{"a", "c"} {
			if _, ok := c.Get(k, now); !ok {
				t.Errorf("%s: got = miss, want = hit", k)
			}
		}
		c.Put("a", entry(4))
		if e, _ := c.Get("a", now); c.Len() != 2 || e.Fees[0].Amount != 4 {
			t.Errorf("replace: got = %d entries, amount %d, want = 2 and 4", c.Len(), e.Fees[0].Amount)
		}
	})
}
