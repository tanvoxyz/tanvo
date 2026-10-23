package app

import (
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/webhooks"
)

// newDispatcher returns the outgoing webhook dispatcher: deliveries go
// through the guarded client of libs/webhooks (no private targets unless
// TANVO_WEBHOOKS_ALLOW_PRIVATE), whose transport is traced and measured
// by otelhttp. Trace context is not propagated to receivers, which are
// third parties.
func newDispatcher(cfg config.Config, tx port.Transactor, tp trace.TracerProvider, mp metric.MeterProvider) *webhooks.Dispatcher {
	client := webhooks.NewClient(webhooks.ClientConfig{Timeout: cfg.WebhooksTimeout, AllowPrivate: cfg.WebhooksAllowPrivate})
	client.Transport = otelhttp.NewTransport(client.Transport,
		otelhttp.WithTracerProvider(tp),
		otelhttp.WithMeterProvider(mp),
		otelhttp.WithPropagators(propagation.NewCompositeTextMapPropagator()),
	)
	return &webhooks.Dispatcher{
		Tx:     tx,
		Sender: &webhooks.Sender{Client: client, AllowPrivate: cfg.WebhooksAllowPrivate},
		Clock:  core.SystemClock{},
	}
}
