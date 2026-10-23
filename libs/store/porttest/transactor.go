package porttest

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

var errAbort = errors.New("abort")

func testTransactor(t *testing.T, h Harness) {
	t.Run("commit and rollback", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		o := organization.New(core.NewID[core.OrganizationID](e.ids), "Acme", now)
		err := e.txErr(func(ctx context.Context, r port.Repos) error {
			if err := r.Organizations.Create(ctx, o); err != nil {
				return err
			}
			if _, err := r.Organizations.Get(ctx, o.ID); err != nil {
				t.Errorf("Get() inside the transaction error got = %v, want = nil", err)
			}
			return errAbort
		})
		wantErr(t, "InTx()", err, errAbort)
		e.tx(func(ctx context.Context, r port.Repos) error {
			_, err := r.Organizations.Get(ctx, o.ID)
			wantErr(t, "Get() after rollback", err, core.ErrNotFound)
			return r.Organizations.Create(ctx, o)
		})
		e.tx(func(ctx context.Context, r port.Repos) error {
			got, err := r.Organizations.Get(ctx, o.ID)
			noErr(t, "Get() after commit", err)
			equal(t, "Get() after commit", got, o)
			return nil
		})
	})

	t.Run("panic rolls back", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		o := organization.New(core.NewID[core.OrganizationID](e.ids), "Acme", now)
		func() {
			defer func() {
				if p := recover(); p != "boom" {
					t.Errorf("recover() got = %v, want = boom", p)
				}
			}()
			_ = e.txErr(func(ctx context.Context, r port.Repos) error {
				if err := r.Organizations.Create(ctx, o); err != nil {
					return err
				}
				panic("boom")
			})
		}()
		e.tx(func(ctx context.Context, r port.Repos) error {
			_, err := r.Organizations.Get(ctx, o.ID)
			wantErr(t, "Get() after panic", err, core.ErrNotFound)
			return nil
		})
	})

	t.Run("nested transaction joins the outer one", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		o := organization.New(core.NewID[core.OrganizationID](e.ids), "Acme", now)
		err := e.txErr(func(ctx context.Context, _ port.Repos) error {
			if err := e.tr.InTx(ctx, func(ctx context.Context, r port.Repos) error {
				return r.Organizations.Create(ctx, o)
			}); err != nil {
				return err
			}
			if err := e.tr.InTx(ctx, func(ctx context.Context, r port.Repos) error {
				_, err := r.Organizations.Get(ctx, o.ID)
				return err
			}); err != nil {
				t.Errorf("nested Get() error got = %v, want = nil", err)
			}
			return errAbort
		})
		wantErr(t, "InTx()", err, errAbort)
		e.tx(func(ctx context.Context, r port.Repos) error {
			_, err := r.Organizations.Get(ctx, o.ID)
			wantErr(t, "Get() after outer rollback", err, core.ErrNotFound)
			return nil
		})
	})

	t.Run("a conflict keeps the transaction usable", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a := organization.New(core.NewID[core.OrganizationID](e.ids), "Acme", now)
		b := organization.New(core.NewID[core.OrganizationID](e.ids), "Acme", now)
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create()", r.Organizations.Create(ctx, a))
			wantErr(t, "Create() with a taken slug", r.Organizations.Create(ctx, b), core.ErrConflict)
			b.Slug = "globex"
			noErr(t, "Create() after the conflict", r.Organizations.Create(ctx, b))
			b.Slug = a.Slug
			wantErr(t, "Update() to a taken slug", r.Organizations.Update(ctx, b), core.ErrConflict)
			return nil
		})
		e.tx(func(ctx context.Context, r port.Repos) error {
			got, err := r.Organizations.Get(ctx, b.ID)
			noErr(t, "Get()", err)
			if got.Slug != "globex" {
				t.Errorf("Slug got = %q, want = %q", got.Slug, "globex")
			}
			return nil
		})
	})

	t.Run("sequences", func(t *testing.T) { t.Parallel(); testSequences(t, h) })
	t.Run("locks", func(t *testing.T) { t.Parallel(); testLocks(t, h) })
	t.Run("jobs", func(t *testing.T) { t.Parallel(); testJobs(t, h) })
}

func testSequences(t *testing.T, h Harness) {
	e := newEnv(t, h)
	a, b := e.twoOrgs()
	inv := port.SequenceKey{Name: port.SequenceInvoiceOrganization}
	cust := port.SequenceKey{Name: port.SequenceInvoiceCustomer, Scope: uuid.Must(uuid.NewV7())}
	next := func(org core.OrganizationID, k port.SequenceKey) int64 {
		t.Helper()
		var v int64
		e.tx(func(ctx context.Context, r port.Repos) (err error) {
			v, err = r.Sequences.Next(ctx, org, k)
			return err
		})
		return v
	}
	for want := int64(1); want <= 3; want++ {
		if got := next(a.OrganizationID, inv); got != want {
			t.Errorf("Next(%s) got = %d, want = %d", inv, got, want)
		}
	}
	if got := next(b.OrganizationID, inv); got != 1 {
		t.Errorf("Next(%s) of another organization got = %d, want = 1", inv, got)
	}
	if got := next(a.OrganizationID, cust); got != 1 {
		t.Errorf("Next(%s) got = %d, want = 1", cust, got)
	}

	// A number taken by a rolled-back transaction is handed out again.
	err := e.txErr(func(ctx context.Context, r port.Repos) error {
		v, err := r.Sequences.Next(ctx, a.OrganizationID, inv)
		if err != nil {
			return err
		}
		if v != 4 {
			t.Errorf("Next() in the rolled-back transaction got = %d, want = 4", v)
		}
		v, err = r.Sequences.Next(ctx, a.OrganizationID, inv)
		if err != nil {
			return err
		}
		if v != 5 {
			t.Errorf("second Next() in the transaction got = %d, want = 5", v)
		}
		return errAbort
	})
	wantErr(t, "InTx()", err, errAbort)
	if got := next(a.OrganizationID, inv); got != 4 {
		t.Errorf("Next() after rollback got = %d, want = 4", got)
	}

	e.tx(func(ctx context.Context, r port.Repos) error {
		_, err := r.Sequences.Next(ctx, core.OrganizationID{}, inv)
		wantErr(t, "Next() without organization", err, core.ErrInvalidValue)
		return nil
	})

	// Concurrent transactions take distinct, gapless numbers.
	const n = 8
	k := port.SequenceKey{Name: port.SequenceQuote}
	got := make([]int64, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			if err := e.tr.InTx(t.Context(), func(ctx context.Context, r port.Repos) (err error) {
				got[i], err = r.Sequences.Next(ctx, a.OrganizationID, k)
				return err
			}); err != nil {
				t.Errorf("concurrent Next() error got = %v, want = nil", err)
			}
		})
	}
	wg.Wait()
	slices.Sort(got)
	for i, v := range got {
		if v != int64(i+1) {
			t.Errorf("concurrent Next() values got = %v, want = 1…%d", got, n)
			break
		}
	}
}

func testLocks(t *testing.T, h Harness) {
	e := newEnv(t, h)
	a, _ := e.twoOrgs()
	k := port.LockKey{Scope: "customer:prepaid_credits", ID: uuid.Must(uuid.NewV7())}
	e.tx(func(ctx context.Context, r port.Repos) error {
		noErr(t, "Lock()", r.Locks.Lock(ctx, a.OrganizationID, k))
		noErr(t, "Lock() again", r.Locks.Lock(ctx, a.OrganizationID, k))
		wantErr(t, "Lock() without organization", r.Locks.Lock(ctx, core.OrganizationID{}, k), core.ErrInvalidValue)
		wantErr(t, "Lock() without scope", r.Locks.Lock(ctx, a.OrganizationID, port.LockKey{ID: k.ID}), core.ErrInvalidValue)
		return nil
	})
	if !h.Concurrent {
		return
	}
	// A second transaction waits for the lock until the first one ends.
	held := make(chan struct{})
	release := make(chan struct{})
	var order []string
	var mu sync.Mutex
	record := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, s)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		err := e.tr.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
			if err := r.Locks.Lock(ctx, a.OrganizationID, k); err != nil {
				return err
			}
			close(held)
			<-release
			record("first done")
			return nil
		})
		if err != nil {
			t.Errorf("first InTx() error got = %v, want = nil", err)
		}
	})
	<-held
	wg.Go(func() {
		err := e.tr.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
			if err := r.Locks.Lock(ctx, a.OrganizationID, k); err != nil {
				return err
			}
			record("second locked")
			return nil
		})
		if err != nil {
			t.Errorf("second InTx() error got = %v, want = nil", err)
		}
	})
	// Another key of the same scope is not blocked.
	other := port.LockKey{Scope: k.Scope, ID: uuid.Must(uuid.NewV7())}
	e.tx(func(ctx context.Context, r port.Repos) error { return r.Locks.Lock(ctx, a.OrganizationID, other) })
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if !slices.Equal(order, []string{"first done", "second locked"}) {
		t.Errorf("lock order got = %v, want = [first done second locked]", order)
	}
}

func testJobs(t *testing.T, h Harness) {
	e := newEnv(t, h)
	a, _ := e.twoOrgs()
	org := a.OrganizationID
	job := func(kind, key string) port.Job {
		return port.Job{Kind: kind, Queue: port.QueueBilling, OrganizationID: org, Args: json.RawMessage(`{"id":"x"}`), UniqueKey: key}
	}
	err := e.txErr(func(ctx context.Context, r port.Repos) error {
		if err := r.Jobs.Enqueue(ctx, job("rolled.back", "")); err != nil {
			return err
		}
		return errAbort
	})
	wantErr(t, "InTx()", err, errAbort)
	if got := e.jobs(); len(got) != 0 {
		t.Errorf("jobs after rollback got = %v, want = none", got)
	}

	delayed := job("invoice.finalize", "")
	delayed.Queue = port.QueueDocuments
	delayed.RunAt = at(time.Hour)
	delayed.MaxAttempts = 3
	e.tx(func(ctx context.Context, r port.Repos) error {
		noErr(t, "Enqueue()", r.Jobs.Enqueue(ctx, job("a", "k1"), delayed))
		// A unique key already pending in this transaction is skipped.
		noErr(t, "Enqueue() duplicate", r.Jobs.Enqueue(ctx, job("a", "k1")))
		// The same key on another kind is distinct.
		noErr(t, "Enqueue() other kind", r.Jobs.Enqueue(ctx, job("b", "k1")))
		wantErr(t, "Enqueue() without kind", r.Jobs.Enqueue(ctx, job("", "")), core.ErrInvalidValue)
		bad := job("c", "")
		bad.Queue = "nope"
		wantErr(t, "Enqueue() with an unknown queue", r.Jobs.Enqueue(ctx, bad), core.ErrInvalidValue)
		bad = job("c", "")
		bad.OrganizationID = core.OrganizationID{}
		wantErr(t, "Enqueue() without organization", r.Jobs.Enqueue(ctx, bad), core.ErrInvalidValue)
		return nil
	})
	// A unique key pending from a committed transaction is skipped too.
	e.tx(func(ctx context.Context, r port.Repos) error { return r.Jobs.Enqueue(ctx, job("a", "k1")) })
	equal(t, "jobs", e.jobs(), []port.Job{job("a", "k1"), delayed, job("b", "k1")})
}
