package app

import (
	"context"
	"crypto/tls"
	"fmt"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"
	"github.com/twmb/franz-go/plugin/kotel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/libs/ingest/kafka"
)

// kafkaTelemetry are the providers the Kafka clients report to.
type kafkaTelemetry struct {
	tp         trace.TracerProvider
	mp         metric.MeterProvider
	propagator propagation.TextMapPropagator
}

// topics returns the ingestion topics, with TANVO_KAFKA_TOPIC_PREFIX.
func topics(cfg config.Config) kafka.Topics {
	t := kafka.DefaultTopics()
	p := cfg.KafkaTopicPrefix
	return kafka.Topics{Raw: p + t.Raw, Enriched: p + t.Enriched, DeadLetter: p + t.DeadLetter, Signals: p + t.Signals}
}

// group returns the name of a consumer group, with
// TANVO_KAFKA_CONSUMER_GROUP_PREFIX.
func group(cfg config.Config, name string) string {
	return cfg.KafkaConsumerGroupPrefix + "-" + name
}

// newKafkaClient creates a client of the brokers with TLS and SASL as
// configured, traced and measured by kotel. groupName is the consumer
// group of a consuming client, empty for a producer.
func newKafkaClient(cfg config.Config, tel kafkaTelemetry, groupName string, opts ...kgo.Opt) (*kgo.Client, error) {
	topts := []kotel.TracerOpt{kotel.TracerProvider(tel.tp), kotel.TracerPropagator(tel.propagator), kotel.ClientID(cfg.KafkaClientID)}
	if groupName != "" {
		topts = append(topts, kotel.ConsumerGroup(groupName), kotel.LinkSpans())
	}
	k := kotel.NewKotel(kotel.WithTracer(kotel.NewTracer(topts...)), kotel.WithMeter(kotel.NewMeter(kotel.MeterProvider(tel.mp))))
	base := []kgo.Opt{
		kgo.SeedBrokers(cfg.KafkaBrokers...),
		kgo.ClientID(cfg.KafkaClientID),
		kgo.WithHooks(k.Hooks()...),
	}
	if cfg.KafkaTLS {
		base = append(base, kgo.DialTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}))
	}
	user, pass := cfg.KafkaSASLUsername, cfg.KafkaSASLPassword
	switch cfg.KafkaSASLMechanism {
	case "":
	case "plain":
		base = append(base, kgo.SASL(plain.Auth{User: user, Pass: pass}.AsMechanism()))
	case "scram-sha-256":
		base = append(base, kgo.SASL(scram.Auth{User: user, Pass: pass}.AsSha256Mechanism()))
	case "scram-sha-512":
		base = append(base, kgo.SASL(scram.Auth{User: user, Pass: pass}.AsSha512Mechanism()))
	default:
		return nil, fmt.Errorf("kafka: unknown SASL mechanism %q", cfg.KafkaSASLMechanism)
	}
	if groupName != "" {
		base = append(base, kgo.ConsumerGroup(groupName), kgo.BlockRebalanceOnPoll(), kgo.DisableAutoCommit())
	}
	cl, err := kgo.NewClient(append(base, opts...)...)
	if err != nil {
		return nil, fmt.Errorf("kafka: client: %w", err)
	}
	return cl, nil
}

// ensureTopics creates the ingestion topics when
// TANVO_KAFKA_CREATE_TOPICS is set.
func ensureTopics(ctx context.Context, cfg config.Config, cl *kgo.Client) error {
	if !cfg.KafkaCreateTopics {
		return nil
	}
	return kafka.EnsureTopics(ctx, cl, cfg.KafkaTopicPartitions, cfg.KafkaTopicReplicas, topics(cfg).All()...)
}
