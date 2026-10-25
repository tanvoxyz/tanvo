// Package cli is the tanvo command line: `tanvo serve` runs the server
// roles, `tanvo migrate` applies the schemas, `tanvo admin bootstrap`
// ensures a first organization, `tanvo events reprocess` re-enriches
// stored events and `tanvo version` prints the build. Settings come from
// TANVO_* variables and an optional TOML file (internal/config);
// telemetry from the OTEL_* variables (internal/telemetry).
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/tanvoxyz/tanvo/apps/server/internal/app"
	"github.com/tanvoxyz/tanvo/apps/server/internal/buildinfo"
	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/apps/server/internal/telemetry"
	"github.com/tanvoxyz/tanvo/libs/core"
)

// telemetryFlushTimeout bounds the flush of telemetry at exit.
const telemetryFlushTimeout = 5 * time.Second

// Env is the process environment of a run.
type Env struct {
	// Environ lists the environment as "KEY=value"; nil is os.Environ.
	Environ []string
	// Stdin is read by commands taking a secret from it; nil is os.Stdin.
	Stdin io.Reader
	// Stdout receives the logs and the version; nil is os.Stdout.
	Stdout io.Writer
	// Stderr receives usage errors; nil is os.Stderr.
	Stderr io.Writer
	// Started, when set, receives the server once it is wired, before it
	// runs (tests read its address).
	Started func(*app.App)
	// Clock, when set, is the server's billing clock ([app.Options.Clock]);
	// a test hook.
	Clock core.Clock
	// StripeBaseURL, when set, is the Stripe API base URL
	// ([app.Options.StripeBaseURL]); a test hook.
	StripeBaseURL string
	// ManualClockJobs runs the clock jobs only when triggered
	// ([app.Options.ManualClockJobs]); a test hook.
	ManualClockJobs bool
}

// Run runs the command line args (without the program name) until ctx
// ends.
func Run(ctx context.Context, args []string, env Env) error {
	if env.Stdout == nil {
		env.Stdout = os.Stdout
	}
	if env.Stderr == nil {
		env.Stderr = os.Stderr
	}
	if env.Stdin == nil {
		env.Stdin = os.Stdin
	}
	root := newRoot(&env)
	root.SetArgs(args)
	root.SetOut(env.Stdout)
	root.SetErr(env.Stderr)
	return root.ExecuteContext(ctx)
}

func newRoot(env *Env) *cobra.Command {
	var file string
	root := &cobra.Command{
		Use:           "tanvo",
		Short:         "Tanvo usage-based billing server",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&file, "config", "", "TOML settings file (default $"+config.FileEnv+")")
	root.AddCommand(newServe(env, &file), newMigrate(env, &file), newAdmin(env, &file), newEvents(env, &file), newVersion(env))
	return root
}

// setup loads the configuration and installs telemetry for a command.
func setup(ctx context.Context, env *Env, file, role string) (config.Config, *telemetry.Telemetry, error) {
	cfg, warnings, err := config.Load(config.Source{Environ: env.Environ, File: file})
	if err != nil {
		return config.Config{}, nil, err
	}
	level, _ := cfg.SlogLevel()
	tel, err := telemetry.Setup(ctx, telemetry.Options{
		Version:     buildinfo.Read().Version,
		Environment: cfg.Environment,
		Role:        role,
		LogLevel:    level,
		LogText:     cfg.LogFormat == "text",
		Stdout:      env.Stdout,
		Prometheus:  cfg.MetricsEndpoint,
		Environ:     env.Environ,
	})
	if err != nil {
		return config.Config{}, nil, err
	}
	for _, w := range warnings {
		tel.Logger.WarnContext(ctx, "config: "+w)
	}
	return cfg, tel, nil
}

func shutdownTelemetry(ctx context.Context, tel *telemetry.Telemetry, err *error) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), telemetryFlushTimeout)
	defer cancel()
	if serr := tel.Shutdown(sctx); serr != nil && *err == nil {
		*err = serr
	}
}

func newServe(env *Env, file *string) *cobra.Command {
	var roles, queues []string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the server roles (api, worker, consumer)",
		Long: "Run the server roles until SIGINT or SIGTERM. --role overrides TANVO_ROLE (default all: api and\n" +
			"worker, plus consumer when Kafka brokers are configured); --queues overrides TANVO_WORKER_QUEUES.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			ctx := cmd.Context()
			cfg, _, err := config.Load(config.Source{Environ: env.Environ, File: *file})
			if err != nil {
				return err
			}
			rs, err := cfg.ResolveRoles(roles)
			if err != nil {
				return fmt.Errorf("--role: %w", err)
			}
			cfg, tel, err := setup(ctx, env, *file, rs.String())
			if err != nil {
				return err
			}
			defer shutdownTelemetry(ctx, tel, &err)
			if len(queues) > 0 {
				cfg.WorkerQueues = queues
				if err := cfg.Validate(); err != nil {
					return fmt.Errorf("--queues: %w", err)
				}
			}
			a, err := app.New(ctx, app.Options{Config: cfg, Roles: rs, Telemetry: tel, Clock: env.Clock, StripeBaseURL: env.StripeBaseURL, ManualClockJobs: env.ManualClockJobs})
			if err != nil {
				tel.Logger.ErrorContext(ctx, "tanvo: start", "error", err)
				return err
			}
			if env.Started != nil {
				env.Started(a)
			}
			if err := a.Run(ctx); err != nil {
				tel.Logger.ErrorContext(ctx, "tanvo: run", "error", err)
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&roles, "role", nil, "roles to run: api, worker, consumer or all (default $TANVO_ROLE, else all)")
	cmd.Flags().StringSliceVar(&queues, "queues", nil, "queues the worker role works (default $TANVO_WORKER_QUEUES, else every queue)")
	return cmd
}

func newMigrate(env *Env, file *string) *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Apply the database schemas (PostgreSQL store, jobs, event store; ClickHouse when configured)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			ctx := cmd.Context()
			cfg, tel, err := setup(ctx, env, *file, "migrate")
			if err != nil {
				return err
			}
			defer shutdownTelemetry(ctx, tel, &err)
			if err := app.Migrate(ctx, cfg, tel.TracerProvider, tel.MeterProvider); err != nil {
				tel.Logger.ErrorContext(ctx, "tanvo: migrate", "error", err)
				return err
			}
			return nil
		},
	}
}

func newVersion(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the build version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			_, err := fmt.Fprintln(env.Stdout, "tanvo", buildinfo.Read().String())
			return err
		},
	}
}
