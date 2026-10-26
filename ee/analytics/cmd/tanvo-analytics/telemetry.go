// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
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
)

// serviceName is the default service.name; OTEL_SERVICE_NAME overrides it.
const serviceName = "tanvo-analytics"

// telemetry holds the OpenTelemetry providers, configured by the standard
// OTEL_* variables through autoexport, and the logger: JSON on stdout,
// bridged to OpenTelemetry logs.
type telemetry struct {
	tracer     trace.TracerProvider
	meter      metric.MeterProvider
	logs       log.LoggerProvider
	propagator propagation.TextMapPropagator
	log        *slog.Logger
	shutdowns  []func(context.Context) error
}

func setupTelemetry(ctx context.Context, level slog.Level) (*telemetry, error) {
	t := &telemetry{propagator: propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})}
	var handler slog.Handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		t.tracer, t.meter, t.logs = tracenoop.NewTracerProvider(), metricnoop.NewMeterProvider(), lognoop.NewLoggerProvider()
	} else {
		if err := t.setupSDK(ctx); err != nil {
			return nil, errors.Join(err, t.shutdown(context.WithoutCancel(ctx)))
		}
		handler = slog.NewMultiHandler(handler, otelslog.NewHandler(serviceName, otelslog.WithLoggerProvider(t.logs)))
	}
	t.log = slog.New(handler)
	slog.SetDefault(t.log)
	otel.SetTracerProvider(t.tracer)
	otel.SetMeterProvider(t.meter)
	otel.SetTextMapPropagator(t.propagator)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) { t.log.Warn("opentelemetry", "error", err) }))
	return t, nil
}

func (t *telemetry) setupSDK(ctx context.Context) error {
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(attribute.String("service.name", serviceName)))
	if err != nil {
		return fmt.Errorf("telemetry: resource: %w", err)
	}
	// The environment (OTEL_SERVICE_NAME, OTEL_RESOURCE_ATTRIBUTES) wins.
	if res, err = resource.Merge(res, resource.Environment()); err != nil {
		return fmt.Errorf("telemetry: resource: %w", err)
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
	t.tracer = tp
	t.shutdowns = append(t.shutdowns, tp.Shutdown)

	reader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return fmt.Errorf("telemetry: metric reader: %w", err)
	}
	mopts := []sdkmetric.Option{sdkmetric.WithResource(res)}
	if !autoexport.IsNoneMetricReader(reader) {
		mopts = append(mopts, sdkmetric.WithReader(reader))
	}
	mp := sdkmetric.NewMeterProvider(mopts...)
	t.meter = mp
	t.shutdowns = append(t.shutdowns, mp.Shutdown)

	logs, err := autoexport.NewLogExporter(ctx)
	if err != nil {
		return fmt.Errorf("telemetry: log exporter: %w", err)
	}
	lopts := []sdklog.LoggerProviderOption{sdklog.WithResource(res)}
	if !autoexport.IsNoneLogExporter(logs) {
		lopts = append(lopts, sdklog.WithProcessor(sdklog.NewBatchProcessor(logs)))
	}
	lp := sdklog.NewLoggerProvider(lopts...)
	t.logs = lp
	t.shutdowns = append(t.shutdowns, lp.Shutdown)
	return nil
}

func (t *telemetry) shutdown(ctx context.Context) error {
	var errs []error
	for _, f := range t.shutdowns {
		errs = append(errs, f(ctx))
	}
	return errors.Join(errs...)
}
