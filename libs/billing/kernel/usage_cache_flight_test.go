package kernel_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/internal/cachetest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
)

func TestUsageCacheCompute(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	watermark := now.Add(-time.Hour)
	entry := kernel.UsageCacheEntry{
		Fees:      []*invoice.Fee{{Amount: 10, GroupedBy: map[string]string{"k": "v"}}},
		CachedAt:  watermark,
		ExpiresAt: now.Add(time.Hour),
	}

	t.Run("concurrent misses compute once", func(t *testing.T) {
		t.Parallel()
		c := kernel.NewUsageCache(cachetest.New(func() time.Time { return now }))
		var calls atomic.Int32
		entered, release := make(chan struct{}), make(chan struct{})
		compute := func(context.Context) (kernel.UsageCacheEntry, bool, error) {
			if calls.Add(1) == 1 {
				close(entered)
			}
			<-release
			return kernel.UsageCacheEntry{
				Fees:      []*invoice.Fee{{Amount: 10, GroupedBy: map[string]string{"k": "v"}}},
				CachedAt:  watermark,
				ExpiresAt: now.Add(time.Hour),
			}, true, nil
		}
		const n = 6
		got := make([]kernel.UsageCacheEntry, n)
		shared := make([]bool, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() {
				var err error
				got[i], shared[i], err = c.Compute(t.Context(), "k", watermark, now, compute)
				if err != nil {
					t.Errorf("Compute() error got = %v, want = nil", err)
				}
			})
		}
		<-entered
		time.Sleep(50 * time.Millisecond) // the others join the call
		close(release)
		wg.Wait()
		if c := calls.Load(); c != 1 {
			t.Errorf("computations got = %d, want = 1", c)
		}
		leaders := 0
		for i := range n {
			if !shared[i] {
				leaders++
			}
			if len(got[i].Fees) != 1 || got[i].Fees[0].Amount != 10 {
				t.Fatalf("entry %d got = %+v, want = one fee of 10", i, got[i].Fees)
			}
		}
		if leaders != 1 {
			t.Errorf("unshared results got = %d, want = 1", leaders)
		}
		// Every caller has fees of its own.
		got[0].Fees[0].Amount = 99
		got[0].Fees[0].GroupedBy["k"] = "changed"
		for i := 1; i < n; i++ {
			if f := got[i].Fees[0]; f.Amount != 10 || f.GroupedBy["k"] != "v" {
				t.Errorf("entry %d after changing entry 0 got = %d %v, want = 10 map[k:v]", i, f.Amount, f.GroupedBy)
			}
		}
		// The entry was kept.
		if e, ok := c.Get(t.Context(), "k", now); !ok || e.Fees[0].Amount != 10 {
			t.Errorf("Get() got = %+v, %v, want = the computed entry", e, ok)
		}
	})

	t.Run("another watermark computes on its own", func(t *testing.T) {
		t.Parallel()
		c := kernel.NewUsageCache(cachetest.New(func() time.Time { return now }))
		var calls atomic.Int32
		entered, release := make(chan struct{}), make(chan struct{})
		go func() {
			_, _, _ = c.Compute(t.Context(), "k", watermark, now, func(context.Context) (kernel.UsageCacheEntry, bool, error) {
				calls.Add(1)
				close(entered)
				<-release
				return entry, false, nil
			})
		}()
		<-entered
		_, shared, err := c.Compute(t.Context(), "k", watermark.Add(time.Second), now, func(context.Context) (kernel.UsageCacheEntry, bool, error) {
			calls.Add(1)
			return entry, false, nil
		})
		close(release)
		if err != nil || shared {
			t.Errorf("Compute() got = %v, %v, want = false, nil", shared, err)
		}
		if c := calls.Load(); c != 2 {
			t.Errorf("computations got = %d, want = 2", c)
		}
	})

	t.Run("an unsettled entry is shared, not kept", func(t *testing.T) {
		t.Parallel()
		c := kernel.NewUsageCache(cachetest.New(func() time.Time { return now }))
		if _, _, err := c.Compute(t.Context(), "k", watermark, now, func(context.Context) (kernel.UsageCacheEntry, bool, error) {
			return entry, false, nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, ok := c.Get(t.Context(), "k", now); ok {
			t.Errorf("Get() got = hit, want = miss")
		}
	})

	t.Run("a cancelled leader makes the waiter compute", func(t *testing.T) {
		t.Parallel()
		c := kernel.NewUsageCache(cachetest.New(func() time.Time { return now }))
		var calls atomic.Int32
		leaderCtx, cancel := context.WithCancel(t.Context())
		entered := make(chan struct{})
		leaderErr := make(chan error, 1)
		go func() {
			_, _, err := c.Compute(leaderCtx, "k", watermark, now, func(ctx context.Context) (kernel.UsageCacheEntry, bool, error) {
				calls.Add(1)
				close(entered)
				<-ctx.Done()
				return kernel.UsageCacheEntry{}, false, ctx.Err()
			})
			leaderErr <- err
		}()
		<-entered
		done := make(chan struct{})
		var (
			got    kernel.UsageCacheEntry
			shared bool
			err    error
		)
		go func() {
			defer close(done)
			got, shared, err = c.Compute(t.Context(), "k", watermark, now, func(context.Context) (kernel.UsageCacheEntry, bool, error) {
				calls.Add(1)
				return entry, true, nil
			})
		}()
		time.Sleep(50 * time.Millisecond) // the waiter joins the call
		cancel()
		if err := <-leaderErr; !errors.Is(err, context.Canceled) {
			t.Errorf("leader Compute() error got = %v, want = %v", err, context.Canceled)
		}
		<-done
		if err != nil || shared || len(got.Fees) != 1 {
			t.Errorf("waiter Compute() got = %+v, %v, %v, want = its own entry, false, nil", got.Fees, shared, err)
		}
		if c := calls.Load(); c != 2 {
			t.Errorf("computations got = %d, want = 2", c)
		}
	})
}
