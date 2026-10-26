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
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/jobs"
)

type fakeBarrier struct {
	reached bool
	err     error
	at      time.Time
}

func (f *fakeBarrier) Reached(_ context.Context, at time.Time) (bool, error) {
	f.at = at
	return f.reached, f.err
}

func TestIngestBarrierWait(t *testing.T) {
	t.Parallel()
	kafkaOrg, pgOrg := core.NewID[core.OrganizationID](core.UUIDv7Generator{}), core.NewID[core.OrganizationID](core.UUIDv7Generator{})
	at := time.Date(2026, 11, 1, 0, 10, 0, 0, time.UTC)
	args, _ := json.Marshal(kernel.BillSubscriptionsArgs{BillingAt: at})
	overrun, _ := noop.NewMeterProvider().Meter("").Int64Counter("x")
	tests := []struct {
		name    string
		kind    string
		org     core.OrganizationID
		reached bool
		err     error
		now     time.Time
		snooze  bool
		logged  string
	}{
		{name: "reached", kind: kernel.JobBillSubscriptions, org: kafkaOrg, reached: true, now: at.Add(time.Minute)},
		{name: "not reached", kind: kernel.JobBillSubscriptions, org: kafkaOrg, now: at.Add(time.Minute), snooze: true},
		{name: "barrier failing", kind: kernel.JobBillSubscriptions, org: kafkaOrg, err: errors.New("no broker"), now: at.Add(time.Minute), snooze: true, logged: "level=WARN"},
		{name: "past the maximum wait", kind: kernel.JobBillSubscriptions, org: kafkaOrg, now: at.Add(time.Hour), logged: "level=ERROR"},
		{name: "postgres mode", kind: kernel.JobBillSubscriptions, org: pgOrg, now: at.Add(time.Minute)},
		{name: "another kind", kind: kernel.JobBillSubscription, org: kafkaOrg, now: at.Add(time.Minute)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			fb := &fakeBarrier{reached: tt.reached, err: tt.err}
			b := &ingestBarrier{
				barrier: fb,
				modeOf: func(o core.OrganizationID) ingest.Mode {
					if o == kafkaOrg {
						return ingest.ModeKafka
					}
					return ingest.ModePostgres
				},
				clock: core.FixedClock(tt.now), maxWait: time.Hour, overrun: overrun,
				log: slog.New(slog.NewTextHandler(&logs, nil)),
			}
			err := b.wait(t.Context(), &jobs.Job[json.RawMessage]{Kind: tt.kind, OrganizationID: tt.org, Args: args})
			var snooze *rivertype.JobSnoozeError
			if got := errors.As(err, &snooze); got != tt.snooze || (!tt.snooze && err != nil) {
				t.Errorf("wait() got = %v, want snooze = %v", err, tt.snooze)
			}
			if tt.snooze && snooze.Duration != barrierSnooze {
				t.Errorf("snooze got = %v, want = %v", snooze.Duration, barrierSnooze)
			}
			if tt.logged == "" && logs.Len() > 0 || tt.logged != "" && !strings.Contains(logs.String(), tt.logged) {
				t.Errorf("logs got = %q, want = %q", logs.String(), tt.logged)
			}
			if checked := !fb.at.IsZero(); checked != (tt.org == kafkaOrg && tt.kind == kernel.JobBillSubscriptions) {
				t.Errorf("barrier checked got = %v at %v", checked, fb.at)
			} else if checked && !fb.at.Equal(at) {
				t.Errorf("barrier instant got = %v, want = %v", fb.at, at)
			}
		})
	}
	var none *ingestBarrier
	if err := none.wait(t.Context(), &jobs.Job[json.RawMessage]{Kind: kernel.JobBillSubscriptions, OrganizationID: kafkaOrg, Args: args}); err != nil {
		t.Errorf("wait() without a barrier got = %v, want = nil", err)
	}
}
