package store_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/store"
)

func TestAcquireSlot(t *testing.T) {
	s := open(t, true)
	ctx := t.Context()
	var held []func()
	for i := range 2 {
		release, ok, err := s.AcquireSlot(ctx, "integrations", 2)
		if err != nil || !ok {
			t.Fatalf("slot %d: got = %v, %v, want = taken", i, ok, err)
		}
		held = append(held, release)
	}
	if _, ok, err := s.AcquireSlot(ctx, "integrations", 2); err != nil || ok {
		t.Fatalf("third slot: got = %v, %v, want = not taken", ok, err)
	}
	if release, ok, err := s.AcquireSlot(ctx, "other", 1); err != nil || !ok {
		t.Fatalf("other name: got = %v, %v, want = taken", ok, err)
	} else {
		release()
	}
	held[0]()
	release, ok, err := s.AcquireSlot(ctx, "integrations", 2)
	if err != nil || !ok {
		t.Fatalf("after release: got = %v, %v, want = taken", ok, err)
	}
	release()
	held[1]()
	if _, _, err := s.AcquireSlot(ctx, "integrations", 0); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("limit 0: error got = %v, want = %v", err, core.ErrInvalidValue)
	}
}

// TestAcquireSlotSessionPool checks that a slot holds a connection of the
// session pool, not of the store's pool, when the store has one.
func TestAcquireSlotSessionPool(t *testing.T) {
	main := open(t, true)
	ctx := t.Context()
	session, err := pgxpool.New(ctx, main.Pool().Config().ConnString())
	if err != nil {
		t.Fatalf("pgxpool.New() error got = %v, want = nil", err)
	}
	t.Cleanup(session.Close)
	s := store.New(main.Pool(), store.WithSessionPool(session))
	release, ok, err := s.AcquireSlot(ctx, "integrations", 1)
	if err != nil || !ok {
		t.Fatalf("slot: got = %v, %v, want = taken", ok, err)
	}
	if got := session.Stat().AcquiredConns(); got != 1 {
		t.Errorf("session pool acquired conns got = %d, want = 1", got)
	}
	if got := main.Pool().Stat().AcquiredConns(); got != 0 {
		t.Errorf("store pool acquired conns got = %d, want = 0", got)
	}
	// The lock is the database's: a store without the session pool sees
	// the slot taken.
	if _, ok, err := main.AcquireSlot(ctx, "integrations", 1); err != nil || ok {
		t.Errorf("slot through the store's pool: got = %v, %v, want = not taken", ok, err)
	}
	release()
	if got := session.Stat().AcquiredConns(); got != 0 {
		t.Errorf("session pool acquired conns after release got = %d, want = 0", got)
	}
}
