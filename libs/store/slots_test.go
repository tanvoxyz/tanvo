package store_test

import (
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
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
