package port

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestHoldSlot checks that the lease is renewed until release, through
// failed renewals and the end of the acquiring context, and dropped once.
func TestHoldSlot(t *testing.T) {
	var renews, drops atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	renew := func(ctx context.Context) error {
		if ctx.Err() != nil {
			t.Errorf("renew context error got = %v, want = nil", ctx.Err())
		}
		if renews.Add(1) == 1 {
			return errors.New("unreachable")
		}
		return nil
	}
	drop := func(ctx context.Context) error {
		drops.Add(1)
		return ctx.Err()
	}
	release := HoldSlot(ctx, "slot", 5*time.Millisecond, renew, drop, nil)
	// The acquiring context ending does not stop the renewals.
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for renews.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := renews.Load(); got < 3 {
		t.Fatalf("renewals got = %d, want >= 3", got)
	}
	release()
	release()
	after := renews.Load()
	time.Sleep(20 * time.Millisecond)
	if got := renews.Load(); got != after {
		t.Errorf("renewals after release got = %d, want = %d", got, after)
	}
	if got := drops.Load(); got != 1 {
		t.Errorf("drops got = %d, want = 1", got)
	}
}
