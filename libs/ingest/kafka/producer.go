package kafka

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/tanvoxyz/tanvo/libs/ingest"
)

// produce encodes values and produces them synchronously, keyed by key.
func produce[T any](ctx context.Context, cl *kgo.Client, topic string, values []T, key func(T) []byte) error {
	if len(values) == 0 {
		return nil
	}
	recs := make([]*kgo.Record, len(values))
	for i, v := range values {
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("kafka: encode %s message: %w", topic, err)
		}
		recs[i] = &kgo.Record{Topic: topic, Key: key(v), Value: b}
	}
	if err := cl.ProduceSync(ctx, recs...).FirstErr(); err != nil {
		return fmt.Errorf("kafka: produce to %s: %w", topic, err)
	}
	return nil
}

// RawProducer produces to the raw events topic, keyed by (organization,
// transaction ID). It implements [ingest.RawProducer].
type RawProducer struct {
	// Client produces.
	Client *kgo.Client
	// Topic is the raw events topic.
	Topic string
}

var _ ingest.RawProducer = RawProducer{}

// ProduceRaw implements [ingest.RawProducer].
func (p RawProducer) ProduceRaw(ctx context.Context, msgs []ingest.RawMessage) error {
	return produce(ctx, p.Client, p.Topic, msgs, func(m ingest.RawMessage) []byte { return m.Key() })
}

// EnrichedSink receives enriched events.
type EnrichedSink interface {
	// ProduceEnriched delivers the events; an error means some may not
	// have been.
	ProduceEnriched(ctx context.Context, events []*ingest.Enriched) error
}

// EnrichedProducer produces to the enriched events topic, keyed by
// (organization, external subscription ID) ([ingest.Enriched.Key]).
type EnrichedProducer struct {
	// Client produces.
	Client *kgo.Client
	// Topic is the enriched events topic.
	Topic string
}

var _ EnrichedSink = EnrichedProducer{}

// ProduceEnriched implements [EnrichedSink].
func (p EnrichedProducer) ProduceEnriched(ctx context.Context, events []*ingest.Enriched) error {
	return produce(ctx, p.Client, p.Topic, events, (*ingest.Enriched).Key)
}

// SignalProducer produces usage refresh signals keyed by (organization,
// customer), so that one consumer sees a customer's signals in order. It
// implements [ingest.UsageSignaler].
type SignalProducer struct {
	// Client produces.
	Client *kgo.Client
	// Topic is the usage refresh topic.
	Topic string
}

var _ ingest.UsageSignaler = SignalProducer{}

// SignalUsage implements [ingest.UsageSignaler].
func (p SignalProducer) SignalUsage(ctx context.Context, signals []ingest.UsageSignal) error {
	return produce(ctx, p.Client, p.Topic, signals, func(s ingest.UsageSignal) []byte {
		return []byte(s.OrganizationID.String() + "/" + s.CustomerID.String())
	})
}
