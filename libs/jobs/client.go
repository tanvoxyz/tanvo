package jobs

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// DefaultSchema is the PostgreSQL schema of River's tables.
const DefaultSchema = "river"

// Config configures a [Client].
type Config struct {
	// Schema is the schema of River's tables; "" is [DefaultSchema].
	Schema string
	// Queues overrides the configuration of some queues; the others use
	// [DefaultQueues].
	Queues map[port.Queue]QueueConfig
	// Work lists the queues this process runs jobs of; empty makes an
	// insert-only client (the api role).
	Work []port.Queue
	// DisablePeriodic keeps a working client out of the periodic schedule.
	// Every client that works queues and may be elected leader should
	// carry the same schedule.
	DisablePeriodic bool
	// ClockConcurrency is the number of periodic jobs a client with the
	// schedule runs at once on [ClockQueue]; 0 is
	// [DefaultClockConcurrency]. Clients without the schedule do not work
	// [ClockQueue].
	ClockConcurrency int
	// DisabledPeriodic lists periodic kinds not to schedule (lago-api's
	// LAGO_DISABLE_EVENTS_VALIDATION, LAGO_DISABLE_LIFETIME_USAGE_REFRESH,
	// LAGO_DISABLE_WALLET_REFRESH).
	DisabledPeriodic []string
	// Intervals overrides the timing of periodic kinds with an interval
	// (lago-api's LAGO_SUBSCRIPTION_ACTIVITY_PROCESSING_INTERVAL_SECONDS,
	// LAGO_LIFETIME_USAGE_REFRESH_INTERVAL_SECONDS,
	// LAGO_WALLET_ONGOING_BALANCE_REFRESH_INTERVAL_SECONDS).
	Intervals map[string]time.Duration
	// JobTimeout bounds an attempt of kinds without their own timeout; 0
	// is 30 minutes.
	JobTimeout time.Duration
	// FetchCooldown and FetchPollInterval tune how often workers look for
	// jobs; 0 keeps River's defaults (100 ms, 1 s).
	FetchCooldown, FetchPollInterval time.Duration
	// Clock is the time source of the token buckets; nil is the system
	// clock.
	Clock core.Clock
	// Logger receives River's logs; nil is slog.Default().
	Logger *slog.Logger
	// Middleware is installed on the River client, in order, around job
	// inserts and job work: telemetry (River's OpenTelemetry middleware)
	// and logging context. apps/server provides it, so that this library
	// stays on the OpenTelemetry API.
	Middleware []rivertype.Middleware
}

// Client inserts jobs and, when configured to work queues, runs them.
type Client struct {
	river    *river.Client[pgx.Tx]
	reg      *Registry
	queues   map[port.Queue]QueueConfig
	fair     *fairness
	clock    core.Clock
	periodic []Periodic
}

// New returns a client on pool running the handlers of reg, which it
// seals. River's tables must exist ([Migrate]).
func New(pool *pgxpool.Pool, reg *Registry, cfg Config) (*Client, error) {
	queues := DefaultQueues()
	for q, c := range cfg.Queues {
		if !q.Valid() {
			return nil, fmt.Errorf("jobs: queue %q: %w", q, core.ErrInvalidValue)
		}
		queues[q] = c
	}
	for q, c := range queues {
		if err := c.validate(q); err != nil {
			return nil, err
		}
	}
	if cfg.ClockConcurrency < 0 || cfg.ClockConcurrency > 10_000 {
		return nil, fmt.Errorf("jobs: clock concurrency %d: %w", cfg.ClockConcurrency, core.ErrInvalidValue)
	}
	reg.seal()
	c := &Client{reg: reg, queues: queues, fair: newFairness(), clock: cfg.Clock}
	if c.clock == nil {
		c.clock = core.SystemClock{}
	}
	rc := &river.Config{
		Schema:            cmp.Or(cfg.Schema, DefaultSchema),
		Workers:           reg.workers,
		JobTimeout:        cmp.Or(cfg.JobTimeout, 30*time.Minute),
		MaxAttempts:       DefaultMaxAttempts,
		FetchCooldown:     cfg.FetchCooldown,
		FetchPollInterval: cfg.FetchPollInterval,
		Logger:            cfg.Logger,
		Middleware:        slices.Clone(cfg.Middleware),
	}
	if len(cfg.Work) > 0 {
		rc.Queues = map[string]river.QueueConfig{}
		for _, q := range cfg.Work {
			qc, ok := queues[q]
			if !ok {
				return nil, fmt.Errorf("jobs: work queue %q: %w", q, core.ErrInvalidValue)
			}
			for i := range qc.Shards {
				rc.Queues[RiverQueue(q, i)] = river.QueueConfig{MaxWorkers: qc.workersPerShard()}
			}
		}
		if !cfg.DisablePeriodic {
			pjs, scheduled, err := c.periodicJobs(cfg)
			if err != nil {
				return nil, err
			}
			rc.PeriodicJobs = pjs
			c.periodic = scheduled
			rc.Queues[string(ClockQueue)] = river.QueueConfig{MaxWorkers: cmp.Or(cfg.ClockConcurrency, DefaultClockConcurrency)}
		}
	}
	rv, err := river.NewClient(riverpgxv5.New(pool), rc)
	if err != nil {
		return nil, fmt.Errorf("jobs: new river client: %w", err)
	}
	c.river = rv
	return c, nil
}

// periodicJobs builds the River periodic jobs of the schedule entries
// whose kind has a handler; they run on [ClockQueue].
func (c *Client) periodicJobs(cfg Config) ([]*river.PeriodicJob, []Periodic, error) {
	var out []*river.PeriodicJob
	var scheduled []Periodic
	for _, p := range Schedule() {
		e, ok := c.reg.lookup(p.Kind)
		if !ok || slices.Contains(cfg.DisabledPeriodic, p.Kind) {
			continue
		}
		if d, ok := cfg.Intervals[p.Kind]; ok {
			if d <= 0 {
				return nil, nil, fmt.Errorf("jobs: interval of %s: %w", p.Kind, core.ErrInvalidValue)
			}
			p.Timing = Every(d)
		}
		env, opts := clockInsert(p.Kind, e)
		out = append(out, river.NewPeriodicJob(p.Timing, func() (river.JobArgs, *river.InsertOpts) {
			return env, opts
		}, &river.PeriodicJobOpts{ID: p.Kind}))
		scheduled = append(scheduled, p)
	}
	return out, scheduled, nil
}

// clockInsert returns the River insert of a run of the periodic kind of
// entry e. Clock jobs are not retried (lago-api's sidekiq retry: 0): the
// next run catches up.
func clockInsert(kind string, e *entry) (envelope, *river.InsertOpts) {
	attempts := cmp.Or(e.kind.MaxAttempts, 1)
	return envelope{kind: kind, Max: attempts}, &river.InsertOpts{
		Queue:       string(ClockQueue),
		MaxAttempts: max(attempts, e.kind.maxRuleAttempts()),
		UniqueOpts:  uniqueOpts(),
	}
}

// Trigger enqueues a run of the periodic job kind now, outside its timing
// (an operator's "run now"): it runs on [ClockQueue], on the processes
// that carry the schedule, and is skipped while a run of kind is
// unfinished. It fails with core.ErrNotFound when kind is not in
// [Schedule] or has no handler.
func (c *Client) Trigger(ctx context.Context, kind string) error {
	e, ok := c.reg.lookup(kind)
	if !ok || !slices.ContainsFunc(Schedule(), func(p Periodic) bool { return p.Kind == kind }) {
		return fmt.Errorf("jobs: trigger %q: %w", kind, core.ErrNotFound)
	}
	env, opts := clockInsert(kind, e)
	if _, err := c.river.Insert(ctx, env, opts); err != nil {
		return fmt.Errorf("jobs: trigger %q: %w", kind, err)
	}
	return nil
}

// uniqueOpts makes a job unique by kind, organization and unique key
// among the jobs not finished yet (lago-api's unique :until_executed).
func uniqueOpts() river.UniqueOpts {
	return river.UniqueOpts{
		ByArgs: true,
		ByState: []rivertype.JobState{
			rivertype.JobStateAvailable,
			rivertype.JobStatePending,
			rivertype.JobStateRetryable,
			rivertype.JobStateRunning,
			rivertype.JobStateScheduled,
		},
	}
}

// Periodic returns the schedule entries this client runs when it is the
// leader.
func (c *Client) Periodic() []Periodic { return slices.Clone(c.periodic) }

// Start starts working the configured queues; it returns at once.
func (c *Client) Start(ctx context.Context) error {
	if err := c.river.Start(ctx); err != nil {
		return fmt.Errorf("jobs: start: %w", err)
	}
	return nil
}

// Stop waits for running jobs to finish, or for ctx to be done.
func (c *Client) Stop(ctx context.Context) error {
	if err := c.river.Stop(ctx); err != nil {
		return fmt.Errorf("jobs: stop: %w", err)
	}
	return nil
}

// River returns the underlying River client, for administration (job
// lists, cancellation) and tests.
func (c *Client) River() *river.Client[pgx.Tx] { return c.river }

// InsertTx inserts jobs with tx: they become visible to workers when tx
// commits and vanish when it rolls back. A job whose (kind, organization,
// unique key) matches an unfinished job is skipped. libs/store calls it
// from port.JobEnqueuer.
func (c *Client) InsertTx(ctx context.Context, tx pgx.Tx, jobs []port.Job) error {
	params, err := c.params(jobs)
	if err != nil || len(params) == 0 {
		return err
	}
	if _, err := c.river.InsertManyTx(ctx, tx, params); err != nil {
		return fmt.Errorf("jobs: insert: %w", err)
	}
	return nil
}

// Insert inserts jobs in a transaction of their own, for callers without
// one.
func (c *Client) Insert(ctx context.Context, jobs ...port.Job) error {
	params, err := c.params(jobs)
	if err != nil || len(params) == 0 {
		return err
	}
	if _, err := c.river.InsertMany(ctx, params); err != nil {
		return fmt.Errorf("jobs: insert: %w", err)
	}
	return nil
}

// Enqueuer returns a [port.JobEnqueuer] inserting with tx.
func (c *Client) Enqueuer(tx pgx.Tx) port.JobEnqueuer { return txEnqueuer{c, tx} }

type txEnqueuer struct {
	c  *Client
	tx pgx.Tx
}

func (e txEnqueuer) Enqueue(ctx context.Context, jobs ...port.Job) error {
	return e.c.InsertTx(ctx, e.tx, jobs)
}

// QueueNow returns clock_timestamp(), the clock River schedules jobs by
// ([port.QueueClock]).
func (e txEnqueuer) QueueNow(ctx context.Context) (time.Time, error) {
	var at time.Time
	if err := e.tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&at); err != nil {
		return time.Time{}, fmt.Errorf("jobs: queue clock: %w", err)
	}
	return at.UTC(), nil
}

type uniqueID struct {
	kind, key string
	org       core.OrganizationID
}

// params validates jobs and maps them to River inserts: the shard of the
// organization, the token bucket's delay, the attempt bounds and the
// unique options. Duplicates within jobs are dropped, as one River insert
// cannot hold the same unique key twice.
func (c *Client) params(jobs []port.Job) ([]river.InsertManyParams, error) {
	now := c.clock.Now().UTC()
	out := make([]river.InsertManyParams, 0, len(jobs))
	seen := map[uniqueID]bool{}
	for _, j := range jobs {
		if j.Kind == "" || !j.Queue.Valid() || j.OrganizationID.IsZero() || j.MaxAttempts < 0 {
			return nil, fmt.Errorf("jobs: enqueue %q: %w", j.Kind, core.ErrInvalidValue)
		}
		e, ok := c.reg.lookup(j.Kind)
		if !ok {
			return nil, fmt.Errorf("jobs: enqueue %q: unknown kind: %w", j.Kind, core.ErrInvalidValue)
		}
		if j.UniqueKey != "" {
			id := uniqueID{j.Kind, j.UniqueKey, j.OrganizationID}
			if seen[id] {
				continue
			}
			seen[id] = true
		}
		qc := c.queues[j.Queue]
		base := cmp.Or(j.MaxAttempts, e.kind.MaxAttempts, qc.MaxAttempts, DefaultMaxAttempts)
		rowMax := base
		if j.MaxAttempts == 0 {
			rowMax = max(base, e.kind.maxRuleAttempts())
		}
		opts := &river.InsertOpts{
			Queue:       RiverQueue(j.Queue, Shard(j.OrganizationID, qc.Shards)),
			MaxAttempts: rowMax,
		}
		if j.UniqueKey != "" {
			opts.UniqueOpts = uniqueOpts()
		}
		at := j.RunAt.UTC()
		if d := c.fair.delay(j.Queue, qc, j.OrganizationID, now); d > 0 {
			at = later(at, now.Add(d))
		}
		if at.After(now) {
			opts.ScheduledAt = at
		}
		out = append(out, river.InsertManyParams{
			Args:       envelope{kind: j.Kind, Org: j.OrganizationID, Key: j.UniqueKey, Max: base, Args: slices.Clone(j.Args)},
			InsertOpts: opts,
		})
	}
	return out, nil
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
