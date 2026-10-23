package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store"
	"github.com/tanvoxyz/tanvo/libs/store/pgtest"
)

func TestJobInserter(t *testing.T) {
	db := pgtest.New(t, template)
	var got []port.Job
	inserter := func(ctx context.Context, tx pgx.Tx, jobs []port.Job) error {
		// The jobs are written in the store's transaction.
		if _, err := tx.Exec(ctx, "CREATE TEMP TABLE IF NOT EXISTS seen (kind text) ON COMMIT DROP"); err != nil {
			return err
		}
		got = append(got, jobs...)
		return nil
	}
	s, err := store.Open(t.Context(), db.AdminDSN, store.WithJobInserter(inserter))
	if err != nil {
		t.Fatalf("Open() error got = %v, want = nil", err)
	}
	t.Cleanup(s.Pool().Close)

	org := core.MustParseID[core.OrganizationID](uuid.Must(uuid.NewV7()).String())
	job := port.Job{Kind: "invoice.finalize", Queue: port.QueueBilling, OrganizationID: org, UniqueKey: "inv-1"}
	if err := s.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
		return r.Jobs.Enqueue(ctx, job)
	}); err != nil {
		t.Fatalf("InTx() error got = %v, want = nil", err)
	}
	if len(got) != 1 || got[0].Kind != job.Kind {
		t.Errorf("inserted got = %+v, want = [%s]", got, job.Kind)
	}
	// The outbox is not used.
	if out, err := s.TakeJobs(t.Context(), 10); err != nil || len(out) != 0 {
		t.Errorf("TakeJobs() got = %v, %v, want = none", out, err)
	}
	// Invalid jobs are refused before the inserter runs.
	err = s.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
		return r.Jobs.Enqueue(ctx, port.Job{Kind: "x", Queue: port.QueueBilling})
	})
	if !errors.Is(err, core.ErrInvalidValue) || len(got) != 1 {
		t.Errorf("invalid job: got = %v (%d inserted), want = ErrInvalidValue", err, len(got))
	}
}

// TestQueueClock checks that the store's job enqueuer reads the database
// clock the job queue schedules by, advancing within a transaction
// (clock_timestamp(), not the transaction start).
func TestQueueClock(t *testing.T) {
	db := pgtest.New(t, template)
	s, err := store.Open(t.Context(), db.AdminDSN)
	if err != nil {
		t.Fatalf("Open() error got = %v, want = nil", err)
	}
	t.Cleanup(s.Pool().Close)
	before := time.Now().Add(-time.Minute)
	err = s.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
		qc, ok := r.Jobs.(port.QueueClock)
		if !ok {
			t.Fatalf("Jobs got = %T, want = a port.QueueClock", r.Jobs)
		}
		first, err := qc.QueueNow(ctx)
		if err != nil {
			return err
		}
		if _, err := s.Pool().Exec(ctx, "SELECT pg_sleep(0.01)"); err != nil {
			return err
		}
		second, err := qc.QueueNow(ctx)
		if err != nil {
			return err
		}
		if first.Location() != time.UTC || first.Before(before) || first.After(time.Now().Add(time.Minute)) {
			t.Errorf("QueueNow() got = %v, want = about now in UTC", first)
		}
		if !second.After(first) {
			t.Errorf("QueueNow() second got = %v, want = after %v", second, first)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InTx() error got = %v, want = nil", err)
	}
}
