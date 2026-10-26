package flight_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/internal/flight"
)

// waitFor polls cond until it holds, failing the test after a while.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDoOnce(t *testing.T) {
	var g flight.Group[int]
	var calls atomic.Int32
	release := make(chan struct{})
	fn := func(context.Context) (int, error) {
		calls.Add(1)
		<-release
		return 42, nil
	}
	const n = 8
	var (
		wg      sync.WaitGroup
		leaders atomic.Int32
		started atomic.Int32
	)
	for range n {
		wg.Go(func() {
			started.Add(1)
			v, leader, err := g.Do(t.Context(), "k", fn)
			if err != nil || v != 42 {
				t.Errorf("Do() got = %d, %v, want = 42, nil", v, err)
			}
			if leader {
				leaders.Add(1)
			}
		})
	}
	waitFor(t, func() bool { return started.Load() == n && calls.Load() == 1 })
	// Let the stragglers join the call in flight.
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Errorf("calls got = %d, want = 1", got)
	}
	if got := leaders.Load(); got != 1 {
		t.Errorf("leaders got = %d, want = 1", got)
	}
}

func TestDoLeaderCancelled(t *testing.T) {
	var g flight.Group[string]
	var calls atomic.Int32
	leaderCtx, cancel := context.WithCancel(t.Context())
	inLeader := make(chan struct{})
	leaderDone := make(chan error, 1)
	go func() {
		_, _, err := g.Do(leaderCtx, "k", func(ctx context.Context) (string, error) {
			calls.Add(1)
			close(inLeader)
			<-ctx.Done()
			return "", ctx.Err()
		})
		leaderDone <- err
	}()
	<-inLeader
	waiterDone := make(chan struct{})
	var (
		got    string
		leader bool
		err    error
	)
	go func() {
		defer close(waiterDone)
		got, leader, err = g.Do(t.Context(), "k", func(context.Context) (string, error) {
			calls.Add(1)
			return "waiter", nil
		})
	}()
	// The waiter joins the leader's call before it is cancelled.
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Errorf("leader Do() error got = %v, want = %v", err, context.Canceled)
	}
	<-waiterDone
	if err != nil || got != "waiter" || !leader {
		t.Errorf("waiter Do() got = %q, %v, %v, want = %q, true, nil", got, leader, err, "waiter")
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("calls got = %d, want = 2", n)
	}
}

func TestDoWaiterCancelled(t *testing.T) {
	var g flight.Group[int]
	release := make(chan struct{})
	inLeader := make(chan struct{})
	leaderDone := make(chan int, 1)
	go func() {
		v, _, _ := g.Do(t.Context(), "k", func(context.Context) (int, error) {
			close(inLeader)
			<-release
			return 1, nil
		})
		leaderDone <- v
	}()
	<-inLeader
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := g.Do(ctx, "k", func(context.Context) (int, error) { return 2, nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("waiter Do() error got = %v, want = %v", err, context.DeadlineExceeded)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("waiter returned after %v, want = promptly", d)
	}
	close(release)
	if v := <-leaderDone; v != 1 {
		t.Errorf("leader Do() got = %d, want = 1", v)
	}
}

func TestDoError(t *testing.T) {
	var g flight.Group[int]
	boom := errors.New("boom")
	_, leader, err := g.Do(t.Context(), "k", func(context.Context) (int, error) { return 0, boom })
	if !errors.Is(err, boom) || !leader {
		t.Errorf("Do() got = %v, %v, want = true, %v", leader, err, boom)
	}
	// Errors are not kept: the next call computes again.
	v, _, err := g.Do(t.Context(), "k", func(context.Context) (int, error) { return 3, nil })
	if err != nil || v != 3 {
		t.Errorf("Do() got = %d, %v, want = 3, nil", v, err)
	}
}

func TestDoPanic(t *testing.T) {
	var g flight.Group[int]
	defer func() {
		if r := recover(); r != "boom" {
			t.Errorf("recover() got = %v, want = boom", r)
		}
	}()
	_, _, _ = g.Do(t.Context(), "k", func(context.Context) (int, error) { panic("boom") })
	t.Error("Do() returned, want = panic")
}
