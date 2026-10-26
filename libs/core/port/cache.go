package port

import (
	"context"
	"time"
)

// Cache keeps values computed from the database or from third parties
// for a while, so that the next request reuses them (lago Rails.cache).
// libs/cache implements it in the process and on Redis or Valkey, which
// every server process shares; apps/server picks one backend for every
// cache of the installation.
//
// A cache is best effort: it may drop any entry at any time, and the
// failures of reads and writes are misses that the implementation reports
// itself (logs and metrics), never errors of the request. Readers
// therefore validate what they read when staleness matters, and writers
// remove the entries a change invalidates. A removal is the exception: it
// returns its failure, since a dropped one leaves an entry that outlives
// the change; the callers whose entries are not validated when read fail
// the change on it.
//
// Values are opaque bytes; callers encode and decode them, so that every
// read returns a copy. Keys are namespaced by the caller.
type Cache interface {
	// Get returns the value of key, false when there is none.
	Get(ctx context.Context, key string) ([]byte, bool)
	// Set stores value under key for ttl, replacing any; a ttl that is
	// not positive stores nothing.
	Set(ctx context.Context, key string, value []byte, ttl time.Duration)
	// Delete removes the keys; missing keys are ignored. An error wraps
	// [core.ErrUnavailable] when the cache could not remove them.
	Delete(ctx context.Context, keys ...string) error
}
