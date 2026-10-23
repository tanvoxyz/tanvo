package stripe

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"golang.org/x/time/rate"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// limiters holds one token bucket per connection; Stripe's limits are per
// account, and a connection is one account key.
type limiters struct {
	live, test rate.Limit

	mu     sync.Mutex
	byConn map[limiterKey]*rate.Limiter
}

type limiterKey struct {
	conn core.IntegrationID
	live bool
}

type limiterCtxKey struct{}

func newLimiters(live, test rate.Limit) *limiters {
	return &limiters{live: live, test: test, byConn: map[limiterKey]*rate.Limiter{}}
}

// with returns ctx carrying the connection's limiter, or ctx unchanged when
// its mode is not throttled.
func (l *limiters) with(ctx context.Context, conn core.IntegrationID, live bool) context.Context {
	r := l.test
	if live {
		r = l.live
	}
	if r <= 0 {
		return ctx
	}
	k := limiterKey{conn, live}
	l.mu.Lock()
	lim, ok := l.byConn[k]
	if !ok {
		// A burst of one second's budget absorbs the few calls of one
		// operation (customer lookup, list, create) without delay.
		lim = rate.NewLimiter(r, max(int(r), 1))
		l.byConn[k] = lim
	}
	l.mu.Unlock()
	return context.WithValue(ctx, limiterCtxKey{}, lim)
}

// transport throttles every request, retries included, on the limiter its
// context carries.
func (l *limiters) transport(next http.RoundTripper) http.RoundTripper {
	return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if lim, ok := r.Context().Value(limiterCtxKey{}).(*rate.Limiter); ok {
			if err := lim.Wait(r.Context()); err != nil {
				return nil, fmt.Errorf("stripe rate limit: %w", err)
			}
		}
		return next.RoundTrip(r)
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
