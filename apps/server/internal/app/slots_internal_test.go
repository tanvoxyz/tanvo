package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/riverqueue/river/rivertype"

	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/jobs"
)

// fakeSlots hands out free slots and counts the ones taken and released.
type fakeSlots struct {
	free, taken, released int
}

func (s *fakeSlots) AcquireSlot(_ context.Context, name string, limit int) (func(), bool, error) {
	if name != integrations.ConcurrencySlot || limit != integrations.ConcurrencyLimit || s.free == 0 {
		return nil, false, nil
	}
	s.free--
	s.taken++
	return func() { s.released++ }, true, nil
}

// TestSyncConcurrency checks that the sync jobs share the deployment's
// concurrency slots (lago config/initializers/throttling.rb:10,
// app/jobs/concerns/concurrency_throttlable.rb) and snooze without one.
func TestSyncConcurrency(t *testing.T) {
	tests := []struct {
		name            string
		kind            string
		free            int
		snoozed         bool
		taken, released int
	}{
		{name: "slot free", kind: kernel.JobSyncInvoice, free: 1, taken: 1, released: 1},
		{name: "no slot", kind: kernel.JobSyncContact, snoozed: true},
		{name: "not limited", kind: kernel.JobDeployCRM},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slots := &fakeSlots{free: tt.free}
			w := &integrationsWiring{svc: integrations.New(&kernel.Deps{}, nil), slots: slots}
			// Args that do not decode end the job before any repository.
			err := w.run(t.Context(), &jobs.Job[json.RawMessage]{Kind: tt.kind, Args: json.RawMessage(`"bad"`)})
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
