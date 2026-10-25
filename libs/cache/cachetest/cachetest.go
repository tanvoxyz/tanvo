package cachetest

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Option configures [Run].
type Option func(*config)

// config collects the options of [Run].
type config struct {
	ttl     time.Duration
	advance func(t *testing.T, c port.Cache, d time.Duration)
}

// WithTTL sets the short TTL of the expiry tests, 500ms by default. It
// must be long enough for a Set and a Get to complete well within it.
func WithTTL(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.ttl = d
		}
	}
}

// WithAdvance sets how the expiry tests let time pass for a cache, real
// sleeping by default; a cache with a fake clock moves it instead.
func WithAdvance(advance func(t *testing.T, c port.Cache, d time.Duration)) Option {
	return func(c *config) {
		if advance != nil {
			c.advance = advance
		}
	}
}

// long is the TTL of entries that must not expire during a test.
const long = time.Hour

// Run runs the conformance suite as subtests of t; newCache returns an
// empty cache for each, whose keys no other subtest uses.
func Run(t *testing.T, newCache func(t *testing.T) port.Cache, opts ...Option) {
	t.Helper()
	cfg := config{
		ttl:     500 * time.Millisecond,
		advance: func(_ *testing.T, _ port.Cache, d time.Duration) { time.Sleep(d) },
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	ctx := context.Background()

	t.Run("GetMissing", func(t *testing.T) {
		c := newCache(t)
		wantMiss(t, c, "missing")
	})

	t.Run("SetGet", func(t *testing.T) {
		c := newCache(t)
		c.Set(ctx, "k", []byte("v1"), long)
		want(t, c, "k", []byte("v1"))
		c.Set(ctx, "k", []byte("value two"), long)
		want(t, c, "k", []byte("value two"))
		c.Set(ctx, "other", []byte("o"), long)
		want(t, c, "k", []byte("value two"))
		want(t, c, "other", []byte("o"))
	})

	t.Run("EmptyValue", func(t *testing.T) {
		c := newCache(t)
		for _, v := range [][]byte{{}, nil} {
			c.Delete(ctx, "k")
			c.Set(ctx, "k", v, long)
			got, ok := c.Get(ctx, "k")
			if !ok || len(got) != 0 {
				t.Errorf("Get(k) after Set(k, %#v) = %q, %v, want = empty, true", v, got, ok)
			}
		}
	})

	t.Run("NonPositiveTTL", func(t *testing.T) {
		c := newCache(t)
		for _, ttl := range []time.Duration{0, -time.Second} {
			key := "ttl" + ttl.String()
			c.Set(ctx, key, []byte("v"), ttl)
			wantMiss(t, c, key)
		}
	})

	t.Run("Expiry", func(t *testing.T) {
		c := newCache(t)
		c.Set(ctx, "short", []byte("s"), cfg.ttl)
		c.Set(ctx, "long", []byte("l"), long)
		want(t, c, "short", []byte("s"))
		cfg.advance(t, c, 2*cfg.ttl)
		wantMiss(t, c, "short")
		want(t, c, "long", []byte("l"))
		// A Set renews the TTL of an expired key.
		c.Set(ctx, "short", []byte("s2"), long)
		want(t, c, "short", []byte("s2"))
	})

	t.Run("OverwriteTTL", func(t *testing.T) {
		c := newCache(t)
		c.Set(ctx, "k", []byte("v1"), cfg.ttl)
		c.Set(ctx, "k", []byte("v2"), long)
		cfg.advance(t, c, 2*cfg.ttl)
		want(t, c, "k", []byte("v2"))
	})

	t.Run("Delete", func(t *testing.T) {
		c := newCache(t)
		for _, k := range []string{"a", "b", "c"} {
			c.Set(ctx, k, []byte(k), long)
		}
		c.Delete(ctx)
		c.Delete(ctx, "missing")
		c.Delete(ctx, "a", "missing", "c")
		wantMiss(t, c, "a")
		wantMiss(t, c, "c")
		want(t, c, "b", []byte("b"))
		c.Delete(ctx, "b", "b")
		wantMiss(t, c, "b")
	})

	t.Run("CopiesValues", func(t *testing.T) {
		c := newCache(t)
		in := []byte("original")
		c.Set(ctx, "k", in, long)
		copy(in, "XXXXXXXX")
		got, _ := c.Get(ctx, "k")
		if !bytes.Equal(got, []byte("original")) {
			t.Fatalf("Get(k) after mutating the value set = %q, want = %q", got, "original")
		}
		copy(got, "YYYYYYYY")
		want(t, c, "k", []byte("original"))
	})

	t.Run("BinaryValue", func(t *testing.T) {
		c := newCache(t)
		v := make([]byte, 0, 512)
		for i := range 256 {
			v = append(v, byte(i))
		}
		v = append(v, "\r\n$-1\r\n\x00\xff"...)
		c.Set(ctx, "bin", v, long)
		want(t, c, "bin", v)
	})

	t.Run("BinaryKey", func(t *testing.T) {
		c := newCache(t)
		key := "k \x00\r\n\xff:é"
		c.Set(ctx, key, []byte("v"), long)
		want(t, c, key, []byte("v"))
		c.Delete(ctx, key)
		wantMiss(t, c, key)
	})

	t.Run("LargeValue", func(t *testing.T) {
		c := newCache(t)
		v := make([]byte, 4<<20)
		_, _ = rand.Read(v)
		c.Set(ctx, "large", v, long)
		want(t, c, "large", v)
	})

	t.Run("Concurrent", func(t *testing.T) {
		c := newCache(t)
		const workers, rounds, keys = 8, 200, 16
		var wg sync.WaitGroup
		errs := make(chan string, workers)
		for w := range workers {
			wg.Go(func() {
				for i := range rounds {
					k := "k" + strconv.Itoa((w+i)%keys)
					switch i % 4 {
					case 0, 1:
						c.Set(ctx, k, []byte(k+"="+strconv.Itoa(w)), long)
					case 2:
						c.Delete(ctx, k)
					}
					// Any value read was written for the key it is read from.
					if v, ok := c.Get(ctx, k); ok && !bytes.HasPrefix(v, []byte(k+"=")) {
						errs <- fmt.Sprintf("Get(%s) = %q, want = a value of %s", k, v, k)
						return
					}
				}
			})
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			t.Error(e)
		}
	})
}

// want fails t unless key holds value.
func want(t *testing.T, c port.Cache, key string, value []byte) {
	t.Helper()
	got, ok := c.Get(context.Background(), key)
	if !ok || !bytes.Equal(got, value) {
		t.Fatalf("Get(%q) = %s, %v, want = %s, true", key, abbrev(got), ok, abbrev(value))
	}
}

// wantMiss fails t unless key misses.
func wantMiss(t *testing.T, c port.Cache, key string) {
	t.Helper()
	if got, ok := c.Get(context.Background(), key); ok {
		t.Fatalf("Get(%q) = %s, true, want = a miss", key, abbrev(got))
	}
}

// abbrev formats a value for a failure message, cutting long ones.
func abbrev(b []byte) string {
	if len(b) > 64 {
		return fmt.Sprintf("%q… (%d bytes)", b[:64], len(b))
	}
	return fmt.Sprintf("%q", b)
}
