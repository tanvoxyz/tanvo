package cache

import "time"

// WithBackoff sets the bounds of the reconnection backoff, for tests.
func WithBackoff(lo, hi time.Duration) ValkeyOption {
	return valkeyOption(func(c *valkeyConfig) { c.minBackoff, c.maxBackoff = lo, hi })
}

// SentinelAuth returns the credentials v reaches the sentinels with.
func SentinelAuth(v *Valkey) (user, password string) {
	return v.opt.Sentinel.Username, v.opt.Sentinel.Password
}
