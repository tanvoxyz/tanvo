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
	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
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
	// Clock, when set, is the billing clock instead of the system clock,
	// and the instant of the billing clock jobs instead of their tick. It
	// is a test hook: production never sets it.
	Clock core.Clock
	// StripeBaseURL, when set, is the Stripe API base URL instead of
	// Stripe's (a fake Stripe server). It is a test hook: production never
	// sets it.
	StripeBaseURL string
	// ManualClockJobs schedules no periodic job: the clock jobs run only
	// when triggered ([jobs.Client.Trigger]). It is a test hook for
	// running them at a test clock's instants.
	ManualClockJobs bool
}

// App is a wired server process.
type App struct {
	cfg   config.Config
	roles config.Roles
	tel   *telemetry.Telemetry
	log   *slog.Logger

	pool *pgxpool.Pool
	// direct is the pool of TANVO_DATABASE_DIRECT_URL, nil when unset:
	// the connections that keep session state ([App.sessionPool]).
	direct    *pgxpool.Pool
	ch        driver.Conn
	store     *store.Store
	jobs      *jobs.Client
	billing   *billing.Engine
	stores    *EventStores
	logs      auditlog.Archive
	producer  *kgo.Client
	consumers *consumers
	api       *api
	changes   *catalogChanges
	health    *health
	blobs     *blobs
	caches    *caches
	docs      *docs
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
	if a.direct, err = openDirectPool(ctx, cfg, directMaxConns, tp, mp); err != nil {
		return nil, err
	}
	if cfg.ClickHouseURL != "" {
		if a.ch, err = openClickHouse(ctx, cfg.ClickHouseURL); err != nil {
			return nil, err
		}
	}
	if cfg.MigrateOnStart {
		err = migrateAll(ctx, cfg, a.sessionPool(), a.ch)
	} else {
		err = checkSchemas(ctx, cfg, a.pool)
	}
	if err != nil {
		return nil, err
	}
	a.health.add("postgres", a.pool.Ping)
	if a.direct != nil {
		a.health.add("postgres_direct", a.direct.Ping)
	}
	a.changes = newCatalogChanges(a.sessionPool(), a.log)
	if a.ch != nil {
		a.health.add("clickhouse", a.ch.Ping)
	}

	// The store, whose jobs commit with its transactions, the event
	// stores, and the jobs.
	sopts := []store.Option{
		store.WithJobInserter(jobInserter(func() *jobs.Client { return a.jobs })),
		store.WithCatalogChanges(a.changes.committed),
		store.WithLogger(a.log),
	}
	if cfg.DatabaseRowLevelSecurity {
		sopts = append(sopts, store.WithRowLevelSecurity())
	}
	a.logs = logArchive(a.ch)
	sopts = append(sopts, logArchiveOptions(a.logs)...)
	a.store = store.New(a.pool, sopts...)
	modeOf, err := ingestModes(cfg)
	if err != nil {
		return nil, err
	}
	a.stores = newEventStores(a.pool, cfg.EventStoreSchema, a.ch, modeOf, tp)
	var clock core.Clock = core.SystemClock{}
	if o.Clock != nil {
		clock = o.Clock
	}
	if a.blobs, err = newBlobs(cfg, a.log, tp); err != nil {
		return nil, err
	}
	if a.caches, err = newCaches(ctx, cfg, a.log, tp, mp); err != nil {
		return nil, err
	}
	composer, sender, err := newMailer(cfg, a.log, tp)
	if err != nil {
		return nil, err
	}
	reg, err := newRegistry(jobHandlers{
		webhooks: newDispatcher(cfg, a.store, tp, mp), tx: a.store, stores: a.stores, mp: mp, clock: clock,
		blobs: a.blobs.blobOrNil(), composer: composer, sender: sender, bucketLimits: usageBucketLimits(cfg), log: a.log,
	})
	if err != nil {
		return nil, err
	}
	triggers := newSyncTriggers(a.store)
	pay, err := newPayments(cfg, a.store, clock, a.log, tp, mp, triggers, o.StripeBaseURL)
	if err != nil {
		return nil, err
	}
	// The concurrency slots are the shared server's when TANVO_CACHE_URL
	// names one, else the database's.
	var slots port.Slots = a.store
	if s := a.caches.slots(); s != nil {
		slots = s
	}
	integ, err := newIntegrations(cfg, a.store, clock, a.log, tp, mp, pay.gocardlessOAuth, triggers, a.blobs.blobOrNil(),
		a.caches.named(cacheDraftTax, memoryCacheEntries), a.caches.counter(counterRateLimits), slots)
	if err != nil {
		return nil, err
	}
	pay.svc.WithPaymentSync(integ.svc)
	pay.svc.WithBlobs(a.blobs.blobOrNil())
	if err := integ.register(reg); err != nil {
		return nil, err
	}
	streams := newStreamProducer(cfg, a.log, tp, mp)
	if a.billing, err = newBilling(cfg, a.store, a.stores, clock, a.log, pay.svc.Refunds(), triggers, integ.taxes, streams,
		a.caches.named(cacheUsage, cfg.UsageCacheEntries)); err != nil {
		return nil, err
	}
	if err := (streamingJobs{engine: a.billing}).register(reg); err != nil {
		return nil, err
	}
	pay.bind(a.billing)
	integ.bind(a.billing)
	// Kafka: the producer of the API, the client of the consumers, and the
	// ingestion barrier of the workers' periodic billing.
	ktel := kafkaTelemetry{tp: tp, mp: mp, propagator: tel.Propagator}
	if ((o.Roles.Has(config.RoleAPI) || o.Roles.Has(config.RoleWorker)) && cfg.KafkaUsed()) || o.Roles.Has(config.RoleConsumer) {
		if a.producer, err = newKafkaClient(cfg, ktel, ""); err != nil {
			return nil, err
		}
		if err := ensureTopics(ctx, cfg, a.producer); err != nil {
			return nil, err
		}
		a.health.add("kafka", a.producer.Ping)
	}
	bj := billingJobs{engine: a.billing, tx: a.store, clock: o.Clock, log: a.log}
	if o.Roles.Has(config.RoleWorker) && cfg.KafkaUsed() {
		if bj.barrier, err = newIngestBarrier(cfg, a.producer, modeOf, clock, mp, a.log); err != nil {
			return nil, err
		}
	}
	if err := bj.register(reg); err != nil {
		return nil, err
	}
	if err := (mailJobs{tx: a.store, composer: composer, sender: sender, blobs: a.blobs.blobOrNil()}).register(reg); err != nil {
		return nil, err
	}
	if a.docs, err = newDocuments(cfg, a.store, a.stores, clock, a.log, triggers, a.blobs.blobOrNil(), composer, sender); err != nil {
		return nil, err
	}
	if err := a.docs.register(reg); err != nil {
		return nil, err
	}
	if err := (paymentJobs{w: pay, billing: bj, composer: composer, sender: sender, docs: a.docs, log: a.log}).register(reg); err != nil {
		return nil, err
	}
	if a.jobs, err = newJobs(a.pool, a.direct, reg, cfg, o.Roles, o.ManualClockJobs, a.log, tp, mp); err != nil {
		return nil, err
	}

	// The Kafka consumers.
	if o.Roles.Has(config.RoleConsumer) {
		if a.consumers, err = newConsumers(cfg, a.store, a.stores.ClickHouse(), a.billing, a.producer, ktel, a.log, a.changes); err != nil {
			return nil, err
		}
	}

	// HTTP: health and metrics on every role, the API on the api role.
	mux := http.NewServeMux()
	a.health.mount(mux)
	a.blobs.mount(mux)
	if tel.Metrics != nil {
		mux.Handle("GET "+metricsPath, tel.Metrics)
	}
	if o.Roles.Has(config.RoleAPI) {
		var producer *kgo.Client
		if cfg.KafkaUsed() {
			producer = a.producer
		}
		svc, err := newIngestService(cfg, a.store, a.billing, modeOf, producer, a.changes)
		if err != nil {
			return nil, err
		}
		reader, err := newEventReader(cfg, a.store, modeOf, a.stores.ClickHouse())
		if err != nil {
			return nil, err
		}
		secretKey, err := cfg.SecretKeyBytes()
		if err != nil {
			return nil, err
		}
		if a.api, err = newAPI(mux, APIOptions{
			Tx: a.store, Ingest: svc, EventReader: reader, AttributedUsage: a.stores, Billing: a.billing, MaxBytes: cfg.HTTPMaxBodyBytes, TrustedProxies: cfg.TrustedProxies,
			Log: a.log, TracerProvider: tp, MeterProvider: mp, Clock: clock, AppURL: cfg.AppURL,
			DisableSignup: cfg.DisableSignup, AnalyticsCacheTTL: cfg.AnalyticsCacheTTL,
			AnalyticsCache: a.caches.named(cacheAnalytics, memoryCacheEntries), APIKeyCache: a.caches.sharedOnly(cacheAPIKeys), APIKeyCacheTTL: cfg.APIKeyCacheTTL, GoogleClientID: cfg.GoogleAuthClientID, GoogleClientSecret: cfg.GoogleAuthClientSecret,
			WebhooksAllowPrivate: cfg.WebhooksAllowPrivate, OIDCAllowPrivate: cfg.OIDCAllowPrivate, SecretKey: secretKey,
			Payments: pay.svc, InboundWebhooks: pay.inbound, Integrations: integ.svc,
			Files: a.docs, Blobs: a.blobs.blobOrNil(),
		}); err != nil {
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

// Jobs returns the jobs client.
func (a *App) Jobs() *jobs.Client { return a.jobs }

// Billing returns the billing engine.
func (a *App) Billing() *billing.Engine { return a.billing }

// Run runs the roles until ctx ends or a component fails, then shuts
// down within TANVO_SHUTDOWN_TIMEOUT: readiness turns unavailable, the
// HTTP server drains its requests, the workers finish their jobs, the
// consumers commit and stop, the API key uses and API logs are written,
// and the connections close. It returns the component's failure, if any.
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
	if a.roles.Has(config.RoleWorker) && a.logs != nil {
		g.Go(func() error { return runLogArchive(gctx, a.store, a.logs, a.log) })
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
		g.Go(func() error { return a.api.runAPILogs(gctx) })
	}
	if a.api != nil || a.consumers != nil {
		g.Go(func() error { return a.changes.run(gctx) })
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
	a.docs.close()
	if a.blobs != nil {
		if err := a.blobs.close(); err != nil {
			a.log.Warn("blob store: close", "error", err)
		}
	}
	a.caches.close()
	if a.pool != nil {
		a.pool.Close()
	}
	if a.direct != nil {
		a.direct.Close()
	}
}

// sessionPool is the pool of the connections that keep session state
// (LISTEN, the migrations' session advisory locks): the direct pool when
// TANVO_DATABASE_DIRECT_URL is set, so that TANVO_DATABASE_URL may point
// at a pooler in transaction mode, the main pool otherwise.
func (a *App) sessionPool() *pgxpool.Pool {
	if a.direct != nil {
		return a.direct
	}
	return a.pool
}
