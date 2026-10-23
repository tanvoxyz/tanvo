package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/jobs"
)

type testArgs struct {
	N int `json:"n"`
}

func org() core.OrganizationID { return core.NewID[core.OrganizationID](core.UUIDv7Generator{}) }

func job(kind string, o core.OrganizationID, n int, key string) port.Job {
	b, _ := json.Marshal(testArgs{N: n})
	return port.Job{Kind: kind, Queue: port.QueueDefault, OrganizationID: o, Args: b, UniqueKey: key}
}

func noop() jobs.HandlerFunc[testArgs] {
	return func(context.Context, *jobs.Job[testArgs]) error { return nil }
}

// unlimited turns the token buckets off.
func unlimited() map[port.Queue]jobs.QueueConfig {
	out := map[port.Queue]jobs.QueueConfig{}
	for q, c := range jobs.DefaultQueues() {
		c.Rate, c.Burst = 0, 0
		out[q] = c
	}
	return out
}

func newClient(t *testing.T, s string, reg *jobs.Registry, cfg jobs.Config) *jobs.Client {
	t.Helper()
	cfg.Schema = s
	cfg.FetchCooldown = 20 * time.Millisecond
	cfg.FetchPollInterval = 100 * time.Millisecond
	if cfg.Queues == nil {
		cfg.Queues = unlimited()
	}
	c, err := jobs.New(testDB.pool, reg, cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	return c
}

func start(t *testing.T, c *jobs.Client) {
	t.Helper()
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := c.Stop(ctx); err != nil {
			t.Errorf("Stop() = %v", err)
		}
	})
}

func inTx(t *testing.T, fn func(tx pgx.Tx) error) error {
	t.Helper()
	return pgx.BeginFunc(context.Background(), testDB.pool, fn)
}

func TestInsertTxCommitsWithTheTransaction(t *testing.T) {
	t.Parallel()
	_, s := schema(t)
	reg := jobs.NewRegistry()
	jobs.MustRegister(reg, jobs.Kind{Name: "test.tx"}, noop())
	c := newClient(t, s, reg, jobs.Config{})
	ctx := context.Background()
	o := org()

	errRollback := errors.New("rollback")
	err := inTx(t, func(tx pgx.Tx) error {
		if err := c.InsertTx(ctx, tx, []port.Job{job("test.tx", o, 1, "")}); err != nil {
			return err
		}
		if got := len(rows(t, s)); got != 0 {
			t.Errorf("rows visible before commit = %d, want = 0", got)
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("tx = %v, want = %v", err, errRollback)
	}
	if got := rows(t, s); len(got) != 0 {
		t.Fatalf("rows after rollback = %v, want none", got)
	}

	if err := inTx(t, func(tx pgx.Tx) error {
		return c.Enqueuer(tx).Enqueue(ctx, job("test.tx", o, 2, ""))
	}); err != nil {
		t.Fatalf("tx = %v", err)
	}
	got := rows(t, s)
	if len(got) != 1 || got[0].Kind != "test.tx" || got[0].State != "available" {
		t.Fatalf("rows after commit = %+v, want one available test.tx", got)
	}

	for _, bad := range []port.Job{
		{Kind: "", Queue: port.QueueDefault, OrganizationID: o},
		{Kind: "test.tx", Queue: "nope", OrganizationID: o},
		{Kind: "test.tx", Queue: port.QueueDefault},
		{Kind: "test.unknown", Queue: port.QueueDefault, OrganizationID: o},
	} {
		if err := c.Insert(ctx, bad); !errors.Is(err, core.ErrInvalidValue) {
			t.Errorf("Insert(%+v) = %v, want = %v", bad, err, core.ErrInvalidValue)
		}
	}
}

func TestUniqueKey(t *testing.T) {
	t.Parallel()
	_, s := schema(t)
	reg := jobs.NewRegistry()
	jobs.MustRegister(reg, jobs.Kind{Name: "test.a"}, noop())
	jobs.MustRegister(reg, jobs.Kind{Name: "test.b"}, noop())
	ctx := context.Background()
	c := newClient(t, s, reg, jobs.Config{})
	o1, o2 := org(), org()

	if err := inTx(t, func(tx pgx.Tx) error {
		return c.InsertTx(ctx, tx, []port.Job{job("test.a", o1, 1, "k"), job("test.a", o1, 2, "k")})
	}); err != nil {
		t.Fatal(err)
	}
	if err := inTx(t, func(tx pgx.Tx) error {
		return c.InsertTx(ctx, tx, []port.Job{
			job("test.a", o1, 3, "k"),  // duplicate of a pending job
			job("test.a", o2, 4, "k"),  // other organization
			job("test.b", o1, 5, "k"),  // other kind
			job("test.a", o1, 6, "k2"), // other key
			job("test.a", o1, 7, ""),   // no key: never unique
			job("test.a", o1, 8, ""),
		})
	}); err != nil {
		t.Fatal(err)
	}
	if got := len(rows(t, s)); got != 6 {
		t.Fatalf("rows = %d, want = 6: %+v", got, rows(t, s))
	}

	// Once the job has run, the key is free again.
	w := newClient(t, s, reg, jobs.Config{Work: []port.Queue{port.QueueDefault}, DisablePeriodic: true})
	start(t, w)
	eventually(t, 15*time.Second, "jobs to complete", func() bool {
		for _, r := range rows(t, s) {
			if r.State != "completed" {
				return false
			}
		}
		return true
	})
	if err := c.Insert(ctx, job("test.a", o1, 9, "k")); err != nil {
		t.Fatal(err)
	}
	if got := len(rows(t, s)); got != 7 {
		t.Fatalf("rows after re-enqueue = %d, want = 7", got)
	}
}

func TestRunAt(t *testing.T) {
	t.Parallel()
	_, s := schema(t)
	ran := make(chan time.Time, 1)
	reg := jobs.NewRegistry()
	jobs.MustRegister(reg, jobs.Kind{Name: "test.later"}, jobs.HandlerFunc[testArgs](func(context.Context, *jobs.Job[testArgs]) error {
		ran <- time.Now()
		return nil
	}))
	c := newClient(t, s, reg, jobs.Config{Work: []port.Queue{port.QueueDefault}, DisablePeriodic: true})
	start(t, c)
	at := time.Now().Add(2 * time.Second).Truncate(time.Microsecond)
	j := job("test.later", org(), 1, "")
	j.RunAt = at
	if err := c.Insert(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	got := rows(t, s)
	if len(got) != 1 || got[0].State != "scheduled" || !got[0].ScheduledAt.Equal(at) {
		t.Fatalf("rows = %+v, want one scheduled at %v", got, at)
	}
	select {
	case when := <-ran:
		if when.Before(at) {
			t.Errorf("ran at %v, before run-at %v", when, at)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("scheduled job did not run")
	}
}

var (
	errLocked = errors.New("locked")
	errOther  = errors.New("other")
)

func TestRetries(t *testing.T) {
	t.Parallel()
	_, s := schema(t)
	zero := func(int) time.Duration { return 0 }
	var mu sync.Mutex
	attempts := map[int][]int{} // args.N -> attempts seen
	fail := func(err error) jobs.HandlerFunc[testArgs] {
		return func(_ context.Context, j *jobs.Job[testArgs]) error {
			mu.Lock()
			attempts[j.Args.N] = append(attempts[j.Args.N], j.Attempt)
			mu.Unlock()
			return err
		}
	}
	reg := jobs.NewRegistry()
	jobs.MustRegister(reg, jobs.Kind{Name: "test.lock", MaxAttempts: 1, Backoff: zero, Retry: []jobs.RetryRule{jobs.RetryOn(3, zero, errLocked)}}, fail(errLocked))
	jobs.MustRegister(reg, jobs.Kind{Name: "test.unmatched", MaxAttempts: 2, Backoff: zero, Retry: []jobs.RetryRule{jobs.RetryOn(5, zero, errLocked)}}, fail(errOther))
	jobs.MustRegister(reg, jobs.Kind{Name: "test.permanent", Backoff: zero}, fail(jobs.Permanent(errOther)))
	jobs.MustRegister(reg, jobs.Kind{Name: "test.override", Backoff: zero}, fail(errOther))
	// No MaxAttempts: an unclassified error is not retried, and ErrRetriable is.
	jobs.MustRegister(reg, jobs.Kind{Name: "test.default", Backoff: zero}, fail(errOther))
	jobs.MustRegister(reg, jobs.Kind{Name: "test.retriable", Backoff: zero}, jobs.HandlerFunc[testArgs](func(_ context.Context, j *jobs.Job[testArgs]) error {
		mu.Lock()
		attempts[j.Args.N] = append(attempts[j.Args.N], j.Attempt)
		mu.Unlock()
		if j.Attempt < 3 {
			return fmt.Errorf("busy: %w", jobs.ErrRetriable)
		}
		return nil
	}))
	c := newClient(t, s, reg, jobs.Config{Work: []port.Queue{port.QueueDefault}, DisablePeriodic: true})
	start(t, c)
	o := org()
	override := job("test.override", o, 4, "")
	override.MaxAttempts = 2
	if err := c.Insert(context.Background(), job("test.lock", o, 1, ""), job("test.unmatched", o, 2, ""), job("test.permanent", o, 3, ""), override, job("test.default", o, 5, ""), job("test.retriable", o, 6, "")); err != nil {
		t.Fatal(err)
	}
	final := func(state string) bool { return state == "cancelled" || state == "discarded" || state == "completed" }
	eventually(t, 20*time.Second, "jobs to finish", func() bool {
		for _, r := range rows(t, s) {
			if !final(r.State) {
				return false
			}
		}
		return true
	})
	got := rows(t, s)
	want := []struct {
		state    string
		attempts int
	}{{"cancelled", 3}, {"cancelled", 2}, {"cancelled", 1}, {"discarded", 2}, {"cancelled", 1}, {"completed", 3}}
	for i, w := range want {
		mu.Lock()
		n := len(attempts[i+1])
		mu.Unlock()
		if got[i].State != w.state || n != w.attempts {
			t.Errorf("%s: state = %s, attempts = %d, want = %s, %d", got[i].Kind, got[i].State, n, w.state, w.attempts)
		}
	}
}

func TestPeriodicSchedule(t *testing.T) {
	t.Parallel()
	_, s := schema(t)
	var runs atomic.Int64
	reg := jobs.NewRegistry()
	jobs.MustRegister(reg, jobs.Kind{Name: jobs.KindActivateSubscriptions}, jobs.HandlerFunc[struct{}](func(_ context.Context, j *jobs.Job[struct{}]) error {
		if !j.OrganizationID.IsZero() || j.Queue != jobs.ClockQueue {
			t.Errorf("periodic job org = %v, queue = %s, want = none, %s", j.OrganizationID, j.Queue, jobs.ClockQueue)
		}
		runs.Add(1)
		return nil
	}))
	jobs.MustRegister(reg, jobs.Kind{Name: jobs.KindValidateEvents}, jobs.HandlerFunc[struct{}](func(context.Context, *jobs.Job[struct{}]) error {
		t.Error("disabled periodic job ran")
		return nil
	}))
	c := newClient(t, s, reg, jobs.Config{
		Work:             []port.Queue{port.QueueBilling},
		Intervals:        map[string]time.Duration{jobs.KindActivateSubscriptions: 500 * time.Millisecond},
		DisabledPeriodic: []string{jobs.KindValidateEvents},
	})
	p := c.Periodic()
	if len(p) != 1 || p[0].Kind != jobs.KindActivateSubscriptions || p[0].Timing.String() != "every 500ms" {
		t.Fatalf("Periodic() = %+v, want only %s every 500ms", p, jobs.KindActivateSubscriptions)
	}
	start(t, c)
	eventually(t, 20*time.Second, "two periodic runs", func() bool { return runs.Load() >= 2 })
	for _, r := range rows(t, s) {
		if r.Kind != jobs.KindActivateSubscriptions || r.Queue != "clock" || r.Max != jobs.RetriableAttempts {
			t.Errorf("periodic row = %+v", r)
		}
	}
}

// TestClockQueueDispatchesPastABacklog checks that periodic jobs run on the
// clock queue while the workload queue they dispatch to is saturated, and
// that their dispatched jobs carry the tick's time and land on that queue.
func TestClockQueueDispatchesPastABacklog(t *testing.T) {
	t.Parallel()
	_, s := schema(t)
	o := org()
	release := make(chan struct{})
	var ticks atomic.Int64
	var c *jobs.Client
	reg := jobs.NewRegistry()
	jobs.MustRegister(reg, jobs.Kind{Name: "test.work"}, jobs.HandlerFunc[testArgs](func(ctx context.Context, j *jobs.Job[testArgs]) error {
		if j.Queue != port.QueueBilling || j.OrganizationID != o {
			t.Errorf("dispatched job queue = %s, org = %v, want = billing, %v", j.Queue, j.OrganizationID, o)
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}))
	jobs.MustRegister(reg, jobs.Kind{Name: jobs.KindBillSubscriptions}, jobs.HandlerFunc[struct{}](func(ctx context.Context, j *jobs.Job[struct{}]) error {
		ticks.Add(1)
		b, _ := json.Marshal(testArgs{N: int(j.ScheduledAt.Unix())})
		return c.Insert(ctx, port.Job{Kind: "test.work", Queue: port.QueueBilling, OrganizationID: o, Args: b, UniqueKey: "bill"})
	}))
	queues := unlimited()
	queues[port.QueueBilling] = jobs.QueueConfig{Concurrency: 1, Shards: 1}
	c = newClient(t, s, reg, jobs.Config{
		Queues:    queues,
		Work:      []port.Queue{port.QueueBilling},
		Intervals: map[string]time.Duration{jobs.KindBillSubscriptions: 200 * time.Millisecond},
	})
	start(t, c)
	t.Cleanup(func() { close(release) })
	// The first dispatched job holds the only billing worker; the clock
	// keeps ticking, and the unique key skips the later dispatches while it
	// runs.
	eventually(t, 20*time.Second, "ticks past the blocked billing worker", func() bool { return ticks.Load() >= 4 })
	work := 0
	for _, r := range rows(t, s) {
		switch r.Kind {
		case "test.work":
			work++
			if r.Queue != "billing" {
				t.Errorf("dispatched row queue = %s, want = billing", r.Queue)
			}
		case jobs.KindBillSubscriptions:
			if r.Queue != "clock" {
				t.Errorf("periodic row queue = %s, want = clock", r.Queue)
			}
		}
	}
	if work != 1 {
		t.Errorf("unfinished dispatched rows = %d, want = 1", work)
	}
}

func TestClockQueueIsNotEnqueuable(t *testing.T) {
	t.Parallel()
	_, s := schema(t)
	if _, err := jobs.New(testDB.pool, jobs.NewRegistry(), jobs.Config{Schema: s, ClockConcurrency: -1}); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("New(ClockConcurrency: -1) = %v, want = %v", err, core.ErrInvalidValue)
	}
	reg := jobs.NewRegistry()
	jobs.MustRegister(reg, jobs.Kind{Name: "test.tx"}, noop())
	c := newClient(t, s, reg, jobs.Config{})
	j := job("test.tx", org(), 1, "")
	j.Queue = jobs.ClockQueue
	if err := c.Insert(context.Background(), j); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("Insert(clock queue) = %v, want = %v", err, core.ErrInvalidValue)
	}
}

func TestPeriodicUniqueWhileRunning(t *testing.T) {
	t.Parallel()
	_, s := schema(t)
	release := make(chan struct{})
	var runs atomic.Int64
	reg := jobs.NewRegistry()
	jobs.MustRegister(reg, jobs.Kind{Name: jobs.KindBillSubscriptions}, jobs.HandlerFunc[struct{}](func(ctx context.Context, _ *jobs.Job[struct{}]) error {
		runs.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}))
	c := newClient(t, s, reg, jobs.Config{
		Work:      []port.Queue{port.QueueBilling},
		Intervals: map[string]time.Duration{jobs.KindBillSubscriptions: 200 * time.Millisecond},
	})
	start(t, c)
	t.Cleanup(func() { close(release) })
	eventually(t, 20*time.Second, "a periodic run", func() bool { return runs.Load() >= 1 })
	time.Sleep(time.Second) // five more ticks
	if got := len(rows(t, s)); got != 1 {
		t.Errorf("rows while the first run is running = %d, want = 1", got)
	}
}

func TestTokenBucketDelaysABusyOrganization(t *testing.T) {
	t.Parallel()
	_, s := schema(t)
	reg := jobs.NewRegistry()
	jobs.MustRegister(reg, jobs.Kind{Name: "test.fair"}, noop())
	queues := unlimited()
	qc := queues[port.QueueDefault]
	qc.Rate, qc.Burst = 1, 2
	queues[port.QueueDefault] = qc
	now := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	c := newClient(t, s, reg, jobs.Config{Queues: queues, Clock: core.FixedClock(now)})
	busy, quiet := org(), org()
	var batch []port.Job
	for i := range 5 {
		batch = append(batch, job("test.fair", busy, i, ""))
	}
	batch = append(batch, job("test.fair", quiet, 9, ""))
	if err := c.Insert(context.Background(), batch...); err != nil {
		t.Fatal(err)
	}
	got := rows(t, s)
	wantDelay := []time.Duration{0, 0, time.Second, 2 * time.Second, 3 * time.Second, 0}
	for i, r := range got {
		d := r.ScheduledAt.Sub(now)
		if wantDelay[i] == 0 {
			// Not delayed: River schedules it at the database's now.
			if r.State != "available" {
				t.Errorf("job %d: state = %s, want = available", i, r.State)
			}
			continue
		}
		if d != wantDelay[i] || r.State != "scheduled" {
			t.Errorf("job %d: delay = %v, state = %s, want = %v, scheduled", i, d, r.State, wantDelay[i])
		}
	}
}

func TestShardsIsolateOrganizations(t *testing.T) {
	t.Parallel()
	_, s := schema(t)
	busy := org()
	quiet := org()
	for jobs.Shard(quiet, 2) == jobs.Shard(busy, 2) {
		quiet = org()
	}
	release := make(chan struct{})
	var running, maxRunning atomic.Int64
	quietDone := make(chan struct{})
	reg := jobs.NewRegistry()
	jobs.MustRegister(reg, jobs.Kind{Name: "test.slow"}, jobs.HandlerFunc[testArgs](func(ctx context.Context, j *jobs.Job[testArgs]) error {
		if j.OrganizationID == quiet {
			close(quietDone)
			return nil
		}
		n := running.Add(1)
		defer running.Add(-1)
		for {
			m := maxRunning.Load()
			if n <= m || maxRunning.CompareAndSwap(m, n) {
				break
			}
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}))
	queues := unlimited()
	queues[port.QueueDefault] = jobs.QueueConfig{Concurrency: 2, Shards: 2}
	c := newClient(t, s, reg, jobs.Config{Queues: queues, Work: []port.Queue{port.QueueDefault}, DisablePeriodic: true})
	start(t, c)
	t.Cleanup(func() { close(release) })
	ctx := context.Background()
	var batch []port.Job
	for i := range 4 {
		batch = append(batch, job("test.slow", busy, i, ""))
	}
	if err := c.Insert(ctx, batch...); err != nil {
		t.Fatal(err)
	}
	eventually(t, 15*time.Second, "the busy organization to hold its shard", func() bool { return running.Load() == 1 })
	if err := c.Insert(ctx, job("test.slow", quiet, 9, "")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-quietDone:
	case <-time.After(15 * time.Second):
		t.Fatal("the quiet organization's job waited behind the busy one")
	}
	if got := maxRunning.Load(); got != 1 {
		t.Errorf("busy organization's concurrent jobs = %d, want = 1", got)
	}
	queuesSeen := map[string]bool{}
	for _, r := range rows(t, s) {
		queuesSeen[r.Queue] = true
	}
	if len(queuesSeen) != 2 {
		t.Errorf("queues = %v, want both shards", queuesSeen)
	}
}

// TestMigrateOnASmallPool runs more concurrent migrations than the pool has
// connections: the waiters must not hold the connections the holder needs.
func TestMigrateOnASmallPool(t *testing.T) {
	t.Parallel()
	if testDB.pool == nil {
		t.Skip(testDB.skip)
	}
	cfg := testDB.pool.Config()
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var wg sync.WaitGroup
	for i := range 4 {
		name := fmt.Sprintf("river_small_pool_%d_%d", testDB.seq.Add(1), i)
		t.Cleanup(func() {
			if _, err := testDB.pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgx.Identifier{name}.Sanitize()+" CASCADE"); err != nil {
				t.Errorf("drop schema: %v", err)
			}
		})
		wg.Go(func() {
			if err := jobs.Migrate(ctx, pool, name); err != nil {
				t.Errorf("Migrate(%s) = %v", name, err)
			}
		})
	}
	wg.Wait()
}
