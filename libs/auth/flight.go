package auth

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync/atomic"

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
}

// Call states of [flightGroup.do]: whether the caller's own function started,
// or the caller gave up before it did.
const (
	flightPending int32 = iota
	flightRunning
	flightAbandoned
)

// do returns the result of fn for key, flightRunning fn with ctx unless a call
// for key is already in flight, whose result it then shares. leader
// reports whether the result is the one of this caller's own fn.
//
// A caller whose ctx ends while it waits on another caller's computation
// returns ctx.Err() at once; one whose own fn is flightRunning waits for it, fn
// seeing the same ctx, so that fn never outlives the caller (it may use
// the caller's transaction). When the computation it shared fails with a
// context error while its own ctx is still alive, the caller's work was
// cut short by another caller's context, and it computes again. A panic in
// fn is raised again in the caller that ran it; the others get an error.
func (g *flightGroup[T]) do(ctx context.Context, key string, fn func(context.Context) (T, error)) (v T, leader bool, err error) {
	for {
		var state atomic.Int32
		ch := g.g.DoChan(key, func() (res any, err error) {
			if !state.CompareAndSwap(flightPending, flightRunning) {
				// The caller left before its turn came: the context error
				// makes the callers that joined compute again.
				return nil, ctx.Err()
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
			// This caller's fn is flightRunning: it sees ctx end and stops soon.
			r = <-ch
		}
		mine := state.Load() == flightRunning
		if pe, ok := errors.AsType[*flightPanic](r.Err); ok && mine {
			panic(pe.value)
		}
		if r.Err != nil {
			if !mine && ctx.Err() == nil && isFlightContextErr(r.Err) {
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
