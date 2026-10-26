package integrations

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/billing/internal/cachetest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
)

func TestDraftTaxCacheConcurrentMisses(t *testing.T) {
	t.Parallel()
	integ := &integration.Integration{ID: core.MustParseID[core.IntegrationID](uuid.NewString()), UpdatedAt: time.Now()}

	t.Run("ask once", func(t *testing.T) {
		t.Parallel()
		c := NewDraftTaxCache(cachetest.New(nil), time.Hour, nil)
		var calls atomic.Int32
		entered, release := make(chan struct{}), make(chan struct{})
		const n = 6
		txs := make([]*integration.TaxTransaction, n)
		got := make([]*integration.TaxResult, n)
		for i := range n {
			// The callers differ only by their line identifiers: the same
			// key.
			txs[i] = cacheTransaction()
			txs[i].Lines[0].ItemKey = fmt.Sprintf("caller_%d", i)
		}
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() {
				tx := txs[i]
				res, err := c.quote(t.Context(), c.key(integ, tx), tx, func(context.Context) (*integration.TaxResult, error) {
					if calls.Add(1) == 1 {
						close(entered)
					}
					<-release
					return cacheAnswer(tx), nil
				})
				if err != nil {
					t.Errorf("quote() error got = %v, want = nil", err)
				}
				got[i] = res
			})
		}
		<-entered
		time.Sleep(50 * time.Millisecond) // the others join the request
		close(release)
		wg.Wait()
		if n := calls.Load(); n != 1 {
			t.Errorf("provider requests got = %d, want = 1", n)
		}
		for i := range n {
			if got[i] == nil || len(got[i].Lines) != 2 {
				t.Fatalf("caller %d got = %+v, want = two lines", i, got[i])
			}
			if k, want := got[i].Lines[0].ItemKey, fmt.Sprintf("caller_%d", i); k != want {
				t.Errorf("caller %d item key got = %q, want = %q", i, k, want)
			}
		}
		// Every caller has an answer of its own.
		got[0].Lines[1].ItemCode = "changed"
		for i := 1; i < n; i++ {
			if code := got[i].Lines[1].ItemCode; code != "m2" {
				t.Errorf("caller %d after changing caller 0 got = %q, want = m2", i, code)
			}
		}
	})

	t.Run("a cancelled leader makes the waiter ask", func(t *testing.T) {
		t.Parallel()
		c := NewDraftTaxCache(cachetest.New(nil), time.Hour, nil)
		tx := cacheTransaction()
		key := c.key(integ, tx)
		var calls atomic.Int32
		leaderCtx, cancel := context.WithCancel(t.Context())
		entered := make(chan struct{})
		leaderErr := make(chan error, 1)
		go func() {
			_, err := c.quote(leaderCtx, key, tx, func(ctx context.Context) (*integration.TaxResult, error) {
				calls.Add(1)
				close(entered)
				<-ctx.Done()
				return nil, fmt.Errorf("anrok: %w", ctx.Err())
			})
			leaderErr <- err
		}()
		<-entered
		done := make(chan struct{})
		var (
			res *integration.TaxResult
			err error
		)
		go func() {
			defer close(done)
			res, err = c.quote(t.Context(), key, tx, func(context.Context) (*integration.TaxResult, error) {
				calls.Add(1)
				return cacheAnswer(tx), nil
			})
		}()
		time.Sleep(50 * time.Millisecond) // the waiter joins the request
		cancel()
		if err := <-leaderErr; !errors.Is(err, context.Canceled) {
			t.Errorf("leader error got = %v, want = %v", err, context.Canceled)
		}
		<-done
		if err != nil || res == nil || len(res.Lines) != 2 {
			t.Errorf("waiter got = %+v, %v, want = an answer of two lines, nil", res, err)
		}
		if n := calls.Load(); n != 2 {
			t.Errorf("provider requests got = %d, want = 2", n)
		}
	})
}
