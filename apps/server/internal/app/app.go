package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/sync/errgroup"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/apps/server/internal/telemetry"
	"github.com/tanvoxyz/tanvo/libs/jobs"
	"github.com/tanvoxyz/tanvo/libs/store"
)

// scopeName is the instrumentation scope of the spans this package makes.
const scopeName = telemetry.ScopeName

// Options configure [New].
type Options struct {
	// Config is the validated configuration.
	Config config.Config
	// Roles are the roles to run.
	Roles config.Roles
	// Telemetry holds the providers and the logger.
	Telemetry *telemetry.Telemetry
	// Listener, when set, serves HTTP instead of listening on
	// TANVO_HTTP_ADDR.
	Listener net.Listener
}

// App is a wired server process.
type App struct {
	cfg   config.Config
	roles config.Roles
	tel   *telemetry.Telemetry
	log   *slog.Logger

	pool      *pgxpool.Pool
	ch        driver.Conn
	store     *store.Store
	jobs      *jobs.Client
	stores    *EventStores
	producer  *kgo.Client
	consumers *consumers
	api       *api
	health    *health
	server    *http.Server
	listener  net.Listener
}

// New wires the components of o.Roles. It applies the schemas when
// TANVO_MIGRATE_ON_START is set and checks them otherwise. Nothing runs
// until [App.Run].
func New(ctx context.Context, o Options) (_ *App, err error) {
	cfg, tel := o.Config, o.Telemetry
	if err := cfg.ValidateFor(o.Roles); err != nil {
		return nil, err
	}
	a := &App{cfg: cfg, roles: o.Roles, tel: tel, log: tel.Logger, health: &health{log: tel.Logger}, listener: o.Listener}
	defer func() {
		if err != nil {
			a.close()
		}
	}()
	tp, mp := tel.TracerProvider, tel.MeterProvider

	// PostgreSQL, ClickHouse and the schemas.
	if a.pool, err = openPool(ctx, cfg.DatabaseURL, poolOptions{MaxConns: cfg.DatabaseMaxConns, Name: "main"}, tp, mp); err != nil {
		return nil, err
	}
	if cfg.ClickHouseURL != "" {
		if a.ch, err = openClickHouse(ctx, cfg.ClickHouseURL); err != nil {
			return nil, err
		}
	}
	if cfg.MigrateOnStart {
		err = migrateAll(ctx, cfg, a.pool, a.ch)
	} else {
		err = checkSchemas(ctx, cfg, a.pool)
	}
	if err != nil {
		return nil, err
	}
	a.health.add("postgres", a.pool.Ping)
	if a.ch != nil {
		a.health.add("clickhouse", a.ch.Ping)
	}

	// The store, whose jobs commit with its transactions, the event
	// stores, and the jobs.
	sopts := []store.Option{store.WithJobInserter(jobInserter(func() *jobs.Client { return a.jobs }))}
	if cfg.DatabaseRowLevelSecurity {
		sopts = append(sopts, store.WithRowLevelSecurity())
	}
	a.store = store.New(a.pool, sopts...)
	modeOf, err := ingestModes(cfg)
	if err != nil {
		return nil, err
	}
	a.stores = newEventStores(a.pool, cfg.EventStoreSchema, a.ch, modeOf, tp)
	reg, err := newRegistry(jobHandlers{webhooks: newDispatcher(cfg, a.store, tp, mp), tx: a.store, stores: a.stores, mp: mp})
	if err != nil {
		return nil, err
	}
	composer, sender, err := newMailer(cfg, a.log, tp)
	if err != nil {
		return nil, err
	}
	if err := (mailJobs{tx: a.store, composer: composer, sender: sender}).register(reg); err != nil {
		return nil, err
	}
	if a.jobs, err = newJobs(a.pool, reg, cfg, o.Roles, a.log, tp, mp); err != nil {
		return nil, err
	}

	// Kafka.
	ktel := kafkaTelemetry{tp: tp, mp: mp, propagator: tel.Propagator}
	if (o.Roles.Has(config.RoleAPI) && cfg.KafkaUsed()) || o.Roles.Has(config.RoleConsumer) {
		if a.producer, err = newKafkaClient(cfg, ktel, ""); err != nil {
			return nil, err
		}
		if err := ensureTopics(ctx, cfg, a.producer); err != nil {
			return nil, err
		}
		a.health.add("kafka", a.producer.Ping)
	}
	if o.Roles.Has(config.RoleConsumer) {
		if a.consumers, err = newConsumers(cfg, a.store, a.stores.ClickHouse(), a.producer, ktel, a.log); err != nil {
			return nil, err
		}
	}

	// HTTP: health and metrics on every role, the API on the api role.
	mux := http.NewServeMux()
	a.health.mount(mux)
	if tel.Metrics != nil {
		mux.Handle("GET "+metricsPath, tel.Metrics)
	}
	if o.Roles.Has(config.RoleAPI) {
		var producer *kgo.Client
		if cfg.KafkaUsed() {
			producer = a.producer
		}
		svc, err := newIngestService(cfg, a.store, modeOf, producer)
		if err != nil {
			return nil, err
		}
		if a.api, err = newAPI(mux, apiDeps{tx: a.store, ingest: svc, maxBytes: cfg.HTTPMaxBodyBytes, trustedProxies: cfg.TrustedProxies, log: a.log, tp: tp, mp: mp}); err != nil {
			return nil, err
		}
	}
	handler := otelhttp.NewHandler(http.MaxBytesHandler(mux, cfg.HTTPMaxBodyBytes), "http",
		otelhttp.WithTracerProvider(tp),
		otelhttp.WithMeterProvider(mp),
		otelhttp.WithPropagators(tel.Propagator),
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != livenessPath && r.URL.Path != readinessPath && r.URL.Path != metricsPath
		}),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method }),
	)
	a.server = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		ErrorLog:          slog.NewLogLogger(a.log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	if a.listener == nil {
		var lc net.ListenConfig
		if a.listener, err = lc.Listen(ctx, "tcp", cfg.HTTPAddr); err != nil {
			return nil, fmt.Errorf("http: listen %s: %w", cfg.HTTPAddr, err)
		}
	}
	return a, nil
}

// Addr returns the address the HTTP server listens on.
func (a *App) Addr() net.Addr { return a.listener.Addr() }

// EventStores returns the event store selector.
func (a *App) EventStores() *EventStores { return a.stores }

// Run runs the roles until ctx ends or a component fails, then shuts
// down within TANVO_SHUTDOWN_TIMEOUT: readiness turns unavailable, the
// HTTP server drains its requests, the workers finish their jobs, the
// consumers commit and stop, the API key uses are written, and the
// connections close. It returns the component's failure, if any.
func (a *App) Run(ctx context.Context) error {
	defer a.close()
	runCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	defer stop()
	g, gctx := errgroup.WithContext(runCtx)

	if a.roles.Has(config.RoleWorker) {
		if err := a.jobs.Start(runCtx); err != nil {
			return err
		}
	}
	g.Go(func() error {
		if err := a.server.Serve(a.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http: %w", err)
		}
		return nil
	})
	if a.consumers != nil {
		for _, c := range a.consumers.run {
			g.Go(func() error { return c.Run(gctx) })
		}
	}
	if a.api != nil {
		g.Go(func() error { return a.api.runUsageFlush(gctx) })
	}
	a.log.InfoContext(ctx, "tanvo: serving", "roles", a.roles.String(), "addr", a.Addr().String())

	select {
	case <-ctx.Done():
	case <-gctx.Done():
	}
	a.log.InfoContext(runCtx, "tanvo: shutting down", "timeout", a.cfg.ShutdownTimeout)
	sctx, cancel := context.WithTimeout(runCtx, a.cfg.ShutdownTimeout)
	defer cancel()
	var errs []error
	a.health.draining.Store(true)
	if err := a.server.Shutdown(sctx); err != nil {
		errs = append(errs, fmt.Errorf("http: shutdown: %w", err))
	}
	if a.roles.Has(config.RoleWorker) {
		if err := a.jobs.Stop(sctx); err != nil {
			errs = append(errs, err)
		}
	}
	stop()
	if err := g.Wait(); err != nil {
		errs = append([]error{err}, errs...)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	a.log.InfoContext(runCtx, "tanvo: stopped")
	return nil
}

// close releases the connections, in reverse order of creation.
func (a *App) close() {
	if a.consumers != nil {
		a.consumers.close()
	}
	if a.producer != nil {
		a.producer.Close()
	}
	if a.ch != nil {
		if err := a.ch.Close(); err != nil {
			a.log.Warn("clickhouse: close", "error", err)
		}
	}
	if a.pool != nil {
		a.pool.Close()
	}
}
