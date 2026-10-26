// Package cache implements [port.Cache], the best-effort bytes cache of
// libs/core (architecture §3.2).
//
// Two backends exist; apps/server picks one for every cache of the
// installation:
//
//   - [Memory] keeps entries in the process: a bounded LRU with a TTL per
//     entry. Each server process has its own, so a write in one process
//     does not invalidate the copies of the others; it suits single-process
//     installations and tests.
//   - [Valkey] keeps entries on a Valkey or Redis server (standalone,
//     Sentinel or cluster) that every process shares, optionally with
//     server-assisted client-side caching ([WithClientSideTTL]). The server
//     is never a dependency of the process: while it is unreachable, at
//     startup or later, reads miss and writes are dropped, and the client
//     reconnects in the background.
//
// [Valkey] also implements [port.Slots] with leases in one sorted set per
// slot name ([Valkey.AcquireSlot]); unlike the cache, its failures are
// errors of the caller.
//
// [Namespace] gives each user of a cache a key prefix of its own and
// counts its hits and misses as OpenTelemetry metrics.
//
// A failure of the backend is never an error of the caller: it is a miss,
// which the backend logs (rate limited) and counts. Values are copied in
// and out, so callers may reuse the slices they pass and receive.
package cache

import "github.com/tanvoxyz/tanvo/libs/core/port"

// Static checks that the backends implement the port.
var (
	_ port.Cache = (*Memory)(nil)
	_ port.Cache = (*Valkey)(nil)
	_ port.Cache = (*namespace)(nil)
	_ port.Slots = (*Valkey)(nil)
)
