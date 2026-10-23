package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/riverqueue/rivercontrib/otelriver"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/apps/server/internal/logctx"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/jobs"
	"github.com/tanvoxyz/tanvo/libs/webhooks"
)

// jobHandlers are the services whose jobs the worker role runs.
type jobHandlers struct {
	webhooks *webhooks.Dispatcher
	tx       port.Transactor
	stores   *EventStores
	mp       metric.MeterProvider
}

// sweepLimit is the number of due webhook deliveries a sweep enqueues.
const sweepLimit = 1000

// newRegistry registers the job kinds this server can run. Only kinds
// whose service is wired are registered; the periodic schedule of
// libs/jobs leaves out the clock kinds without a handler.
func newRegistry(h jobHandlers) (*jobs.Registry, error) {
	reg := jobs.NewRegistry()
	for _, register := range []func(*jobs.Registry) error{h.registerWebhooks, h.registerAPIKeyUsage, h.registerUsageBuckets} {
		if err := register(reg); err != nil {
			return nil, err
		}
	}
	return reg, nil
}

// registerWebhooks registers the outgoing webhook kinds: the fan-out and
// the attempts, the retention clock job and its per-organization purges,
// and the sweep of due deliveries without a job.
func (h jobHandlers) registerWebhooks(reg *jobs.Registry) error {
	d := h.webhooks
	if err := jobs.Register(reg, jobs.Kind{Name: webhooks.KindFanOut},
		jobs.HandlerFunc[webhooks.EventArgs](func(ctx context.Context, j *jobs.Job[webhooks.EventArgs]) error {
			return d.FanOut(ctx, j.OrganizationID, j.Args)
		})); err != nil {
		return err
	}
	// A delivery attempt that fails is recorded and its next attempt is
	// enqueued by the dispatcher, with the backoff of the delivery; the
	// job itself fails only when the worker stops or the database fails.
	if err := jobs.Register(reg, jobs.Kind{Name: webhooks.KindDeliver},
		jobs.HandlerFunc[webhooks.DeliveryArgs](func(ctx context.Context, j *jobs.Job[webhooks.DeliveryArgs]) error {
			return d.Deliver(ctx, j.OrganizationID, j.Args)
		})); err != nil {
		return err
	}
	// Retention runs per organization, under its row-level security
	// scope: the clock job dispatches the purges.
	if err := jobs.Register(reg, jobs.Kind{Name: jobs.KindCleanWebhooks},
		jobs.HandlerFunc[struct{}](func(ctx context.Context, j *jobs.Job[struct{}]) error {
			n, err := d.DispatchPurges(ctx, j.ScheduledAt)
			if err != nil {
				return err
			}
			slog.InfoContext(ctx, "webhooks: dispatched purges", "organizations", n)
			return nil
		})); err != nil {
		return err
	}
	if err := jobs.Register(reg, jobs.Kind{Name: webhooks.KindPurge},
		jobs.HandlerFunc[webhooks.PurgeArgs](func(ctx context.Context, j *jobs.Job[webhooks.PurgeArgs]) error {
			n, err := d.Purge(ctx, j.OrganizationID, j.Args)
			if err != nil {
				return err
			}
			slog.InfoContext(ctx, "webhooks: purged deliveries", "count", n)
			return nil
		})); err != nil {
		return err
	}
	return jobs.Register(reg, jobs.Kind{Name: jobs.KindSweepWebhooks},
		jobs.HandlerFunc[struct{}](func(ctx context.Context, _ *jobs.Job[struct{}]) error {
			n, err := d.Sweep(ctx, sweepLimit)
			if err != nil {
				return err
			}
			if n > 0 {
				slog.WarnContext(ctx, "webhooks: enqueued due deliveries without a job", "count", n)
			}
			return nil
		}))
}

// registerAPIKeyUsage registers the API key usage clock job and its
// per-organization jobs, which move the uses the API role recorded to the
// keys' last_used_at.
func (h jobHandlers) registerAPIKeyUsage(reg *jobs.Registry) error {
	if err := jobs.Register(reg, jobs.Kind{Name: jobs.KindTrackAPIKeyUsage},
		jobs.HandlerFunc[struct{}](func(ctx context.Context, _ *jobs.Job[struct{}]) error {
			_, err := auth.DispatchUsage(ctx, h.tx)
			return err
		})); err != nil {
		return err
	}
	return jobs.Register(reg, jobs.Kind{Name: auth.JobKindApplyAPIKeyUsage},
		jobs.HandlerFunc[struct{}](func(ctx context.Context, j *jobs.Job[struct{}]) error {
			_, err := auth.ApplyUsage(ctx, h.tx, j.OrganizationID)
			return err
		}))
}

// registerUsageBuckets registers the event stores' bucket maintenance:
// the ClickHouse repair, when ClickHouse is configured, and the bucket
// size report.
func (h jobHandlers) registerUsageBuckets(reg *jobs.Registry) error {
	if ch := h.stores.ClickHouse(); ch != nil {
		if err := jobs.Register(reg, jobs.Kind{Name: jobs.KindRepairUsageBuckets},
			jobs.HandlerFunc[struct{}](func(ctx context.Context, _ *jobs.Job[struct{}]) error {
				return ch.RepairRecentBuckets(ctx, clickhouse.RepairMargin)
			})); err != nil {
			return err
		}
	}
	report, err := newBucketStatsReport(h.stores, h.mp)
	if err != nil {
		return err
	}
	return jobs.Register(reg, jobs.Kind{Name: jobs.KindReportUsageBucketStats},
		jobs.HandlerFunc[struct{}](func(ctx context.Context, _ *jobs.Job[struct{}]) error {
			return report.run(ctx)
		}))
}

// newJobs creates the River client of the roles: a working client on
// the configured queues (with the periodic schedule unless disabled) for
// the worker role, an insert-only client otherwise.
func newJobs(pool *pgxpool.Pool, reg *jobs.Registry, cfg config.Config, roles config.Roles, log *slog.Logger, tp trace.TracerProvider, mp metric.MeterProvider) (*jobs.Client, error) {
	jc := jobs.Config{
		Schema:           cfg.JobsSchema,
		DisablePeriodic:  cfg.JobsDisablePeriodic,
		ClockConcurrency: cfg.JobsClockConcurrency,
		Logger:           log,
		Middleware: []rivertype.Middleware{
			otelriver.NewMiddleware(&otelriver.MiddlewareConfig{
				TracerProvider:         tp,
				MeterProvider:          mp,
				EnableSemanticMetrics:  true,
				EnableTracePropagation: true,
				DurationUnit:           "s",
			}),
			jobContext(),
		},
	}
	if roles.Has(config.RoleWorker) {
		jc.Work = cfg.Queues()
	}
	c, err := jobs.New(pool, reg, jc)
	if err != nil {
		return nil, fmt.Errorf("jobs: %w", err)
	}
	return c, nil
}

// jobContext puts the job's organization, id and kind on its span and on
// the logs of its work. The organization is read from the arguments
// envelope of libs/jobs ({"org": …}); periodic jobs have none.
func jobContext() rivertype.Middleware {
	return river.WorkerMiddlewareFunc(func(ctx context.Context, job *rivertype.JobRow, doInner func(context.Context) error) error {
		attrs := []slog.Attr{slog.String("job.id", strconv.FormatInt(job.ID, 10)), slog.String("job.kind", job.Kind)}
		var env struct {
			Org core.OrganizationID `json:"org"`
		}
		if json.Unmarshal(job.EncodedArgs, &env) == nil && !env.Org.IsZero() {
			attrs = append(attrs, slog.String(logctx.OrganizationKey, env.Org.String()))
			trace.SpanFromContext(ctx).SetAttributes(attribute.String(logctx.OrganizationKey, env.Org.String()))
		}
		return doInner(logctx.With(ctx, attrs...))
	})
}

// jobInserter is the store's JobInserter: it inserts the jobs with the
// River client in the store's transaction, so that they commit with it.
func jobInserter(client func() *jobs.Client) func(ctx context.Context, tx pgx.Tx, js []port.Job) error {
	return func(ctx context.Context, tx pgx.Tx, js []port.Job) error {
		if len(js) == 0 {
			return nil
		}
		c := client()
		if c == nil {
			return fmt.Errorf("jobs: enqueue %s before the job client is ready: %w", js[0].Kind, core.ErrInvalidValue)
		}
		return c.InsertTx(ctx, tx, js)
	}
}
