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
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/jobs"
	"github.com/tanvoxyz/tanvo/libs/webhooks"
)

// jobHandlers are the services whose jobs the worker role runs.
type jobHandlers struct {
	webhooks *webhooks.Dispatcher
}

// newRegistry registers the job kinds this server can run. Only kinds
// whose service is wired are registered; the periodic schedule of
// libs/jobs leaves out the clock kinds without a handler.
func newRegistry(h jobHandlers) (*jobs.Registry, error) {
	reg := jobs.NewRegistry()
	d := h.webhooks
	if err := jobs.Register(reg, jobs.Kind{Name: webhooks.KindFanOut},
		jobs.HandlerFunc[webhooks.EventArgs](func(ctx context.Context, j *jobs.Job[webhooks.EventArgs]) error {
			return d.FanOut(ctx, j.OrganizationID, j.Args)
		})); err != nil {
		return nil, err
	}
	// A delivery attempt that fails is recorded and its next attempt is
	// enqueued by the dispatcher, with the backoff of the delivery; the
	// job itself fails only when the worker stops or the database fails.
	if err := jobs.Register(reg, jobs.Kind{Name: webhooks.KindDeliver},
		jobs.HandlerFunc[webhooks.DeliveryArgs](func(ctx context.Context, j *jobs.Job[webhooks.DeliveryArgs]) error {
			return d.Deliver(ctx, j.OrganizationID, j.Args)
		})); err != nil {
		return nil, err
	}
	// Webhook retention is a bounded maintenance delete with no
	// per-organization work, so it runs in the clock handler (see
	// jobs.Periodic).
	if err := jobs.Register(reg, jobs.Kind{Name: jobs.KindCleanWebhooks},
		jobs.HandlerFunc[struct{}](func(ctx context.Context, _ *jobs.Job[struct{}]) error {
			n, err := d.Purge(ctx)
			if err != nil {
				return err
			}
			slog.InfoContext(ctx, "webhooks: purged deliveries", "count", n)
			return nil
		})); err != nil {
		return nil, err
	}
	return reg, nil
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

// txCaptureKey marks a context whose store transaction is captured by the
// job inserter (see [captureTx]).
type txCaptureKey struct{}

// jobInserter is the store's JobInserter: it inserts the jobs with the
// River client in the store's transaction, so that they commit with it.
// An empty enqueue on a context made by [captureTx] hands the
// transaction to the caller instead.
func jobInserter(client func() *jobs.Client) func(ctx context.Context, tx pgx.Tx, js []port.Job) error {
	return func(ctx context.Context, tx pgx.Tx, js []port.Job) error {
		if len(js) == 0 {
			if p, ok := ctx.Value(txCaptureKey{}).(*pgx.Tx); ok {
				*p = tx
			}
			return nil
		}
		c := client()
		if c == nil {
			return fmt.Errorf("jobs: enqueue %s before the job client is ready: %w", js[0].Kind, core.ErrInvalidValue)
		}
		return c.InsertTx(ctx, tx, js)
	}
}

// captureTx returns the pgx transaction of the store transaction r
// belongs to. The store does not expose it; an empty enqueue through the
// store's job inserter reveals it.
func captureTx(ctx context.Context, r port.Repos) (pgx.Tx, error) {
	var tx pgx.Tx
	if err := r.Jobs.Enqueue(context.WithValue(ctx, txCaptureKey{}, &tx)); err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, fmt.Errorf("store: transaction not available: %w", core.ErrInvalidValue)
	}
	return tx, nil
}
