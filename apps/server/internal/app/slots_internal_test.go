package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river/rivertype"

	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/jobs"
)

// fakeSlots hands out the free slots of each name and counts the ones
// taken and released; with err set, every acquisition fails.
type fakeSlots struct {
	free            map[string]int
	err             error
	taken, released int
}

func (s *fakeSlots) AcquireSlot(_ context.Context, name string, limit int) (func(), bool, error) {
	if s.err != nil {
		return nil, false, s.err
	}
	if limit != integrations.ConcurrencyLimit || s.free[name] == 0 {
		return nil, false, nil
	}
	s.free[name]--
	s.taken++
	return func() { s.released++ }, true, nil
}

// TestSyncConcurrency checks that the sync jobs of an organization share
// its concurrency slots (lago config/initializers/throttling.rb:10,
// app/jobs/concerns/concurrency_throttlable.rb, shared there by every
// organization) and snooze without one, longer and with a warning when
// the slots cannot be reached.
func TestSyncConcurrency(t *testing.T) {
	busy, quiet := core.NewID[core.OrganizationID](core.UUIDv7Generator{}), core.NewID[core.OrganizationID](core.UUIDv7Generator{})
	tests := []struct {
		name            string
		kind            string
		org             core.OrganizationID
		err             error
		snooze          time.Duration
		taken, released int
	}{
		{name: "slot free", kind: kernel.JobSyncInvoice, org: quiet, taken: 1, released: 1},
		{name: "no slot", kind: kernel.JobSyncContact, org: busy, snooze: throttledSnooze},
		{name: "slots unreachable", kind: kernel.JobSyncInvoice, org: quiet, err: errors.New("connection refused"), snooze: slotErrorSnooze},
		{name: "not limited", kind: kernel.JobDeployCRM, org: busy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slots := &fakeSlots{free: map[string]int{integrations.ConcurrencySlotOf(quiet): 1}, err: tt.err}
			var logs bytes.Buffer
			w := &integrationsWiring{svc: integrations.New(&kernel.Deps{}, nil), slots: slots, log: slog.New(slog.NewTextHandler(&logs, nil))}
			// Args that do not decode end the job before any repository.
			err := w.run(t.Context(), &jobs.Job[json.RawMessage]{Kind: tt.kind, OrganizationID: tt.org, Args: json.RawMessage(`"bad"`)})
			var snooze *rivertype.JobSnoozeError
			var got time.Duration
			if errors.As(err, &snooze) {
				got = snooze.Duration
			}
			if got != tt.snooze {
				t.Errorf("snooze got = %v (%v), want = %v", got, err, tt.snooze)
			}
			if warned := strings.Contains(logs.String(), "level=WARN"); warned != (tt.err != nil) {
				t.Errorf("warning got = %v (%q), want = %v", warned, logs.String(), tt.err != nil)
			}
			if slots.taken != tt.taken || slots.released != tt.released {
				t.Errorf("slots got = %d taken, %d released, want = %d, %d", slots.taken, slots.released, tt.taken, tt.released)
			}
		})
	}
}
