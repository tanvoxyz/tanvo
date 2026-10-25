package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/billing/streaming"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/jobs"
	"github.com/tanvoxyz/tanvo/libs/webhooks/kinesis"
)

// newStreamProducer returns the producer of the streaming destinations
// (parity §1.40): Kinesis, signed with the destination roles assumed from
// the workers' AWS identity, through the intermediate role when one is
// configured.
func newStreamProducer(cfg config.Config, log *slog.Logger, tp trace.TracerProvider, mp metric.MeterProvider) *kinesis.Producer {
	hc := kinesis.NewHTTPClient()
	hc.Transport = otelhttp.NewTransport(hc.Transport, otelhttp.WithTracerProvider(tp), otelhttp.WithMeterProvider(mp))
	return kinesis.New(kinesis.Config{
		HTTPClient:          hc,
		IntermediateRoleARN: cfg.StreamingIntermediateRoleARN,
		KinesisEndpoint:     cfg.StreamingKinesisEndpoint,
		STSEndpoint:         cfg.StreamingSTSEndpoint,
		Logger:              log,
	})
}

// streamingJobs registers the delivery job of the streaming destinations.
type streamingJobs struct {
	engine *billing.Engine
}

// register registers [streaming.JobDeliverEvent] on the streaming queue.
// A delivery runs once (lago DeliverEventJob retries nothing): the next
// refresh of the customer delivers again.
func (s streamingJobs) register(reg *jobs.Registry) error {
	for _, k := range streaming.JobKinds() {
		if err := jobs.Register(reg, jobs.Kind{Name: k, MaxAttempts: 1}, jobs.HandlerFunc[json.RawMessage](s.run)); err != nil {
			return err
		}
	}
	return nil
}

// run runs a delivery job with the engine's streaming service.
func (s streamingJobs) run(ctx context.Context, j *jobs.Job[json.RawMessage]) error {
	if s.engine.Streaming == nil {
		return jobs.Permanent(fmt.Errorf("streaming: kind %s: no producer: %w", j.Kind, core.ErrNotAllowed))
	}
	ok, err := s.engine.Streaming.HandleJob(ctx, port.Job{
		Kind: j.Kind, Queue: port.QueueStreaming, OrganizationID: j.OrganizationID, Args: j.Args, UniqueKey: j.UniqueKey,
	})
	if !ok {
		return jobs.Permanent(fmt.Errorf("streaming: kind %s: %w", j.Kind, core.ErrNotFound))
	}
	return err
}
