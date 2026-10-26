package analytics

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/internal/cachetest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/analytics"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestCacheConcurrentMisses(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.March, 10, 12, 0, 0, 0, time.UTC)
	ids := core.UUIDv7Generator{}
	org := core.NewID[core.OrganizationID](ids)
	scope := analytics.Scope{Now: now, Currency: money.MustCurrency("EUR")}

	t.Run("compute once", func(t *testing.T) {
		t.Parallel()
		c := NewCache(cachetest.New(func() time.Time { return now }), time.Hour)
		var calls atomic.Int32
		entered, release := make(chan struct{}), make(chan struct{})
		compute := func() ([]analytics.Usage, error) {
			if calls.Add(1) == 1 {
				close(entered)
			}
			<-release
			return []analytics.Usage{{MetricCode: "calls", Amount: 7}}, nil
		}
		const n = 6
		got := make([][]analytics.Usage, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() {
				rows, err := cached(t.Context(), c, "usage", org, scope, false, compute)
				if err != nil {
					t.Errorf("cached() error got = %v, want = nil", err)
				}
				got[i] = rows
			})
		}
		<-entered
		time.Sleep(50 * time.Millisecond) // the others join the computation
		close(release)
		wg.Wait()
		if n := calls.Load(); n != 1 {
			t.Errorf("computations got = %d, want = 1", n)
		}
		for i := range n {
			if len(got[i]) != 1 || got[i][0].Amount != 7 {
				t.Fatalf("caller %d rows got = %+v, want = one row of 7", i, got[i])
			}
		}
		// Every caller has rows of its own.
		got[0][0].Amount = 99
		for i := 1; i < n; i++ {
			if a := got[i][0].Amount; a != 7 {
				t.Errorf("caller %d after changing caller 0 got = %d, want = 7", i, a)
			}
		}
	})

	t.Run("a cancelled leader makes the waiter compute", func(t *testing.T) {
		t.Parallel()
		c := NewCache(cachetest.New(func() time.Time { return now }), time.Hour)
		var calls atomic.Int32
		leaderCtx, cancel := context.WithCancel(t.Context())
		entered := make(chan struct{})
		leaderErr := make(chan error, 1)
		go func() {
			_, err := cached(leaderCtx, c, "usage", org, scope, false, func() ([]analytics.Usage, error) {
				calls.Add(1)
				close(entered)
				<-leaderCtx.Done()
				return nil, leaderCtx.Err()
			})
			leaderErr <- err
		}()
		<-entered
		done := make(chan struct{})
		var (
			rows []analytics.Usage
			err  error
		)
		go func() {
			defer close(done)
			rows, err = cached(t.Context(), c, "usage", org, scope, false, func() ([]analytics.Usage, error) {
				calls.Add(1)
				return []analytics.Usage{{MetricCode: "calls", Amount: 3}}, nil
			})
		}()
		time.Sleep(50 * time.Millisecond) // the waiter joins the computation
		cancel()
		if err := <-leaderErr; !errors.Is(err, context.Canceled) {
			t.Errorf("leader error got = %v, want = %v", err, context.Canceled)
		}
		<-done
		if err != nil || len(rows) != 1 || rows[0].Amount != 3 {
			t.Errorf("waiter got = %+v, %v, want = one row of 3, nil", rows, err)
		}
		if n := calls.Load(); n != 2 {
			t.Errorf("computations got = %d, want = 2", n)
		}
	})
}
