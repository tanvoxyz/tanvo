package auth

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// flightGroup runs one computation per key at a time among concurrent callers.
// The zero value is ready to use; a flightGroup must not be copied after first
// use. It is safe for concurrent use.
//
// Every caller of one computation receives the same value of type T, so
// T should be immutable or something each caller copies, such as encoded
// bytes it decodes.
type flightGroup[T any] struct {
	g singleflight.Group
	// MaxWait bounds how long a caller waits on another caller's
	// computation before it computes on its own; 0 is [defaultFlightMaxWait].
	MaxWait time.Duration
}

// defaultFlightMaxWait is the [flightGroup.MaxWait] of a zero
// flightGroup. Callers wait inside their own transactions, holding a pooled connection and maybe
// locks that the shared computation needs; the bound turns such a cycle
// into a delay instead of a deadlock.
const defaultFlightMaxWait = time.Second

// errFlightAbandoned is the result of a call whose caller stopped waiting
// before its function started: the callers that joined it compute again.
var errFlightAbandoned = errors.New("auth: lookup abandoned")

// Call states of [flightGroup.do]: whether the caller's own function started,
// or the caller gave up before it did.
const (
	flightPending int32 = iota
	flightRunning
	flightAbandoned
)

// do returns the result of fn for key, running fn with ctx unless a call
// for key is already in flight, whose result it then shares. leader
// reports whether the result is the one of this caller's own fn.
//
// A caller whose ctx ends while it waits on another caller's computation
// returns ctx.Err() at once, and one that has waited
// [flightGroup.MaxWait] runs fn itself; one whose own fn is running waits
// for it, fn seeing the same ctx, so that fn never outlives the caller (it may use
// the caller's transaction). When the computation it shared fails with a
// context error while its own ctx is still alive, the caller's work was
// cut short by another caller's context, and it computes again. A panic in
// fn is raised again in the caller that ran it; the others get an error.
func (g *flightGroup[T]) do(ctx context.Context, key string, fn func(context.Context) (T, error)) (v T, leader bool, err error) {
	wait := time.NewTimer(cmp.Or(g.MaxWait, defaultFlightMaxWait))
	defer wait.Stop()
	for {
		var state atomic.Int32
		ch := g.g.DoChan(key, func() (res any, err error) {
			if !state.CompareAndSwap(flightPending, flightRunning) {
				// The caller left before its turn came.
				return nil, errFlightAbandoned
			}
			// singleflight crashes the process on a panic in a DoChan
			// function; it is handed to the caller instead.
			defer func() {
				if r := recover(); r != nil {
					err = &flightPanic{value: r, stack: debug.Stack()}
				}
			}()
			return fn(ctx)
		})
		var r singleflight.Result
		select {
		case r = <-ch:
		case <-ctx.Done():
			if state.CompareAndSwap(flightPending, flightAbandoned) {
				var zero T
				return zero, false, ctx.Err()
			}
			// This caller's fn is running: it sees ctx end and stops soon.
			r = <-ch
		case <-wait.C:
			if state.CompareAndSwap(flightPending, flightAbandoned) {
				v, err = fn(ctx)
				return v, true, err
			}
			// This caller's fn is running: its result is the one to wait for.
			r = <-ch
		}
		mine := state.Load() == flightRunning
		if pe, ok := errors.AsType[*flightPanic](r.Err); ok && mine {
			panic(pe.value)
		}
		if r.Err != nil {
			if !mine && ctx.Err() == nil && (isFlightContextErr(r.Err) || errors.Is(r.Err, errFlightAbandoned)) {
				continue
			}
			var zero T
			return zero, mine, r.Err
		}
		v, _ = r.Val.(T)
		return v, mine, nil
	}
}

func isFlightContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// flightPanic carries a panic of a computation to the callers sharing it.
type flightPanic struct {
	value any
	stack []byte
}

func (p *flightPanic) Error() string {
	return fmt.Sprintf("auth: lookup panicked: %v\n\n%s", p.value, p.stack)
}
