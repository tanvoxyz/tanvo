// Package telemetry installs the OpenTelemetry SDK (architecture §15):
// tracer, meter and logger providers configured by the standard OTEL_*
// environment variables through autoexport, a resource describing the
// process, W3C Trace Context and Baggage propagation, an optional
// Prometheus reader served at /metrics, and the slog logger that writes
// JSON to stdout and bridges records to OpenTelemetry logs.
//
// It is the only package of the repository that imports the SDK and the
// exporters; libraries receive the providers through their options.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/log"
	lognoop "go.opentelemetry.io/otel/log/noop"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/tanvoxyz/tanvo/apps/server/internal/logctx"
)

// ServiceName is the default service.name; OTEL_SERVICE_NAME overrides
// it.
const ServiceName = "tanvo"

// ScopeName is the instrumentation scope of the server's own telemetry.
const ScopeName = "github.com/tanvoxyz/tanvo/apps/server"

// Options configures [Setup].
type Options struct {
	// Version is service.version.
	Version string
	// Environment is deployment.environment.name; empty leaves it out.
	Environment string
	// Role is tanvo.role ("api,worker").
	Role string
	// LogLevel is the minimum level of the logs.
	LogLevel slog.Level
	// LogText writes text logs instead of JSON.
	LogText bool
	// Stdout receives the logs; nil is os.Stdout.
	Stdout io.Writer
	// Prometheus adds a Prometheus reader served by [Telemetry.Metrics].
	Prometheus bool
	// Environ lists the environment; nil is os.Environ. Only
	// OTEL_SDK_DISABLED is read from it; the exporters read the process
	// environment.
	Environ []string
}

// Telemetry holds the installed providers.
type Telemetry struct {
	// TracerProvider, MeterProvider and LoggerProvider are the providers,
	// also installed as the globals.
	TracerProvider trace.TracerProvider
	// MeterProvider is the meter provider.
	MeterProvider metric.MeterProvider
	// LoggerProvider is the OpenTelemetry logs provider.
	LoggerProvider log.LoggerProvider
	// Propagator is the W3C Trace Context and Baggage propagator.
	Propagator propagation.TextMapPropagator
	// Logger writes to stdout and to LoggerProvider; it is also
	// slog.Default.
	Logger *slog.Logger
	// Metrics serves the Prometheus reader, nil when disabled.
	Metrics http.Handler

	shutdown []func(context.Context) error
}

// Setup creates the providers and the logger and installs them as the
// globals.
func Setup(ctx context.Context, o Options) (*Telemetry, error) {
	t := &Telemetry{
		Propagator: propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}),
	}
	stdout := o.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	disabled := sdkDisabled(o.Environ)
	var err error
	if disabled {
		t.TracerProvider, t.MeterProvider, t.LoggerProvider = tracenoop.NewTracerProvider(), metricnoop.NewMeterProvider(), lognoop.NewLoggerProvider()
	} else if err = t.setupSDK(ctx, o); err != nil {
		_ = t.Shutdown(context.WithoutCancel(ctx))
		return nil, err
	}

	hopts := &slog.HandlerOptions{Level: o.LogLevel}
	var console slog.Handler
	if o.LogText {
		console = slog.NewTextHandler(stdout, hopts)
	} else {
		console = slog.NewJSONHandler(stdout, hopts)
	}
	handler := slog.Handler(logctx.NewTraceHandler(console))
	if !disabled {
		bridge := otelslog.NewHandler(ScopeName, otelslog.WithLoggerProvider(t.LoggerProvider), otelslog.WithVersion(o.Version))
		handler = slog.NewMultiHandler(handler, &levelHandler{min: o.LogLevel, next: bridge})
	}
	t.Logger = slog.New(logctx.NewHandler(handler))

	slog.SetDefault(t.Logger)
	otel.SetTracerProvider(t.TracerProvider)
	otel.SetMeterProvider(t.MeterProvider)
	otel.SetLoggerProvider(t.LoggerProvider)
	otel.SetTextMapPropagator(t.Propagator)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		t.Logger.Warn("opentelemetry", "error", err)
	}))
	return t, nil
}

func (t *Telemetry) setupSDK(ctx context.Context, o Options) error {
	res, err := newResource(ctx, o)
	if err != nil {
		return err
	}

	spans, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		return fmt.Errorf("telemetry: span exporter: %w", err)
	}
	topts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if !autoexport.IsNoneSpanExporter(spans) {
		topts = append(topts, sdktrace.WithBatcher(spans))
	}
	tp := sdktrace.NewTracerProvider(topts...)
	t.TracerProvider = tp
	t.shutdown = append(t.shutdown, tp.Shutdown)

	reader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return fmt.Errorf("telemetry: metric reader: %w", err)
	}
	mopts := []sdkmetric.Option{sdkmetric.WithResource(res)}
	if !autoexport.IsNoneMetricReader(reader) {
		mopts = append(mopts, sdkmetric.WithReader(reader))
	}
	if o.Prometheus {
		reg := prometheus.NewRegistry()
		exp, err := otelprom.New(otelprom.WithRegisterer(reg))
		if err != nil {
			return fmt.Errorf("telemetry: prometheus reader: %w", err)
		}
		mopts = append(mopts, sdkmetric.WithReader(exp))
		t.Metrics = promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	}
	mp := sdkmetric.NewMeterProvider(mopts...)
	t.MeterProvider = mp
	t.shutdown = append(t.shutdown, mp.Shutdown)

	logs, err := autoexport.NewLogExporter(ctx)
	if err != nil {
		return fmt.Errorf("telemetry: log exporter: %w", err)
	}
	lopts := []sdklog.LoggerProviderOption{sdklog.WithResource(res)}
	if !autoexport.IsNoneLogExporter(logs) {
		lopts = append(lopts, sdklog.WithProcessor(sdklog.NewBatchProcessor(logs)))
	}
	lp := sdklog.NewLoggerProvider(lopts...)
	t.LoggerProvider = lp
	t.shutdown = append(t.shutdown, lp.Shutdown)
	return nil
}

// newResource describes the process; OTEL_SERVICE_NAME and
// OTEL_RESOURCE_ATTRIBUTES override the defaults.
func newResource(ctx context.Context, o Options) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{
		attribute.String("service.name", ServiceName),
		attribute.String("service.instance.id", uuid.NewString()),
		attribute.String("tanvo.role", o.Role),
	}
	if o.Version != "" {
		attrs = append(attrs, attribute.String("service.version", o.Version))
	}
	if o.Environment != "" {
		attrs = append(attrs, attribute.String("deployment.environment.name", o.Environment))
	}
	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithHost(),
		resource.WithOSType(),
		resource.WithProcessPID(),
		resource.WithProcessRuntimeName(),
		resource.WithProcessRuntimeVersion(),
		resource.WithContainerID(),
		resource.WithAttributes(attrs...),
		resource.WithFromEnv(),
	)
	if err != nil && !errors.Is(err, resource.ErrPartialResource) {
		return nil, fmt.Errorf("telemetry: resource: %w", err)
	}
	return res, nil
}

func sdkDisabled(environ []string) bool {
	if environ == nil {
		return strings.EqualFold(strings.TrimSpace(os.Getenv("OTEL_SDK_DISABLED")), "true")
	}
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, "OTEL_SDK_DISABLED="); ok {
			return strings.EqualFold(strings.TrimSpace(v), "true")
		}
	}
	return false
}

// Shutdown flushes and stops the providers.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	for i := len(t.shutdown) - 1; i >= 0; i-- {
		if err := t.shutdown[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	t.shutdown = nil
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("telemetry: shutdown: %w", err)
	}
	return nil
}

// Tracer returns the server's tracer.
func (t *Telemetry) Tracer() trace.Tracer { return t.TracerProvider.Tracer(ScopeName) }

// Meter returns the server's meter.
func (t *Telemetry) Meter() metric.Meter { return t.MeterProvider.Meter(ScopeName) }

// levelHandler drops records below min.
type levelHandler struct {
	min  slog.Level
	next slog.Handler
}

func (h *levelHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.min && h.next.Enabled(ctx, l)
}

func (h *levelHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.next.Handle(ctx, r)
}

func (h *levelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &levelHandler{min: h.min, next: h.next.WithAttrs(attrs)}
}

func (h *levelHandler) WithGroup(name string) slog.Handler {
	return &levelHandler{min: h.min, next: h.next.WithGroup(name)}
}
