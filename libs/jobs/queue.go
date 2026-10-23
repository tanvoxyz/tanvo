package jobs

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// QueueConfig configures one queue.
type QueueConfig struct {
	// Concurrency is the number of jobs of the queue one worker process
	// runs at once, spread evenly over the shards (at least one worker per
	// shard).
	Concurrency int
	// Shards splits the queue by organization hash: one organization's
	// jobs all go to one shard, so it holds at most Concurrency / Shards
	// workers of a process and cannot starve organizations of other
	// shards. 1 disables sharding. Shard 0 is the queue's own name, shard
	// i > 0 is "<queue>_<i>"; lowering Shards strands the jobs of the
	// removed shards, so drain them first.
	Shards int
	// Rate is the sustained number of jobs per second one organization may
	// enqueue on the queue before its jobs are delayed; 0 disables the
	// token bucket.
	Rate float64
	// Burst is the token bucket's size: the jobs an idle organization may
	// enqueue at once without delay.
	Burst int
	// MaxAttempts is the attempt bound of jobs whose kind and enqueue do
	// not set one.
	MaxAttempts int
}

// DefaultMaxAttempts is the attempt bound of a job when neither the job,
// its kind nor its queue sets one: an error that no retry rule classifies is
// not retried, because a job may have had effects outside its transaction
// (a payment, an email, a provider call) that a second run would repeat.
// Errors known to be safe to retry are declared per kind ([Kind.Retry]).
const DefaultMaxAttempts = 1

// ClockQueue is the River queue of the periodic jobs of [Schedule]. It is
// not sharded and has no token bucket: its jobs belong to no organization.
// A periodic job scans for due work and enqueues it as jobs of the
// organizations concerned on the workload queues, so a backlog of a
// workload queue does not delay the schedule, a long scan does not hold a
// workload worker, and one tenant's failure does not stop the others.
const ClockQueue port.Queue = "clock"

// DefaultClockConcurrency is the number of periodic jobs one process runs
// at once on [ClockQueue] when [Config.ClockConcurrency] is 0.
const DefaultClockConcurrency = 5

// DefaultQueues returns the default configuration of every queue.
func DefaultQueues() map[port.Queue]QueueConfig {
	return map[port.Queue]QueueConfig{
		port.QueueBilling:            {Concurrency: 20, Shards: 4, Rate: 50, Burst: 1000, MaxAttempts: DefaultMaxAttempts},
		port.QueuePayments:           {Concurrency: 10, Shards: 4, Rate: 20, Burst: 500, MaxAttempts: DefaultMaxAttempts},
		port.QueueWebhooks:           {Concurrency: 20, Shards: 4, Rate: 100, Burst: 2000, MaxAttempts: DefaultMaxAttempts},
		port.QueueDocuments:          {Concurrency: 5, Shards: 2, Rate: 20, Burst: 500, MaxAttempts: DefaultMaxAttempts},
		port.QueueIntegrations:       {Concurrency: 10, Shards: 4, Rate: 20, Burst: 500, MaxAttempts: DefaultMaxAttempts},
		port.QueueExports:            {Concurrency: 2, Shards: 2, Rate: 1, Burst: 20, MaxAttempts: DefaultMaxAttempts},
		port.QueueAlerts:             {Concurrency: 10, Shards: 2, Rate: 50, Burst: 1000, MaxAttempts: DefaultMaxAttempts},
		port.QueueAlertsHighPriority: {Concurrency: 10, Shards: 2, Rate: 50, Burst: 1000, MaxAttempts: DefaultMaxAttempts},
		port.QueueWallets:            {Concurrency: 10, Shards: 2, Rate: 50, Burst: 1000, MaxAttempts: DefaultMaxAttempts},
		port.QueueDefault:            {Concurrency: 10, Shards: 1, Rate: 50, Burst: 1000, MaxAttempts: DefaultMaxAttempts},
	}
}

func (c QueueConfig) validate(q port.Queue) error {
	switch {
	case c.Concurrency < 1 || c.Concurrency > 10_000:
		return fmt.Errorf("jobs: queue %s: concurrency %d: %w", q, c.Concurrency, core.ErrInvalidValue)
	case c.Shards < 1 || c.Shards > 256:
		return fmt.Errorf("jobs: queue %s: shards %d: %w", q, c.Shards, core.ErrInvalidValue)
	case c.Rate < 0 || c.Burst < 0 || (c.Rate > 0 && c.Burst < 1):
		return fmt.Errorf("jobs: queue %s: rate %g burst %d: %w", q, c.Rate, c.Burst, core.ErrInvalidValue)
	case c.MaxAttempts < 0:
		return fmt.Errorf("jobs: queue %s: max attempts %d: %w", q, c.MaxAttempts, core.ErrInvalidValue)
	}
	return nil
}

// workersPerShard is the River MaxWorkers of each shard.
func (c QueueConfig) workersPerShard() int {
	return max(1, (c.Concurrency+c.Shards-1)/c.Shards)
}

// Shard returns the shard of org among n: FNV-1a of the organization's
// UUID modulo n. The zero organization is shard 0.
func Shard(org core.OrganizationID, n int) int {
	if n <= 1 || org.IsZero() {
		return 0
	}
	h := fnv.New64a()
	u := org.UUID()
	_, _ = h.Write(u[:])
	return int(h.Sum64() % uint64(n))
}

// RiverQueue returns the River queue name of shard i of q.
func RiverQueue(q port.Queue, i int) string {
	if i == 0 {
		return string(q)
	}
	return string(q) + "_" + strconv.Itoa(i)
}

// queueOf returns the queue of a River queue name.
func queueOf(name string) port.Queue {
	if q := port.Queue(name); q.Valid() {
		return q
	}
	if i := strings.LastIndexByte(name, '_'); i > 0 {
		return port.Queue(name[:i])
	}
	return port.Queue(name)
}

// fairness holds the token buckets of (queue, organization) pairs. The
// buckets live in the enqueuing process: with several API replicas an
// organization's effective rate is the sum of theirs.
type fairness struct {
	mu      sync.Mutex
	buckets map[bucketKey]*rate.Limiter
	// limit bounds the number of buckets kept; full buckets are dropped
	// when it is reached, as they behave like new ones.
	limit int
}

type bucketKey struct {
	queue port.Queue
	org   core.OrganizationID
}

func newFairness() *fairness {
	return &fairness{buckets: map[bucketKey]*rate.Limiter{}, limit: 10_000}
}

// delay takes one token of org's bucket on q at now and returns how long
// the job must wait for it.
func (f *fairness) delay(q port.Queue, c QueueConfig, org core.OrganizationID, now time.Time) time.Duration {
	if c.Rate <= 0 || org.IsZero() {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	k := bucketKey{q, org}
	l, ok := f.buckets[k]
	if !ok {
		if len(f.buckets) >= f.limit {
			f.evict(now)
		}
		l = rate.NewLimiter(rate.Limit(c.Rate), c.Burst)
		f.buckets[k] = l
	}
	return l.ReserveN(now, 1).DelayFrom(now)
}

func (f *fairness) evict(now time.Time) {
	for k, l := range f.buckets {
		if l.TokensAt(now) >= float64(l.Burst()) {
			delete(f.buckets, k)
		}
	}
}
