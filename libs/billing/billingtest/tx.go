package billingtest

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Tx wraps a transactor so that the [Emitter] records of a failed
// transaction are discarded too. It also checks that
// every transaction takes its locks in the order of [kernel.LockRank]: a
// lock taken out of order fails with an error.
type Tx struct {
	// Inner is the wrapped transactor.
	Inner port.Transactor
	// Emitter's records are rolled back with the transaction.
	Emitter *Emitter
}

type txKey struct{}

// InTx implements [port.Transactor].
func (t *Tx) InTx(ctx context.Context, fn func(ctx context.Context, r port.Repos) error) error {
	if st, ok := ctx.Value(txKey{}).(*lockOrder); ok {
		return t.Inner.InTx(ctx, st.wrap(fn))
	}
	n := t.Emitter.len()
	st := &lockOrder{}
	err := t.Inner.InTx(context.WithValue(ctx, txKey{}, st), st.wrap(fn))
	if err != nil {
		t.Emitter.truncate(n)
	}
	return err
}

// Emitter is a [port.WebhookEmitter] that records the events before
// passing them to the memstore.
type Emitter struct {
	// Inner is the memstore emitter.
	Inner port.WebhookEmitter

	mu     sync.Mutex
	events []webhook.Event
}

// Emit implements [port.WebhookEmitter].
func (e *Emitter) Emit(ctx context.Context, ev webhook.Event) error {
	if err := e.Inner.Emit(ctx, ev); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
	return nil
}

// Events returns the recorded events, oldest first.
func (e *Emitter) Events() []webhook.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.events)
}

// Reset forgets the recorded events.
func (e *Emitter) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = nil
}

func (e *Emitter) len() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.events)
}

func (e *Emitter) truncate(n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = e.events[:n]
}

// lockOrder records the locks one transaction holds and refuses one taken
// out of the order of [kernel.LockRank].
type lockOrder struct {
	mu   sync.Mutex
	held []port.LockKey
}

// wrap returns fn with repositories whose locker checks the order.
func (o *lockOrder) wrap(fn func(ctx context.Context, r port.Repos) error) func(ctx context.Context, r port.Repos) error {
	return func(ctx context.Context, r port.Repos) error {
		if _, ok := r.Locks.(orderedLocker); !ok {
			r.Locks = orderedLocker{inner: r.Locks, order: o}
		}
		return fn(ctx, r)
	}
}

// check records k, failing when the transaction holds a lock of higher
// rank and not k, or when it takes a second subscription lock neither
// under a billing_schedule lock nor in ascending ID order.
func (o *lockOrder) check(k port.LockKey) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if slices.Contains(o.held, k) {
		return nil
	}
	rank := kernel.LockRank(k)
	scheduled := false
	for _, h := range o.held {
		hr := kernel.LockRank(h)
		if rank != 0 && hr > rank {
			return fmt.Errorf("lock order: %s (rank %d) taken while holding %s (rank %d)", k, rank, h, hr)
		}
		scheduled = scheduled || hr == kernel.LockRank(port.CustomerLock(customer.LockScopeBillingSchedule, core.CustomerID{}))
	}
	if rank == kernel.LockRank(port.LockKey{Scope: "subscription"}) && !scheduled {
		for _, h := range o.held {
			if kernel.LockRank(h) == rank && h.ID.String() > k.ID.String() {
				return fmt.Errorf("lock order: %s taken while holding %s, outside a billing_schedule lock and not in ID order", k, h)
			}
		}
	}
	o.held = append(o.held, k)
	return nil
}

// orderedLocker is a [port.Locker] checking the lock order.
type orderedLocker struct {
	inner port.Locker
	order *lockOrder
}

// Lock implements [port.Locker].
func (l orderedLocker) Lock(ctx context.Context, org core.OrganizationID, k port.LockKey) error {
	if err := l.order.check(k); err != nil {
		return err
	}
	return l.inner.Lock(ctx, org, k)
}
