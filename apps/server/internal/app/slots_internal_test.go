package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/riverqueue/river/rivertype"

	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/jobs"
)

// fakeSlots hands out the free slots of each name and counts the ones
// taken and released.
type fakeSlots struct {
	free            map[string]int
	taken, released int
}

func (s *fakeSlots) AcquireSlot(_ context.Context, name string, limit int) (func(), bool, error) {
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
// organization) and snooze without one.
func TestSyncConcurrency(t *testing.T) {
	busy, quiet := core.NewID[core.OrganizationID](core.UUIDv7Generator{}), core.NewID[core.OrganizationID](core.UUIDv7Generator{})
	tests := []struct {
		name            string
		kind            string
		org             core.OrganizationID
		snoozed         bool
		taken, released int
	}{
		{name: "slot free", kind: kernel.JobSyncInvoice, org: quiet, taken: 1, released: 1},
		{name: "no slot", kind: kernel.JobSyncContact, org: busy, snoozed: true},
		{name: "not limited", kind: kernel.JobDeployCRM, org: busy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slots := &fakeSlots{free: map[string]int{integrations.ConcurrencySlotOf(quiet): 1}}
			w := &integrationsWiring{svc: integrations.New(&kernel.Deps{}, nil), slots: slots}
			// Args that do not decode end the job before any repository.
			err := w.run(t.Context(), &jobs.Job[json.RawMessage]{Kind: tt.kind, OrganizationID: tt.org, Args: json.RawMessage(`"bad"`)})
			var snooze *rivertype.JobSnoozeError
			if got := errors.As(err, &snooze); got != tt.snoozed {
				t.Errorf("snoozed got = %v (%v), want = %v", got, err, tt.snoozed)
			}
			if slots.taken != tt.taken || slots.released != tt.released {
				t.Errorf("slots got = %d taken, %d released, want = %d, %d", slots.taken, slots.released, tt.taken, tt.released)
			}
		})
	}
}
