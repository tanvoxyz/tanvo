package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel/metric"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/ingest/kafka"
	"github.com/tanvoxyz/tanvo/libs/jobs"
)

// barrierSnooze is how long a periodic billing waiting for the ingestion
// barrier snoozes between two checks.
const barrierSnooze = 15 * time.Second

// ingestBarrier holds the periodic billing of the organizations that
// ingest through Kafka until the events received before the billing
// instant are in the event store (architecture §7.1), for at most maxWait
// after the instant.
type ingestBarrier struct {
	barrier interface {
		Reached(ctx context.Context, at time.Time) (bool, error)
	}
	modeOf  func(core.OrganizationID) ingest.Mode
	clock   core.Clock
	maxWait time.Duration
	overrun metric.Int64Counter
	log     *slog.Logger
}

// newIngestBarrier returns the barrier of the periodic billing, nil when
// TANVO_BILLING_INGEST_BARRIER_MAX_WAIT is 0.
func newIngestBarrier(cfg config.Config, cl *kgo.Client, modeOf func(core.OrganizationID) ingest.Mode, clock core.Clock, mp metric.MeterProvider, log *slog.Logger) (*ingestBarrier, error) {
	if cfg.BillingIngestBarrierMaxWait == 0 {
		return nil, nil
	}
	b, err := kafka.NewBarrier(kafka.BarrierConfig{
		Admin: kadm.NewClient(cl), Topics: topics(cfg),
		EnrichGroup: group(cfg, "enrich"), WriteGroup: group(cfg, "write"),
		Now: clock.Now,
	})
	if err != nil {
		return nil, err
	}
	overrun, err := mp.Meter("github.com/tanvoxyz/tanvo/apps/server").Int64Counter("tanvo.billing.ingest_barrier.overruns",
		metric.WithUnit("{job}"), metric.WithDescription("Periodic billings run before the ingestion barrier was reached, at the end of its maximum wait."))
	if err != nil {
		return nil, fmt.Errorf("billing: ingest barrier: %w", err)
	}
	return &ingestBarrier{barrier: b, modeOf: modeOf, clock: clock, maxWait: cfg.BillingIngestBarrierMaxWait, overrun: overrun, log: log}, nil
}

// wait returns nil when the periodic billing j may run, and a snooze
// while the barrier at its billing instant is not reached. An error of
// the barrier counts as not reached.
func (b *ingestBarrier) wait(ctx context.Context, j *jobs.Job[json.RawMessage]) error {
	if b == nil || j.Kind != kernel.JobBillSubscriptions || b.modeOf(j.OrganizationID) != ingest.ModeKafka {
		return nil
	}
	var a kernel.BillSubscriptionsArgs
	if err := json.Unmarshal(j.Args, &a); err != nil || a.BillingAt.IsZero() {
		// The engine rejects the job.
		return nil
	}
	reached, err := b.barrier.Reached(ctx, a.BillingAt)
	if err != nil {
		b.log.WarnContext(ctx, "billing: ingest barrier", slog.String("organization_id", j.OrganizationID.String()), slog.Any("error", err))
	}
	if reached {
		return nil
	}
	if b.clock.Now().Before(a.BillingAt.Add(b.maxWait)) {
		return jobs.Snooze(barrierSnooze)
	}
	b.overrun.Add(ctx, 1)
	b.log.ErrorContext(ctx, "billing: ingest barrier not reached, billing anyway",
		slog.String("organization_id", j.OrganizationID.String()), slog.Time("billing_at", a.BillingAt), slog.Duration("max_wait", b.maxWait))
	return nil
}
