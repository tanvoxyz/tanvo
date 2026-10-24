package gocardless

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// limiters holds one token bucket per connection; a connection is one
// GoCardless access token, which the API throttles.
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
		// A burst of one second's budget absorbs the few calls of one
		// operation (list mandates, then pay) without delay.
		lim = rate.NewLimiter(l.rate, max(int(l.rate), 1))
		l.byConn[conn] = lim
	}
	l.mu.Unlock()
	return context.WithValue(ctx, limiterCtxKey{}, lim)
}

// recorder keeps the status and rate-limit reset of the last answer of an
// operation: the client drops them from undecodable error answers, which
// classification still needs.
type recorder struct {
	mu     sync.Mutex
	status int
	reset  string
}

type recorderCtxKey struct{}

// withRecorder returns ctx carrying a new recorder.
func withRecorder(ctx context.Context) (context.Context, *recorder) {
	r := &recorder{}
	return context.WithValue(ctx, recorderCtxKey{}, r), r
}

// last returns the status of the last answer, 0 when the last request got
// none, and the delay its RateLimit-Reset header asks for.
func (r *recorder) last(now time.Time) (status int, retry time.Duration) {
	if r == nil {
		return 0, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, err := http.ParseTime(r.reset); err == nil && t.After(now) {
		retry = t.Sub(now)
	}
	return r.status, retry
}

func (r *recorder) set(res *http.Response) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status, r.reset = 0, ""
	if res != nil {
		r.status, r.reset = res.StatusCode, res.Header.Get("RateLimit-Reset")
	}
}

// transport throttles every request on the limiter its context carries
// and records its answer on the context's recorder.
func (l *limiters) transport(next http.RoundTripper) http.RoundTripper {
	return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if lim, ok := r.Context().Value(limiterCtxKey{}).(*rate.Limiter); ok {
			if err := lim.Wait(r.Context()); err != nil {
				return nil, fmt.Errorf("gocardless rate limit: %w", err)
			}
		}
		res, err := next.RoundTrip(r)
		if rec, ok := r.Context().Value(recorderCtxKey{}).(*recorder); ok {
			rec.set(res)
		}
		return res, err
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
