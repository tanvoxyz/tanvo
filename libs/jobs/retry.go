package jobs

import (
	"errors"
	"math"
	"math/rand/v2"
	"time"

	"github.com/riverqueue/river"
)

// Backoff returns the delay before the next attempt of a job whose attempt
// number attempt (1 for the first run) failed.
type Backoff func(attempt int) time.Duration

// Polynomial is ActiveJob's :polynomially_longer wait, used by most of
// lago-api's retry_on declarations: attempt⁴ + 2 seconds plus up to
// jitter × attempt⁴ seconds of random jitter (ActiveJob's default jitter
// is 0.15; lago uses 0.75 for sequence conflicts).
func Polynomial(jitter float64) Backoff {
	return func(attempt int) time.Duration {
		base := math.Pow(float64(max(attempt, 1)), 4)
		return seconds(base + 2 + rand.Float64()*base*jitter)
	}
}

// RandomDelay waits a uniformly random whole number of seconds in
// [0, upTo), as lago-api's ApplicationJob.random_delay.
func RandomDelay(upTo time.Duration) Backoff {
	n := max(int64(upTo/time.Second), 1)
	return func(int) time.Duration { return time.Duration(rand.Int64N(n)) * time.Second }
}

// LinearDelay waits step × attempt, capped at limit when limit > 0, plus a
// random jitter in [0, step), as lago-api's ApplicationJob.linear_delay.
func LinearDelay(step, limit time.Duration) Backoff {
	return func(attempt int) time.Duration {
		d := step * time.Duration(max(attempt, 1))
		if limit > 0 {
			d = min(d, limit)
		}
		return d + time.Duration(rand.Int64N(max(int64(step), 1)))
	}
}

func seconds(s float64) time.Duration {
	if s >= math.MaxInt64/float64(time.Second) {
		return math.MaxInt64
	}
	return time.Duration(s * float64(time.Second))
}

// RetryRule gives the errors it matches their own attempt bound and
// backoff, as one retry_on declaration of a lago-api job.
type RetryRule struct {
	// Match selects the errors of the rule.
	Match func(error) bool
	// Attempts is the total number of attempts (the first run included)
	// for a job failing with a matching error; at least 1.
	Attempts int
	// Backoff is the wait between attempts; nil uses the kind's.
	Backoff Backoff
}

// RetryOn is the rule of errors that are, or wrap, one of targets.
func RetryOn(attempts int, backoff Backoff, targets ...error) RetryRule {
	return RetryRule{
		Match: func(err error) bool {
			for _, t := range targets {
				if errors.Is(err, t) {
					return true
				}
			}
			return false
		},
		Attempts: attempts,
		Backoff:  backoff,
	}
}

// Retry bounds of lago-api's ApplicationJob and its common retry_on
// declarations.
const (
	// LockRetryAttempts is MAX_LOCK_RETRY_ATTEMPTS, the attempts of jobs
	// failing to take a lock.
	LockRetryAttempts = 25
	// LockRetryMaxDelay is MAX_LOCK_RETRY_DELAY, the bound of the random
	// wait of [LockRetry].
	LockRetryMaxDelay = 16 * time.Second
	// RetriableAttempts is the attempts of ApplicationJob's generic
	// RetriableError.
	RetriableAttempts = 20
	// SequenceRetryAttempts is the attempts on a numbering conflict
	// (Sequenced::SequenceError).
	SequenceRetryAttempts = 15
	// ThrottlingRetryAttempts is the attempts on provider throttling
	// (BaseService::ThrottlingError).
	ThrottlingRetryAttempts = 25
	// RequestLimitRetryAttempts is the attempts of integration jobs on an
	// aggregator request limit (Integrations::Aggregator::RequestLimitError).
	RequestLimitRetryAttempts = 100
	// HTTPRetryAttempts is the attempts of most jobs on a provider HTTP
	// error, a timeout or a connection failure.
	HTTPRetryAttempts = 6
)

// LockRetry is lago-api's lock retry: [LockRetryAttempts] attempts with a
// random wait below [LockRetryMaxDelay], for the errors of targets.
func LockRetry(targets ...error) RetryRule {
	return RetryOn(LockRetryAttempts, RandomDelay(LockRetryMaxDelay), targets...)
}

// SequenceRetry is lago-api's retry of numbering conflicts:
// [SequenceRetryAttempts] attempts, polynomial with 0.75 jitter.
func SequenceRetry(targets ...error) RetryRule {
	return RetryOn(SequenceRetryAttempts, Polynomial(0.75), targets...)
}

// ErrRetriable marks an error to be retried up to [RetriableAttempts]
// times, as lago-api's RetriableError, whatever the kind's own bound.
var ErrRetriable = errors.New("jobs: retriable")

// Permanent marks err as not retryable: the job is cancelled at once.
func Permanent(err error) error { return river.JobCancel(err) }

// Snooze makes the job run again after d without counting an attempt, for
// waits that are not failures (a rate-limited provider, data not ready).
func Snooze(d time.Duration) error { return river.JobSnooze(d) }
