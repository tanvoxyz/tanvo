package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Kind declares a job kind and its retry policy.
type Kind struct {
	// Name is the kind ("invoice.finalize"), as in [port.Job.Kind].
	Name string
	// MaxAttempts is the attempt bound of errors that no rule of Retry
	// matches (1: not retried); 0 uses the queue's.
	MaxAttempts int
	// Backoff is the wait between attempts; nil is [Polynomial](0.15).
	Backoff Backoff
	// Retry are the per-error bounds, first match wins. A rule always
	// present after them retries [ErrRetriable] [RetriableAttempts] times.
	Retry []RetryRule
	// Timeout bounds one attempt; 0 uses the client's job timeout, -1
	// disables it.
	Timeout time.Duration
}

// kindRE is River's format of job kinds.
var kindRE = regexp.MustCompile(`\A[\w][\w\-\[\]<>\/.·:+]+\z`)

func (k Kind) validate() error {
	if !kindRE.MatchString(k.Name) || k.MaxAttempts < 0 {
		return fmt.Errorf("jobs: kind %q: %w", k.Name, core.ErrInvalidValue)
	}
	for _, r := range k.Retry {
		if r.Match == nil || r.Attempts < 1 {
			return fmt.Errorf("jobs: kind %q: retry rule: %w", k.Name, core.ErrInvalidValue)
		}
	}
	return nil
}

// rule returns the retry rule of err.
func (k Kind) rule(err error) (RetryRule, bool) {
	for _, r := range k.Retry {
		if r.Match(err) {
			return r, true
		}
	}
	if errors.Is(err, ErrRetriable) {
		return RetryRule{Attempts: RetriableAttempts}, true
	}
	return RetryRule{}, false
}

// maxRuleAttempts is the largest attempt bound of the kind's rules.
func (k Kind) maxRuleAttempts() int {
	n := RetriableAttempts
	for _, r := range k.Retry {
		n = max(n, r.Attempts)
	}
	return n
}

// Job is one run of a job, handed to its [Handler].
type Job[A any] struct {
	// ID is River's job id.
	ID int64
	// Kind is the job kind.
	Kind string
	// Queue is the job's queue (its shard is not shown); [ClockQueue] for
	// periodic jobs.
	Queue port.Queue
	// OrganizationID is the tenant; zero for periodic jobs.
	OrganizationID core.OrganizationID
	// UniqueKey is the key the job was enqueued with.
	UniqueKey string
	// Args are the decoded arguments.
	Args A
	// Attempt is the number of this attempt, 1 for the first run.
	Attempt int
	// MaxAttempts is the job's attempt bound.
	MaxAttempts int
	// ScheduledAt is when the job was due; for a periodic job, the tick
	// that the jobs it dispatches should take as their "now".
	ScheduledAt time.Time
}

// Handler runs the jobs of one kind. A nil error completes the job; an
// error is retried by the kind's policy, except [Permanent] errors, which
// cancel it, and [Snooze], which postpones it.
type Handler[A any] interface {
	// Handle runs one attempt of job.
	Handle(ctx context.Context, job *Job[A]) error
}

// HandlerFunc adapts a function to [Handler].
type HandlerFunc[A any] func(ctx context.Context, job *Job[A]) error

// Handle implements [Handler].
func (f HandlerFunc[A]) Handle(ctx context.Context, job *Job[A]) error { return f(ctx, job) }

// Registry maps job kinds to their handlers. It is sealed by [New] and
// must not change afterwards.
type Registry struct {
	mu      sync.Mutex
	sealed  bool
	kinds   map[string]*entry
	workers *river.Workers
}

type entry struct {
	kind Kind
	run  func(ctx context.Context, row *rivertype.JobRow, e *envelope) error
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{kinds: map[string]*entry{}, workers: river.NewWorkers()}
}

// Register adds the handler h of kind k. It fails when k is invalid or
// already registered, or the registry is sealed.
func Register[A any](r *Registry, k Kind, h Handler[A]) error {
	if err := k.validate(); err != nil {
		return err
	}
	if h == nil {
		return fmt.Errorf("jobs: kind %q: nil handler: %w", k.Name, core.ErrInvalidValue)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return fmt.Errorf("jobs: register %q: registry is sealed: %w", k.Name, core.ErrNotAllowed)
	}
	if _, ok := r.kinds[k.Name]; ok {
		return fmt.Errorf("jobs: register %q: %w", k.Name, core.ErrConflict)
	}
	e := &entry{kind: k}
	e.run = func(ctx context.Context, row *rivertype.JobRow, env *envelope) error {
		job := &Job[A]{
			ID:             row.ID,
			Kind:           row.Kind,
			Queue:          queueOf(row.Queue),
			OrganizationID: env.Org,
			UniqueKey:      env.Key,
			Attempt:        row.Attempt,
			MaxAttempts:    row.MaxAttempts,
			ScheduledAt:    row.ScheduledAt.UTC(),
		}
		if len(env.Args) > 0 && string(env.Args) != "null" {
			if err := json.Unmarshal(env.Args, &job.Args); err != nil {
				return Permanent(fmt.Errorf("jobs: decode %s args: %w", row.Kind, err))
			}
		}
		return h.Handle(ctx, job)
	}
	if err := addWorker(r.workers, k.Name, &worker{entry: e}); err != nil {
		return fmt.Errorf("jobs: register %q: %w", k.Name, err)
	}
	r.kinds[k.Name] = e
	return nil
}

// addWorker registers w for kind. River exposes registration under a kind
// chosen at run time only through AddWorkerArgs, which panics on error.
func addWorker(ws *river.Workers, kind string, w *worker) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%v: %w", p, core.ErrInvalidValue)
		}
	}()
	river.AddWorkerArgs(ws, envelope{kind: kind}, w)
	return nil
}

// MustRegister is [Register] that panics on error, for wiring code.
func MustRegister[A any](r *Registry, k Kind, h Handler[A]) {
	if err := Register(r, k, h); err != nil {
		panic(err)
	}
}

// Kinds returns the registered kinds.
func (r *Registry) Kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.kinds))
	for k := range r.kinds {
		out = append(out, k)
	}
	return out
}

func (r *Registry) seal() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sealed = true
}

// lookup returns the entry of kind; the registry is sealed when it is
// called, so it needs no lock.
func (r *Registry) lookup(kind string) (*entry, bool) {
	e, ok := r.kinds[kind]
	return e, ok
}

// envelope is the River args of every job: the organization and unique
// key, which River's uniqueness hashes (with the kind), and the handler's
// arguments.
type envelope struct {
	// kind is the River kind; it is not encoded.
	kind string
	// Org is the tenant.
	Org core.OrganizationID `json:"org,omitzero" river:"unique"`
	// Key is the unique key.
	Key string `json:"key,omitempty" river:"unique"`
	// Max is the attempt bound of errors that no retry rule matches.
	Max int `json:"max,omitempty"`
	// Args are the handler's JSON arguments.
	Args json.RawMessage `json:"args,omitempty"`

	// backoff is the backoff of the failed attempt's rule, set by Work for
	// NextRetry on the same job.
	backoff Backoff
}

// Kind implements river.JobArgs.
func (e envelope) Kind() string { return e.kind }

// worker runs the jobs of one kind.
type worker struct {
	river.WorkerDefaults[envelope]
	entry *entry
}

func (w *worker) Timeout(*river.Job[envelope]) time.Duration { return w.entry.kind.Timeout }

func (w *worker) NextRetry(job *river.Job[envelope]) time.Time {
	b := job.Args.backoff
	if b == nil {
		b = w.entry.kind.Backoff
	}
	if b == nil {
		b = Polynomial(0.15)
	}
	return time.Now().UTC().Add(b(job.Attempt))
}

func (w *worker) Work(ctx context.Context, job *river.Job[envelope]) error {
	err := w.entry.run(ctx, job.JobRow, &job.Args)
	if err == nil {
		return nil
	}
	var cancel *rivertype.JobCancelError
	var snooze *rivertype.JobSnoozeError
	if errors.As(err, &cancel) || errors.As(err, &snooze) {
		return err
	}
	limit := job.Args.Max
	if r, ok := w.entry.kind.rule(err); ok {
		limit = r.Attempts
		job.Args.backoff = r.Backoff
	}
	if limit > 0 && job.Attempt >= limit && job.Attempt < job.MaxAttempts {
		// River discards a job only at its row's bound, the largest of the
		// kind's; a smaller bound of this error cancels it.
		return river.JobCancel(fmt.Errorf("after %d attempts: %w", job.Attempt, err))
	}
	return err
}
