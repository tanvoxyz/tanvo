package moneyhash

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"golang.org/x/time/rate"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// limiters holds one token bucket per connection; a connection is one
// MoneyHash API key.
type limiters struct {
	rate rate.Limit

	mu     sync.Mutex
	byConn map[core.IntegrationID]*rate.Limiter
}

type limiterCtxKey struct{}

func newLimiters(r rate.Limit) *limiters {
	return &limiters{rate: r, byConn: map[core.IntegrationID]*rate.Limiter{}}
}

// with returns ctx carrying the connection's limiter, or ctx unchanged when
// throttling is disabled.
func (l *limiters) with(ctx context.Context, conn core.IntegrationID) context.Context {
	if l.rate <= 0 {
		return ctx
	}
	l.mu.Lock()
	lim, ok := l.byConn[conn]
	if !ok {
		lim = rate.NewLimiter(l.rate, max(int(l.rate), 1))
		l.byConn[conn] = lim
	}
	l.mu.Unlock()
	return context.WithValue(ctx, limiterCtxKey{}, lim)
}

// transport throttles every request on the limiter its context carries.
func (l *limiters) transport(next http.RoundTripper) http.RoundTripper {
	return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if lim, ok := r.Context().Value(limiterCtxKey{}).(*rate.Limiter); ok {
			if err := lim.Wait(r.Context()); err != nil {
				return nil, fmt.Errorf("moneyhash rate limit: %w", err)
			}
		}
		return next.RoundTrip(r)
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
