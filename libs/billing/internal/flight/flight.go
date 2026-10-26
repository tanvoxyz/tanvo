package flight

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync/atomic"

	"golang.org/x/sync/singleflight"
)

// Group runs one computation per key at a time among concurrent callers.
// The zero value is ready to use; a Group must not be copied after first
// use. It is safe for concurrent use.
//
// Every caller of one computation receives the same value of type T, so
// T should be immutable or something each caller copies, such as encoded
// bytes it decodes.
type Group[T any] struct {
	g singleflight.Group
}

// Call states of [Group.Do]: whether the caller's own function started,
// or the caller gave up before it did.
const (
	pending int32 = iota
	running
	abandoned
)

// Do returns the result of fn for key, running fn with ctx unless a call
// for key is already in flight, whose result it then shares. leader
// reports whether the result is the one of this caller's own fn.
//
// A caller whose ctx ends while it waits on another caller's computation
// returns ctx.Err() at once; one whose own fn is running waits for it, fn
// seeing the same ctx, so that fn never outlives the caller (it may use
// the caller's transaction). When the computation it shared fails with a
// context error while its own ctx is still alive, the caller's work was
// cut short by another caller's context, and it computes again. A panic in
// fn is raised again in the caller that ran it; the others get an error.
func (g *Group[T]) Do(ctx context.Context, key string, fn func(context.Context) (T, error)) (v T, leader bool, err error) {
	for {
		var state atomic.Int32
		ch := g.g.DoChan(key, func() (res any, err error) {
			if !state.CompareAndSwap(pending, running) {
				// The caller left before its turn came: the context error
				// makes the callers that joined compute again.
				return nil, ctx.Err()
			}
			// singleflight crashes the process on a panic in a DoChan
			// function; it is handed to the caller instead.
			defer func() {
				if r := recover(); r != nil {
					err = &panicError{value: r, stack: debug.Stack()}
				}
			}()
			return fn(ctx)
		})
		var r singleflight.Result
		select {
		case r = <-ch:
		case <-ctx.Done():
			if state.CompareAndSwap(pending, abandoned) {
				var zero T
				return zero, false, ctx.Err()
			}
			// This caller's fn is running: it sees ctx end and stops soon.
			r = <-ch
		}
		mine := state.Load() == running
		if pe, ok := errors.AsType[*panicError](r.Err); ok && mine {
			panic(pe.value)
		}
		if r.Err != nil {
			if !mine && ctx.Err() == nil && isContextErr(r.Err) {
				continue
			}
			var zero T
			return zero, mine, r.Err
		}
		v, _ = r.Val.(T)
		return v, mine, nil
	}
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// panicError carries a panic of a computation to the callers sharing it.
type panicError struct {
	value any
	stack []byte
}

func (p *panicError) Error() string {
	return fmt.Sprintf("flight: computation panicked: %v\n\n%s", p.value, p.stack)
}
