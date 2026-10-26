package port

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Slots bounds how many holders of a name run at once across every
// process of the installation, as the integration sync jobs of an
// organization share a few concurrency slots. libs/store implements it on
// PostgreSQL and libs/cache on Redis or Valkey.
//
// A slot is a lease with an expiry ([DefaultSlotTTL] unless the
// implementation is configured otherwise): while it is held, a goroutine
// started by AcquireSlot renews it every third of its TTL, until release
// stops it and deletes the lease. A holder that crashes stops renewing,
// and its lease expires after the TTL; an expired lease counts as free.
// A renewal that fails is logged and retried at the next tick; the work
// holding the slot goes on.
//
// Each lease carries a random holder token: a holder whose lease expired
// and was taken by another never renews or deletes the other's lease.
type Slots interface {
	// AcquireSlot takes one of the limit slots named name and returns the
	// function that frees it, which is idempotent; ok is false, with
	// nothing held, when every slot is taken. An empty name or a limit
	// below one is [core.ErrInvalidValue].
	AcquireSlot(ctx context.Context, name string, limit int) (release func(), ok bool, err error)
}

// DefaultSlotTTL is the lease TTL of a slot of [Slots] by default.
const DefaultSlotTTL = 30 * time.Second

// slotReleaseTimeout bounds the deletion of a released lease.
const slotReleaseTimeout = 5 * time.Second

// HoldSlot keeps the lease of the slot name for an implementation of
// [Slots]: it calls renew every interval (a third of the lease TTL), each
// call bounded by interval, in a goroutine, and returns the release
// function that stops the goroutine and then calls drop, bounded by five
// seconds. release is idempotent. Neither renew nor drop sees the
// cancellation of ctx, only its values. Their errors are logged at warn
// level with log, unless log is nil because the implementation reports
// its failures itself; renewals go on until release.
func HoldSlot(ctx context.Context, name string, interval time.Duration, renew, drop func(context.Context) error, log *slog.Logger) (release func()) {
	base := context.WithoutCancel(ctx)
	bg, cancel := context.WithCancel(base)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-bg.Done():
				return
			case <-t.C:
			}
			rctx, rcancel := context.WithTimeout(bg, interval)
			err := renew(rctx)
			rcancel()
			if err != nil && bg.Err() == nil && log != nil {
				log.WarnContext(bg, "slots: renew lease", slog.String("slot", name), slog.String("error", err.Error()))
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
			dctx, dcancel := context.WithTimeout(base, slotReleaseTimeout)
			defer dcancel()
			if err := drop(dctx); err != nil && log != nil {
				log.WarnContext(dctx, "slots: release lease", slog.String("slot", name), slog.String("error", err.Error()))
			}
		})
	}
}
