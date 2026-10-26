// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

// Command tanvo-analytics serves the Data API (architecture §14.1).
//
//	tanvo-analytics serve                      migrate, then serve and refresh
//	tanvo-analytics migrate                    apply the analytics migrations
//	tanvo-analytics rebuild --organization ID  rebuild one organization
//
// It reads its configuration from the environment:
//
//	TANVO_ANALYTICS_DATABASE_URL      the core database's primary, as the analytics role (required)
//	TANVO_ANALYTICS_REPLICA_URL       a replica serving the reports (optional)
//	TANVO_ANALYTICS_TOKEN_KEYS        the core's service token public keys (serve)
//	TANVO_LICENSE_KEY                 the license key (serve)
//	TANVO_ANALYTICS_LISTEN            the listen address, :8090 by default
//	TANVO_ANALYTICS_REFRESH_INTERVAL  how often changed organizations are rebuilt, 5m by default
//	TANVO_LOG_LEVEL                   debug, info (default), warn or error
//
// Telemetry is configured by the standard OTEL_* variables.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // the customers' time zones, whatever the host has

	"buf.build/go/protovalidate"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/tanvoxyz/tanvo/ee/analytics/server"
	"github.com/tanvoxyz/tanvo/ee/analytics/servicetoken"
	"github.com/tanvoxyz/tanvo/ee/analytics/store"
	"github.com/tanvoxyz/tanvo/ee/license"
)

const usage = `usage: tanvo-analytics <command>

commands:
  serve                      apply the migrations, then serve the Data API and refresh the analytics
  migrate                    apply the analytics migrations
  rebuild --organization ID  rebuild the analytics of one organization
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:])
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tanvo-analytics:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("missing command")
	}
	var level slog.Level
	if v := os.Getenv("TANVO_LOG_LEVEL"); v != "" {
		if err := level.UnmarshalText([]byte(v)); err != nil {
			return fmt.Errorf("TANVO_LOG_LEVEL: %w", err)
		}
	}
	tel, err := setupTelemetry(ctx, level)
	if err != nil {
		return err
	}
	defer func() {
		if err := tel.shutdown(context.WithoutCancel(ctx)); err != nil {
			tel.log.Warn("telemetry shutdown", "error", err)
		}
	}()

	switch args[0] {
	case "serve":
		return serve(ctx, tel)
	case "migrate":
		pool, err := connect(ctx, "TANVO_ANALYTICS_DATABASE_URL", true)
		if err != nil {
			return err
		}
		defer pool.Close()
		return store.Migrate(ctx, pool, tel.log)
	case "rebuild":
		fs := flag.NewFlagSet("rebuild", flag.ContinueOnError)
		org := fs.String("organization", "", "the organization's ID")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *org == "" {
			return errors.New("rebuild: --organization is required")
		}
		pool, err := connect(ctx, "TANVO_ANALYTICS_DATABASE_URL", true)
		if err != nil {
			return err
		}
		defer pool.Close()
		return store.New(pool, store.Options{Log: tel.log, Tracer: tel.tracer.Tracer("github.com/tanvoxyz/tanvo/ee/analytics/store")}).Rebuild(ctx, *org)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	}
	fmt.Fprint(os.Stderr, usage)
	return fmt.Errorf("unknown command %q", args[0])
}

// connect opens the pool of the database URL in env.
func connect(ctx context.Context, env string, required bool) (*pgxpool.Pool, error) {
	url := os.Getenv(env)
	if url == "" {
		if required {
			return nil, fmt.Errorf("%s is required", env)
		}
		return nil, nil //nolint:nilnil // an optional database not configured
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", env, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("%s: %w", env, err)
	}
	return pool, nil
}

func serve(ctx context.Context, tel *telemetry) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	log := tel.log

	checker := license.NewChecker(license.NewVerifier(license.TrustedKeys(), nil), os.Getenv(license.EnvKey), license.ServiceAnalytics, log)
	if err := checker.Start(ctx); err != nil {
		return fmt.Errorf("license: %w", err)
	}
	keys, err := servicetoken.ParseKeys(os.Getenv("TANVO_ANALYTICS_TOKEN_KEYS"))
	if err != nil {
		return fmt.Errorf("TANVO_ANALYTICS_TOKEN_KEYS: %w", err)
	}
	if len(keys) == 0 {
		return errors.New("TANVO_ANALYTICS_TOKEN_KEYS is required: run `tanvo admin service-token-key public` on the core")
	}
	refresh := 5 * time.Minute
	if v := os.Getenv("TANVO_ANALYTICS_REFRESH_INTERVAL"); v != "" {
		if refresh, err = time.ParseDuration(v); err != nil || refresh <= 0 {
			return fmt.Errorf("TANVO_ANALYTICS_REFRESH_INTERVAL: want a positive duration, got %q", v)
		}
	}
	listen := os.Getenv("TANVO_ANALYTICS_LISTEN")
	if listen == "" {
		listen = ":8090"
	}

	primary, err := connect(ctx, "TANVO_ANALYTICS_DATABASE_URL", true)
	if err != nil {
		return err
	}
	defer primary.Close()
	replica, err := connect(ctx, "TANVO_ANALYTICS_REPLICA_URL", false)
	if err != nil {
		return err
	}
	if replica != nil {
		defer replica.Close()
	}
	if err := store.Migrate(ctx, primary, log); err != nil {
		return err
	}
	st := store.New(primary, store.Options{
		Replica: replica, Log: log, Tracer: tel.tracer.Tracer("github.com/tanvoxyz/tanvo/ee/analytics/store"),
	})

	validator, err := protovalidate.New()
	if err != nil {
		return fmt.Errorf("validator: %w", err)
	}
	telemetry, err := server.Telemetry(tel.tracer, tel.meter, log)
	if err != nil {
		return err
	}
	handler, err := server.Handler(server.NewService(st, time.Now),
		telemetry, server.Recover(log), server.Licensed(checker),
		server.Auth(servicetoken.NewVerifier(keys, time.Now)), server.Validate(validator))
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr: listen,
		Handler: otelhttp.NewHandler(handler, "tanvo-analytics",
			otelhttp.WithTracerProvider(tel.tracer), otelhttp.WithMeterProvider(tel.meter), otelhttp.WithPropagators(tel.propagator)),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	go refreshLoop(ctx, st, refresh, log)
	errc := make(chan error, 1)
	go func() {
		log.InfoContext(ctx, "serving the Data API", "address", listen)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	shutdown, done := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer done()
	if err := srv.Shutdown(shutdown); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

// refreshLoop rebuilds the organizations whose core data changed, every
// interval, until ctx is done.
func refreshLoop(ctx context.Context, st *store.Store, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := st.Refresh(ctx); err != nil && ctx.Err() == nil {
			log.ErrorContext(ctx, "analytics refresh failed", "error", err)
		}
	}
}
