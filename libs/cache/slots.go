package cache

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// errLeaseLost is the renewal failure of a lease that expired and was
// removed by another acquirer.
var errLeaseLost = errors.New("cache: slot lease taken by another holder")

// slotNow is the Lua prologue of the slot scripts: now is the server's
// clock in milliseconds, so that the clocks of the clients do not matter.
// Effects replication lets a script write after TIME on servers before
// Redis 5; later servers always replicate effects.
const slotNow = `if redis.replicate_commands then redis.replicate_commands() end
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local function ms(n) return string.format('%.0f', n) end
local function expire()
  local last = redis.call('ZRANGE', KEYS[1], -1, -1, 'WITHSCORES')
  if last[2] then redis.call('PEXPIRE', KEYS[1], ms(math.max(tonumber(last[2]) - now, 1))) end
end
`

// slotAcquireScript adds holder ARGV[1] to the sorted set KEYS[1] of the
// leases of a slot name, with its expiry (now plus ARGV[3] milliseconds)
// as score, unless ARGV[2] unexpired leases are already there; it drops
// the expired ones first and returns 1 when it added the lease, else 0.
// The key expires with its last lease, so that an idle name vanishes. A
// script runs atomically and touches one key, so it is safe on a cluster.
const slotAcquireScript = slotNow + `redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', '(' .. ms(now))
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[2]) then return 0 end
redis.call('ZADD', KEYS[1], ms(now + tonumber(ARGV[3])), ARGV[1])
expire()
return 1`

// slotRenewScript moves the expiry of holder ARGV[1] in KEYS[1] to now
// plus ARGV[2] milliseconds when its lease is still there, returning 1,
// else 0.
const slotRenewScript = slotNow + `if not redis.call('ZSCORE', KEYS[1], ARGV[1]) then return 0 end
redis.call('ZADD', KEYS[1], 'XX', ms(now + tonumber(ARGV[2])), ARGV[1])
expire()
return 1`

// AcquireSlot implements [port.Slots] with one sorted set per name, key
// <prefix>slots:<name>, whose members are the random tokens of the
// holders and whose scores are the expiries of their leases on the
// server's clock. Unlike the cache methods, a failure is an error, so
// that the caller can tell an unreachable server from taken slots:
// [ErrNotConnected] until the client has connected, else the command's
// error, counted and logged as a slot-acquire failure. Failed renewals and
// releases are counted and logged as slot-renew and slot-release
// failures.
func (v *Valkey) AcquireSlot(ctx context.Context, name string, limit int) (release func(), ok bool, err error) {
	if name == "" || limit < 1 {
		return nil, false, fmt.Errorf("cache: slot %q: %w", name, core.ErrInvalidValue)
	}
	c := v.load()
	if c == nil {
		return nil, false, ErrNotConnected
	}
	key := v.cfg.prefix + "slots:" + name
	holder := rand.Text()
	ttl := strconv.FormatInt(max(v.cfg.slotTTL.Milliseconds(), 1), 10)
	actx, cancel := context.WithTimeout(ctx, v.cfg.timeout)
	defer cancel()
	n, err := v.acquire.Exec(actx, c, []string{key}, []string{holder, strconv.Itoa(limit), ttl}).AsInt64()
	if err != nil {
		v.fail(actx, "slot-acquire", err)
		return nil, false, fmt.Errorf("cache: slot %s: %w", name, err)
	}
	if n == 0 {
		return nil, false, nil
	}
	renew := func(ctx context.Context) error {
		c := v.load()
		if c == nil {
			return ErrNotConnected
		}
		ctx, cancel := context.WithTimeout(ctx, v.cfg.timeout)
		defer cancel()
		n, err := v.renew.Exec(ctx, c, []string{key}, []string{holder, ttl}).AsInt64()
		if err == nil && n == 0 {
			err = errLeaseLost
		}
		if err != nil {
			v.fail(ctx, "slot-renew", err)
		}
		return err
	}
	drop := func(ctx context.Context) error {
		c := v.load()
		if c == nil {
			return ErrNotConnected
		}
		ctx, cancel := context.WithTimeout(ctx, v.cfg.timeout)
		defer cancel()
		err := c.Do(ctx, c.B().Zrem().Key(key).Member(holder).Build()).Error()
		if err != nil {
			v.fail(ctx, "slot-release", err)
		}
		return err
	}
	// The failures are counted and logged by fail.
	return port.HoldSlot(ctx, name, v.cfg.slotTTL/3, renew, drop, nil), true, nil
}
