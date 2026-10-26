package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// blockingTx counts its transactions; while block is set, each waits for
// release, or for its context to end.
type blockingTx struct {
	port.Transactor
	block   atomic.Bool
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (b *blockingTx) InTx(ctx context.Context, fn func(ctx context.Context, r port.Repos) error) error {
	if b.block.Load() {
		if b.calls.Add(1) == 1 {
			close(b.entered)
		}
		select {
		case <-b.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return b.Transactor.InTx(ctx, fn)
}

func newBlockingKeys(e *env) (*APIKeys, *blockingTx) {
	tx := &blockingTx{Transactor: e.store, entered: make(chan struct{}), release: make(chan struct{})}
	d := e.deps
	d.Tx = tx
	keys := NewAPIKeys(d, e.usage).WithCache(newTestCache(), DefaultAPIKeyCacheTTL)
	tx.block.Store(true)
	return keys, tx
}

func TestAPIKeysAuthenticateCachedConcurrentMisses(t *testing.T) {
	t.Run("one database read", func(t *testing.T) {
		e := newEnv(t)
		k := e.key(e.org())
		keys, tx := newBlockingKeys(e)
		const n = 6
		got := make([]*access.APIKey, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() {
				var err error
				if got[i], err = keys.AuthenticateCached(e.ctx, k.Secret); err != nil {
					t.Errorf("AuthenticateCached() error got = %v, want = nil", err)
				}
			})
		}
		<-tx.entered
		time.Sleep(50 * time.Millisecond) // the others join the read
		close(tx.release)
		wg.Wait()
		if c := tx.calls.Load(); c != 1 {
			t.Errorf("database reads got = %d, want = 1", c)
		}
		for i := range n {
			if got[i] == nil || got[i].ID != k.Key.ID {
				t.Fatalf("caller %d got = %v, want = key %s", i, got[i], k.Key.ID)
			}
		}
		// Every caller has a key of its own.
		name := "changed"
		got[0].Name = &name
		got[0].TokenHash[0] ^= 0xff
		for i := 1; i < n; i++ {
			if got[i] == got[0] || got[i].Name != nil || got[i].TokenHash[0] == got[0].TokenHash[0] {
				t.Errorf("caller %d after changing caller 0 got = %+v, want = unchanged", i, got[i])
			}
		}
	})

	t.Run("a cancelled leader makes the waiter read", func(t *testing.T) {
		e := newEnv(t)
		k := e.key(e.org())
		keys, tx := newBlockingKeys(e)
		leaderCtx, cancel := context.WithCancel(e.ctx)
		leaderErr := make(chan error, 1)
		go func() {
			_, err := keys.AuthenticateCached(leaderCtx, k.Secret)
			leaderErr <- err
		}()
		<-tx.entered
		done := make(chan struct{})
		var (
			got *access.APIKey
			err error
		)
		go func() {
			defer close(done)
			got, err = keys.AuthenticateCached(e.ctx, k.Secret)
		}()
		time.Sleep(50 * time.Millisecond) // the waiter joins the read
		// The waiter's own read goes through.
		tx.block.Store(false)
		cancel()
		if err := <-leaderErr; !errors.Is(err, context.Canceled) {
			t.Errorf("leader error got = %v, want = %v", err, context.Canceled)
		}
		<-done
		if err != nil || got == nil || got.ID != k.Key.ID {
			t.Errorf("waiter got = %v, %v, want = key %s, nil", got, err, k.Key.ID)
		}
	})

	t.Run("a waiter whose context ends returns", func(t *testing.T) {
		e := newEnv(t)
		k := e.key(e.org())
		keys, tx := newBlockingKeys(e)
		defer close(tx.release)
		go func() { _, _ = keys.AuthenticateCached(e.ctx, k.Secret) }()
		<-tx.entered
		ctx, cancel := context.WithTimeout(e.ctx, 20*time.Millisecond)
		defer cancel()
		if _, err := keys.AuthenticateCached(ctx, k.Secret); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("AuthenticateCached() error got = %v, want = %v", err, context.DeadlineExceeded)
		}
	})
}
