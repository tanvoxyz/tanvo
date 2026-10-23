package memstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

var (
	storeOrg = core.MustParseID[core.OrganizationID]("0190a0a0-0000-7000-8000-0000000000a1")
	storeNow = time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
)

func storeCreateOrg(ctx context.Context, r port.Repos) error {
	return r.Organizations.Create(ctx, organization.New(storeOrg, "Hooli", storeNow))
}

func TestStoreCommitAndRollback(t *testing.T) {
	ctx := context.Background()
	s := New()
	boom := errors.New("boom")

	err := s.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := storeCreateOrg(ctx, r); err != nil {
			return err
		}
		if _, err := r.Organizations.Get(ctx, storeOrg); err != nil {
			t.Errorf("Get inside the transaction got = %v, want = nil", err)
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx got = %v, want = %v", err, boom)
	}
	_ = s.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if _, err := r.Organizations.Get(ctx, storeOrg); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("Get after rollback got = %v, want = ErrNotFound", err)
		}
		return nil
	})

	if err := s.InTx(ctx, storeCreateOrg); err != nil {
		t.Fatal(err)
	}
	_ = s.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if _, err := r.Organizations.Get(ctx, storeOrg); err != nil {
			t.Errorf("Get after commit got = %v, want = nil", err)
		}
		return nil
	})
}

func TestStoreRollbackOnPanic(t *testing.T) {
	ctx := context.Background()
	s := New()
	func() {
		defer func() { _ = recover() }()
		_ = s.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			_ = storeCreateOrg(ctx, r)
			panic("boom")
		})
	}()
	// The mutex was released and nothing was committed.
	_ = s.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if _, err := r.Organizations.Get(ctx, storeOrg); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("Get after panic got = %v, want = ErrNotFound", err)
		}
		return nil
	})
}

func TestStoreNestedJoinsOuter(t *testing.T) {
	ctx := context.Background()
	s := New()
	boom := errors.New("boom")
	err := s.InTx(ctx, func(ctx context.Context, _ port.Repos) error {
		if err := s.InTx(ctx, storeCreateOrg); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx got = %v, want = %v", err, boom)
	}
	_ = s.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if _, err := r.Organizations.Get(ctx, storeOrg); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("nested write after outer rollback got = %v, want = ErrNotFound", err)
		}
		return nil
	})
}

func TestStoreSequences(t *testing.T) {
	ctx := context.Background()
	s := New()
	cust := uuid.MustParse("0190a0a0-0000-7000-8000-0000000000c1")
	k := port.SequenceKey{Name: port.SequenceInvoiceCustomer, Scope: cust}
	next := func() (n int64) {
		_ = s.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			var err error
			n, err = r.Sequences.Next(ctx, storeOrg, k)
			return err
		})
		return n
	}
	if got := next(); got != 1 {
		t.Errorf("Next got = %d, want = 1", got)
	}
	_ = s.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		_, _ = r.Sequences.Next(ctx, storeOrg, k)
		return errors.New("rollback")
	})
	if got := next(); got != 2 {
		t.Errorf("Next after a rolled back number got = %d, want = 2 (gapless)", got)
	}
	_ = s.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		n, err := r.Sequences.Next(ctx, storeOrg, port.SequenceKey{Name: port.SequenceCustomer})
		if err != nil || n != 1 {
			t.Errorf("Next(other key) got = %d, %v, want = 1, nil", n, err)
		}
		if _, err := r.Sequences.Next(ctx, core.OrganizationID{}, k); !errors.Is(err, core.ErrInvalidValue) {
			t.Errorf("Next(no org) got = %v, want = ErrInvalidValue", err)
		}
		return nil
	})
}

func TestStoreJobsAndLocks(t *testing.T) {
	ctx := context.Background()
	s := New()
	job := port.Job{Kind: "invoice.finalize", Queue: port.QueueBilling, OrganizationID: storeOrg, UniqueKey: "inv-1"}
	_ = s.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		_ = r.Jobs.Enqueue(ctx, job)
		return errors.New("rollback")
	})
	if got := len(s.Jobs()); got != 0 {
		t.Errorf("jobs after rollback got = %d, want = 0", got)
	}
	_ = s.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := r.Locks.Lock(ctx, storeOrg, port.SubscriptionLock(core.MustParseID[core.SubscriptionID]("0190a0a0-0000-7000-8000-0000000000d1"))); err != nil {
			t.Errorf("Lock got = %v, want = nil", err)
		}
		if err := r.Jobs.Enqueue(ctx, job, job); err != nil {
			t.Errorf("Enqueue got = %v, want = nil", err)
		}
		if err := r.Jobs.Enqueue(ctx, port.Job{Kind: "x", Queue: "nope", OrganizationID: storeOrg}); !errors.Is(err, core.ErrInvalidValue) {
			t.Errorf("Enqueue(bad queue) got = %v, want = ErrInvalidValue", err)
		}
		return nil
	})
	_ = s.InTx(ctx, func(ctx context.Context, r port.Repos) error { return r.Jobs.Enqueue(ctx, job) })
	if got := len(s.TakeJobs()); got != 1 {
		t.Errorf("unique jobs got = %d, want = 1", got)
	}
	if got := len(s.Jobs()); got != 0 {
		t.Errorf("jobs after TakeJobs got = %d, want = 0", got)
	}
}

func TestStoreEmit(t *testing.T) {
	ctx := context.Background()
	s := New()
	e := webhook.Event{
		ID:             core.MustParseID[core.AuditEventID]("0190a0a0-0000-7000-8000-0000000000e1"),
		OrganizationID: storeOrg,
		Type:           webhook.EventTypeValues()[0],
		ObjectType:     "invoice",
		ObjectID:       uuid.MustParse("0190a0a0-0000-7000-8000-0000000000e2"),
		Payload:        json.RawMessage(`{"id":"x"}`),
		OccurredAt:     storeNow,
	}
	if err := s.Emit(ctx, e); err != nil {
		t.Fatal(err)
	}
	jobs := s.Jobs()
	if len(jobs) != 1 || jobs[0].Kind != JobDeliverWebhookEvent || jobs[0].Queue != port.QueueWebhooks {
		t.Errorf("jobs got = %+v, want = one %s job", jobs, JobDeliverWebhookEvent)
	}
	_ = s.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if _, err := r.DomainEvents.Get(ctx, storeOrg, e.ID); err != nil {
			t.Errorf("recorded event got = %v, want = nil", err)
		}
		return nil
	})
	if err := s.Emit(ctx, webhook.Event{}); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("Emit(invalid) got = %v, want = ErrInvalidValue", err)
	}
}
