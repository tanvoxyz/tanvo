package kafka

import (
	"context"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/tanvoxyz/tanvo/libs/ingest"
)

// scopeName is the instrumentation scope of the package's metrics.
const scopeName = "github.com/tanvoxyz/tanvo/libs/ingest/kafka"

// Attribute keys of the package's metrics. The consumer group and the
// topic use the messaging semantic conventions; error.type is the bounded
// error code of a record (the dead letter codes of package ingest).
const (
	// AttrGroup is the consumer group.
	AttrGroup = attribute.Key("messaging.consumer.group.name")
	// AttrTopic is the topic.
	AttrTopic = attribute.Key("messaging.destination.name")
	// AttrOutcome is what became of a record: "done", "retry" or "dead".
	AttrOutcome = attribute.Key("tanvo.ingest.outcome")
	// AttrErrorType is the error code of a retried or dead record
	// ([ingest.ErrorDecode], …): "unknown" for none, "other" for a code
	// outside the package's set.
	AttrErrorType = attribute.Key("error.type")
)

// Outcomes ([AttrOutcome]).
const (
	outcomeDone  = "done"
	outcomeRetry = "retry"
	outcomeDead  = "dead"
)

// codeDeadLetters is the error code of the records of a batch retried
// because its dead letters could not be produced.
const codeDeadLetters = "produce_dead_letters"

// boundedCode returns the error.type of a record's error code: the code
// when it is one of the package's, "unknown" when empty and "other"
// otherwise, so that the attribute stays bounded.
func boundedCode(code string) string {
	switch code {
	case ingest.ErrorDecode, ingest.ErrorExpression, ingest.ErrorCatalog, ingest.ErrorProduce,
		ingest.ErrorWrite, ingest.ErrorReject, ingest.ErrorInAdvance, codeDeadLetters:
		return code
	case "":
		return "unknown"
	default:
		return "other"
	}
}

// DefaultLagInterval is the period of the consumer lag reports.
const DefaultLagInterval = 15 * time.Second

// outcomeKey counts the records of one outcome and error code.
type outcomeKey struct {
	outcome, code string
}

// meters are a consumer's instruments.
type meters struct {
	group    string
	records  metric.Int64Counter
	lag      metric.Int64Gauge
	retryAge metric.Float64Gauge
	// enabled reports a meter provider other than the no-op one: the lag
	// is only fetched when it is reported.
	enabled bool
}

func newMeters(mp metric.MeterProvider, group string) meters {
	m := meters{group: group, enabled: mp != nil}
	if mp == nil {
		mp = noop.NewMeterProvider()
	}
	meter := mp.Meter(scopeName)
	fallback := noop.NewMeterProvider().Meter(scopeName)
	var err error
	// A meter returns a usable instrument along with its error; the no-op
	// one stands in when it does not.
	if m.records, err = meter.Int64Counter("tanvo.ingest.records",
		metric.WithDescription("Records handled by an ingest consumer, by outcome and error code."),
		metric.WithUnit("{record}")); err != nil {
		m.records, _ = fallback.Int64Counter("tanvo.ingest.records")
	}
	if m.lag, err = meter.Int64Gauge("tanvo.ingest.consumer.lag",
		metric.WithDescription("Records of a topic the consumer group has not committed yet."),
		metric.WithUnit("{record}")); err != nil {
		m.lag, _ = fallback.Int64Gauge("tanvo.ingest.consumer.lag")
	}
	if m.retryAge, err = meter.Float64Gauge("tanvo.ingest.retry.age",
		metric.WithDescription("Age of the oldest record the consumer is retrying, from its ingestion; zero when none is."),
		metric.WithUnit("s")); err != nil {
		m.retryAge, _ = fallback.Float64Gauge("tanvo.ingest.retry.age")
	}
	return m
}

// count records the outcomes of a batch.
func (m meters) count(ctx context.Context, counts map[outcomeKey]int64) {
	for k, n := range counts {
		attrs := []attribute.KeyValue{AttrGroup.String(m.group), AttrOutcome.String(k.outcome)}
		if k.outcome != outcomeDone {
			attrs = append(attrs, AttrErrorType.String(boundedCode(k.code)))
		}
		m.records.Add(ctx, n, metric.WithAttributes(attrs...))
	}
}

// retrying records the age of the oldest retried record of a batch.
func (m meters) retrying(ctx context.Context, age time.Duration) {
	m.retryAge.Record(ctx, max(age, 0).Seconds(), metric.WithAttributes(AttrGroup.String(m.group)))
}

// reportLag records the group's lag per topic every interval until ctx
// ends. A failed fetch is logged and the gauge keeps its last value.
func (c *Consumer) reportLag(ctx context.Context, interval time.Duration) {
	adm := kadm.NewClient(c.cfg.Client)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		lctx, cancel := context.WithTimeout(ctx, interval)
		lags, err := adm.Lag(lctx, c.m.group)
		cancel()
		if err == nil {
			err = lags.Error()
		}
		if err != nil {
			if ctx.Err() == nil {
				c.cfg.Logger.WarnContext(ctx, "kafka: consumer lag", "group", c.m.group, "error", err)
			}
			continue
		}
		for _, l := range lags {
			for _, tl := range l.Lag.TotalByTopic() {
				c.m.lag.Record(ctx, tl.Lag, metric.WithAttributes(AttrGroup.String(c.m.group), AttrTopic.String(tl.Topic)))
			}
		}
	}
}
