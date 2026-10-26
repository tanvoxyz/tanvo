package auth

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
)

// testCache is a port.Cache in a map, recording reads and TTLs.
type testCache struct {
	mu   sync.Mutex
	data map[string][]byte
	ttls map[string]time.Duration
	hits int
	// down makes every removal fail.
	down bool
}

func newTestCache() *testCache {
	return &testCache{data: map[string][]byte{}, ttls: map[string]time.Duration{}}
}

func (c *testCache) Get(_ context.Context, key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.data[key]
	if ok {
		c.hits++
	}
	return slices.Clone(b), ok
}

func (c *testCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key], c.ttls[key] = slices.Clone(value), ttl
}

func (c *testCache) Delete(_ context.Context, keys ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down {
		return fmt.Errorf("test cache: %w", core.ErrUnavailable)
	}
	for _, k := range keys {
		delete(c.data, k)
		delete(c.ttls, k)
	}
	return nil
}

func (c *testCache) has(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.data[key]
	return ok
}

func TestAPIKeyCacheKey(t *testing.T) {
	// source: spec/services/api_keys/cache_service_spec.rb:12
	hash := access.HashToken("token")
	got := apiKeyCacheKey(hash)
	if want := "api_key/1/" + hex.EncodeToString(hash); got != want {
		t.Errorf("apiKeyCacheKey() got = %q, want = %q", got, want)
	}
	if strings.Contains(got, "token") {
		t.Errorf("apiKeyCacheKey() got = %q, want = no secret in the key", got)
	}
}

func TestAPIKeysAuthenticateCached(t *testing.T) {
	t.Run("without a cache reads the database", func(t *testing.T) {
		// source: spec/services/api_keys/cache_service_spec.rb:47
		e := newEnv(t)
		k := e.key(e.org())
		got, err := e.keys.AuthenticateCached(e.ctx, k.Secret)
		if err != nil || got.ID != k.Key.ID {
			t.Fatalf("AuthenticateCached() got = %v %v, want = %s", got, err, k.Key.ID)
		}
	})

	t.Run("caches the key once read", func(t *testing.T) {
		// source: spec/services/api_keys/cache_service_spec.rb:56
		e := newEnv(t)
		c := newTestCache()
		e.keys.WithCache(c, DefaultAPIKeyCacheTTL)
		k := e.key(e.org())
		got, err := e.keys.AuthenticateCached(e.ctx, k.Secret)
		if err != nil || got.ID != k.Key.ID {
			t.Fatalf("AuthenticateCached() got = %v %v, want = %s", got, err, k.Key.ID)
		}
		ck := apiKeyCacheKey(k.Key.TokenHash)
		if !c.has(ck) {
			t.Fatalf("cache got = no entry, want = an entry under %s", ck)
		}
		if c.ttls[ck] != DefaultAPIKeyCacheTTL {
			t.Errorf("ttl got = %s, want = %s", c.ttls[ck], DefaultAPIKeyCacheTTL)
		}

		// source: spec/services/api_keys/cache_service_spec.rb:65
		again, err := e.keys.AuthenticateCached(e.ctx, k.Secret)
		if err != nil || again.ID != k.Key.ID || again.OrganizationID != k.Key.OrganizationID {
			t.Fatalf("cached AuthenticateCached() got = %v %v, want = %s", again, err, k.Key.ID)
		}
		if c.hits != 1 {
			t.Errorf("cache hits got = %d, want = 1", c.hits)
		}
		if !slices.Equal(again.TokenHash, k.Key.TokenHash) || again.Prefix != k.Key.Prefix {
			t.Errorf("cached key got = %+v, want = %+v", again, k.Key)
		}
	})

	t.Run("refuses a cached key that expired", func(t *testing.T) {
		// source: spec/services/api_keys/cache_service_spec.rb:80
		e := newEnv(t)
		c := newTestCache()
		e.keys.WithCache(c, DefaultAPIKeyCacheTTL)
		k := e.key(e.org())
		if _, err := e.keys.AuthenticateCached(e.ctx, k.Secret); err != nil {
			t.Fatal(err)
		}
		// Expire the key behind the cache's back: the cached copy says it
		// never expires, so only the expiry check of the copy protects it.
		past := e.clock.Now().Add(-10 * time.Minute)
		cached := *k.Key
		cached.ExpiresAt = &past
		b, err := json.Marshal(&cached)
		if err != nil {
			t.Fatal(err)
		}
		c.Set(e.ctx, apiKeyCacheKey(k.Key.TokenHash), b, time.Hour)
		if _, err := e.keys.AuthenticateCached(e.ctx, k.Secret); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("AuthenticateCached() got = %v, want = ErrUnauthenticated", err)
		}
	})

	t.Run("keeps an expiring key no longer than its expiry", func(t *testing.T) {
		e := newEnv(t)
		c := newTestCache()
		e.keys.WithCache(c, DefaultAPIKeyCacheTTL)
		o := e.org()
		old := e.key(o)
		in := e.clock.Now().Add(10 * time.Minute)
		if _, err := e.keys.Rotate(e.ctx, o.ID, old.Key.ID, APIKeyRotation{ExpiresAt: &in}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.keys.AuthenticateCached(e.ctx, old.Secret); err != nil {
			t.Fatal(err)
		}
		if got := c.ttls[apiKeyCacheKey(old.Key.TokenHash)]; got != 10*time.Minute {
			t.Errorf("ttl got = %s, want = 10m", got)
		}
	})

	t.Run("unknown secrets are not cached", func(t *testing.T) {
		e := newEnv(t)
		c := newTestCache()
		e.keys.WithCache(c, DefaultAPIKeyCacheTTL)
		for _, s := range []string{"", "nope"} {
			if _, err := e.keys.AuthenticateCached(e.ctx, s); !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("AuthenticateCached(%q) got = %v, want = ErrUnauthenticated", s, err)
			}
		}
		if len(c.data) != 0 {
			t.Errorf("cache entries got = %d, want = 0", len(c.data))
		}
	})
}

func TestAPIKeysChangesExpireTheCache(t *testing.T) {
	// source: spec/services/api_keys/cache_service_spec.rb:18
	name := "renamed"
	for _, tc := range []struct {
		name   string
		change func(e *env, k *CreatedAPIKey) error
		// valid reports whether the old secret still authenticates.
		valid bool
	}{
		{"update", func(e *env, k *CreatedAPIKey) error {
			_, err := e.keys.Update(e.ctx, k.Key.OrganizationID, k.Key.ID, APIKeyUpdate{Name: &name})
			return err
		}, true},
		{"rotate", func(e *env, k *CreatedAPIKey) error {
			_, err := e.keys.Rotate(e.ctx, k.Key.OrganizationID, k.Key.ID, APIKeyRotation{})
			return err
		}, false},
		{"destroy", func(e *env, k *CreatedAPIKey) error {
			_, err := e.keys.Destroy(e.ctx, k.Key.OrganizationID, k.Key.ID, true)
			return err
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			c := newTestCache()
			e.keys.WithCache(c, DefaultAPIKeyCacheTTL)
			k := e.key(e.org())
			if _, err := e.keys.AuthenticateCached(e.ctx, k.Secret); err != nil {
				t.Fatal(err)
			}
			// A cache that cannot remove the entry fails the change, which
			// does not happen.
			c.mu.Lock()
			c.down = true
			c.mu.Unlock()
			if err := tc.change(e, k); !errors.Is(err, core.ErrUnavailable) {
				t.Fatalf("%s with the cache down: error got = %v, want = %v", tc.name, err, core.ErrUnavailable)
			}
			c.mu.Lock()
			c.down = false
			c.mu.Unlock()
			if err := c.Delete(e.ctx, apiKeyCacheKey(k.Key.TokenHash)); err != nil {
				t.Fatal(err)
			}
			if got, err := e.keys.AuthenticateCached(e.ctx, k.Secret); err != nil || got.Name != nil && *got.Name == name {
				t.Fatalf("AuthenticateCached() after the failed %s got = %v %v, want = the key unchanged", tc.name, got, err)
			}
			if err := tc.change(e, k); err != nil {
				t.Fatal(err)
			}
			if c.has(apiKeyCacheKey(k.Key.TokenHash)) {
				t.Fatalf("cache got = entry after %s, want = none", tc.name)
			}
			got, err := e.keys.AuthenticateCached(e.ctx, k.Secret)
			if tc.valid {
				if err != nil || got.Name == nil || *got.Name != name {
					t.Errorf("AuthenticateCached() got = %v %v, want = the renamed key", got, err)
				}
				return
			}
			if !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("AuthenticateCached() got = %v, want = ErrUnauthenticated", err)
			}
		})
	}
}
