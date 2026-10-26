package cache_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/tanvoxyz/tanvo/libs/cache"
	"github.com/tanvoxyz/tanvo/libs/core"
)

// rawClient returns a client of the server at rawURL, closed when the
// test ends.
func rawClient(t *testing.T, rawURL string) valkey.Client {
	t.Helper()
	raw, err := valkey.NewClient(valkey.MustParseURL(rawURL + "?client_cache=0"))
	if err != nil {
		t.Fatalf("NewClient() error got = %v, want = nil", err)
	}
	t.Cleanup(raw.Close)
	return raw
}

// holders returns the members of the lease set of name.
func holders(t *testing.T, raw valkey.Client, prefix, name string) []string {
	t.Helper()
	m, err := raw.Do(context.Background(), raw.B().Zrange().Key(prefix+"slots:"+name).Min("0").Max("-1").Build()).AsStrSlice()
	if err != nil {
		t.Fatalf("ZRANGE error got = %v, want = nil", err)
	}
	return m
}

// TestValkeySlots checks the limit shared by two processes, release,
// idempotent release and the validation of the arguments.
func TestValkeySlots(t *testing.T) {
	ctx := context.Background()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			prefix := uniquePrefix()
			one := newValkey(t, b.url, cache.WithPrefix(prefix))
			two := newValkey(t, b.url, cache.WithPrefix(prefix))
			r1, ok, err := one.AcquireSlot(ctx, "sync", 2)
			if err != nil || !ok {
				t.Fatalf("first slot: got = %v, %v, want = taken", ok, err)
			}
			r2, ok, err := two.AcquireSlot(ctx, "sync", 2)
			if err != nil || !ok {
				t.Fatalf("second slot: got = %v, %v, want = taken", ok, err)
			}
			if _, ok, err := one.AcquireSlot(ctx, "sync", 2); err != nil || ok {
				t.Fatalf("third slot: got = %v, %v, want = not taken", ok, err)
			}
			if r, ok, err := two.AcquireSlot(ctx, "other", 1); err != nil || !ok {
				t.Fatalf("other name: got = %v, %v, want = taken", ok, err)
			} else {
				r()
			}
			r1()
			r1()
			r3, ok, err := two.AcquireSlot(ctx, "sync", 2)
			if err != nil || !ok {
				t.Fatalf("after release: got = %v, %v, want = taken", ok, err)
			}
			r2()
			r3()
			raw := rawClient(t, b.url)
			if n, err := raw.Do(ctx, raw.B().Exists().Key(prefix+"slots:sync").Build()).AsInt64(); err != nil || n != 0 {
				t.Errorf("lease set after release got = %d, %v, want = 0, nil", n, err)
			}
			for _, tt := range []struct {
				name  string
				limit int
			}{{"sync", 0}, {"", 1}} {
				if _, _, err := one.AcquireSlot(ctx, tt.name, tt.limit); !errors.Is(err, core.ErrInvalidValue) {
					t.Errorf("AcquireSlot(%q, %d) error got = %v, want = %v", tt.name, tt.limit, err, core.ErrInvalidValue)
				}
			}
		})
	}
}

// TestValkeySlotsRace checks that concurrent acquirers of two processes
// take exactly the limit.
func TestValkeySlotsRace(t *testing.T) {
	ctx := context.Background()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			prefix := uniquePrefix()
			clients := []*cache.Valkey{newValkey(t, b.url, cache.WithPrefix(prefix)), newValkey(t, b.url, cache.WithPrefix(prefix))}
			const limit = 3
			var (
				wg       sync.WaitGroup
				taken    atomic.Int32
				mu       sync.Mutex
				releases []func()
			)
			for i := range 40 {
				wg.Go(func() {
					release, ok, err := clients[i%2].AcquireSlot(ctx, "race", limit)
					if err != nil {
						t.Errorf("AcquireSlot() error got = %v, want = nil", err)
					}
					if ok {
						taken.Add(1)
						mu.Lock()
						releases = append(releases, release)
						mu.Unlock()
					}
				})
			}
			wg.Wait()
			if got := taken.Load(); got != limit {
				t.Errorf("slots taken got = %d, want = %d", got, limit)
			}
			for _, r := range releases {
				r()
			}
		})
	}
}

// TestValkeySlotsExpiry checks that a held lease is renewed past its TTL
// and that the lease of a crashed holder expires.
func TestValkeySlotsExpiry(t *testing.T) {
	ctx := context.Background()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			prefix := uniquePrefix()
			v := newValkey(t, b.url, cache.WithPrefix(prefix), cache.WithSlotTTL(300*time.Millisecond))
			release, ok, err := v.AcquireSlot(ctx, "renewed", 1)
			if err != nil || !ok {
				t.Fatalf("slot: got = %v, %v, want = taken", ok, err)
			}
			time.Sleep(time.Second)
			if _, ok, err := v.AcquireSlot(ctx, "renewed", 1); err != nil || ok {
				t.Errorf("slot past its TTL: got = %v, %v, want = not taken", ok, err)
			}
			release()

			// A crashed holder's lease: one that nobody renews.
			raw := rawClient(t, b.url)
			if err := raw.Do(ctx, raw.B().Zadd().Key(prefix+"slots:crashed").ScoreMember().
				ScoreMember(float64(time.Now().Add(-time.Second).UnixMilli()), "gone").Build()).Error(); err != nil {
				t.Fatalf("ZADD error got = %v, want = nil", err)
			}
			r, ok, err := v.AcquireSlot(ctx, "crashed", 1)
			if err != nil || !ok {
				t.Fatalf("expired slot: got = %v, %v, want = taken", ok, err)
			}
			if got := holders(t, raw, prefix, "crashed"); len(got) != 1 || got[0] == "gone" {
				t.Errorf("holders got = %v, want = only the new holder", got)
			}
			pttl, err := raw.Do(ctx, raw.B().Pttl().Key(prefix+"slots:crashed").Build()).AsInt64()
			if err != nil || pttl <= 0 || pttl > 300 {
				t.Errorf("PTTL got = %d, %v, want = in (0, 300]", pttl, err)
			}
			r()
		})
	}
}

// TestValkeySlotsStaleHolder checks that a holder whose lease expired and
// was taken neither renews nor deletes the new holder's lease.
func TestValkeySlotsStaleHolder(t *testing.T) {
	ctx := context.Background()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			prefix := uniquePrefix()
			v := newValkey(t, b.url, cache.WithPrefix(prefix), cache.WithSlotTTL(300*time.Millisecond))
			raw := rawClient(t, b.url)
			stale, ok, err := v.AcquireSlot(ctx, "stale", 1)
			if err != nil || !ok {
				t.Fatalf("first slot: got = %v, %v, want = taken", ok, err)
			}
			first := holders(t, raw, prefix, "stale")
			if len(first) != 1 {
				t.Fatalf("holders got = %v, want = one", first)
			}
			// The first holder stalls past its TTL.
			if err := raw.Do(ctx, raw.B().Zadd().Key(prefix+"slots:stale").Xx().ScoreMember().ScoreMember(1, first[0]).Build()).Error(); err != nil {
				t.Fatalf("ZADD error got = %v, want = nil", err)
			}
			release, ok, err := v.AcquireSlot(ctx, "stale", 1)
			if err != nil || !ok {
				t.Fatalf("expired slot: got = %v, %v, want = taken", ok, err)
			}
			defer release()
			want := holders(t, raw, prefix, "stale")
			// Past a few renewals of both holders and the stale one's
			// release, the lease is still the new holder's alone.
			time.Sleep(300 * time.Millisecond)
			stale()
			if got := holders(t, raw, prefix, "stale"); len(got) != 1 || len(want) != 1 || got[0] != want[0] || got[0] == first[0] {
				t.Errorf("holders got = %v, want = %v", got, want)
			}
		})
	}
}
