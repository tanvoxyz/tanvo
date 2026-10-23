package store

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindInfra binds the locks, the sequences and the job outbox.
func (t *txState) bindInfra(r *port.Repos) {
	r.Locks = locker{t}
	r.Sequences = sequences{t}
	r.Jobs = jobEnqueuer{t}
}

// locker implements port.Locker with transaction-scoped advisory locks. The
// 64-bit lock key is hashtextextended of "organization/scope/id"; a hash
// collision only serializes two unrelated operations. Advisory locks are
// re-entrant within a transaction, so taking a held lock again is a no-op.
type locker struct{ t *txState }

func (l locker) Lock(ctx context.Context, org core.OrganizationID, k port.LockKey) error {
	if org.IsZero() || k.Scope == "" {
		return fmt.Errorf("lock %s: %w", k, core.ErrInvalidValue)
	}
	key := org.String() + "/" + k.String()
	if _, err := l.t.tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", key); err != nil {
		return fmt.Errorf("lock %s: %w", k, mapErr(err))
	}
	return nil
}

// sequences implements port.Sequences with counter rows: the increment
// locks the row until the transaction ends, so concurrent transactions take
// numbers one after the other and a rolled-back number is reused.
type sequences struct{ t *txState }

func (s sequences) Next(ctx context.Context, org core.OrganizationID, k port.SequenceKey) (int64, error) {
	if org.IsZero() || k.Name == "" {
		return 0, fmt.Errorf("sequence %s: %w", k, core.ErrInvalidValue)
	}
	q, err := s.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	v, err := q.NextCounter(ctx, db.NextCounterParams{OrganizationID: org.UUID(), Name: k.Name.String(), Scope: k.Scope})
	if err != nil {
		return 0, fmt.Errorf("sequence %s: %w", k, mapErr(err))
	}
	return v, nil
}

// jobEnqueuer implements port.JobEnqueuer in the transaction: with the
// store's [JobInserter] when one is set, otherwise with the outbox job_outbox,
// whose committed rows a relay takes with [Store.TakeJobs].
type jobEnqueuer struct{ t *txState }

func (j jobEnqueuer) Enqueue(ctx context.Context, jobs ...port.Job) error {
	if len(jobs) == 0 {
		return nil
	}
	for _, job := range jobs {
		if job.Kind == "" || !job.Queue.Valid() || job.OrganizationID.IsZero() || job.MaxAttempts < 0 {
			return fmt.Errorf("enqueue %q: %w", job.Kind, core.ErrInvalidValue)
		}
	}
	if j.t.store.jobs != nil {
		if err := j.t.store.jobs(ctx, j.t.tx, jobs); err != nil {
			return fmt.Errorf("enqueue: %w", err)
		}
		return nil
	}
	for _, job := range jobs {
		var runAt *time.Time
		if !job.RunAt.IsZero() {
			runAt = optUTC(&job.RunAt)
		}
		// A duplicate unique key is skipped by ON CONFLICT DO NOTHING.
		if _, err := db.New(j.t.tx).InsertJob(ctx, db.InsertJobParams{
			OrganizationID: job.OrganizationID.UUID(),
			Kind:           job.Kind,
			Queue:          string(job.Queue),
			Args:           slices.Clone(job.Args),
			RunAt:          runAt,
			UniqueKey:      optString(job.UniqueKey),
			MaxAttempts:    int32(job.MaxAttempts), // small retry bound
		}); err != nil {
			return fmt.Errorf("enqueue %q: %w", job.Kind, mapErr(err))
		}
	}
	return nil
}

func jobOf(r db.JobOutbox) port.Job {
	j := port.Job{
		Kind:           r.Kind,
		Queue:          port.Queue(r.Queue),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		Args:           rawJSON(r.Args),
		UniqueKey:      deref(r.UniqueKey),
		MaxAttempts:    int(r.MaxAttempts),
	}
	if r.RunAt != nil {
		j.RunAt = r.RunAt.UTC()
	}
	return j
}

// Jobs returns the committed jobs still in the outbox, oldest first.
func (s *Store) Jobs(ctx context.Context) ([]port.Job, error) {
	rows, err := db.New(s.pool).ListJobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: list jobs: %w", mapErr(err))
	}
	out := make([]port.Job, len(rows))
	for i, r := range rows {
		out[i] = jobOf(r)
	}
	return out, nil
}

// TakeJobs removes up to limit committed jobs from the outbox, oldest first,
// and returns them; rows being taken by a concurrent caller are skipped. The
// relay of libs/jobs calls it in a transaction that also inserts the jobs
// into the queue, so a job leaves the outbox exactly when it enters the
// queue.
func (s *Store) TakeJobs(ctx context.Context, limit int) ([]port.Job, error) {
	var out []port.Job
	err := s.InTx(ctx, func(ctx context.Context, _ port.Repos) error {
		t := ctx.Value(txKey{}).(*txState)
		rows, err := db.New(t.tx).TakeJobs(ctx, int32(min(limit, core.MaxPageSize))) // bounded
		if err != nil {
			return fmt.Errorf("store: take jobs: %w", mapErr(err))
		}
		slices.SortFunc(rows, func(a, b db.JobOutbox) int { return cmp.Compare(a.ID, b.ID) })
		out = make([]port.Job, len(rows))
		for i, r := range rows {
			out[i] = jobOf(r)
		}
		return nil
	})
	return out, err
}
