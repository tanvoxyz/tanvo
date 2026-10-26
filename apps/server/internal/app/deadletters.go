package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/ingest/kafka"
)

// ReplayInput are the options of [ReplayDeadLetters].
type ReplayInput struct {
	// OrganizationID is the organization whose dead letters are replayed.
	OrganizationID string
	// From and To bound the failure instant (From included, To excluded);
	// zero for no bound.
	From, To time.Time
	// ErrorCode keeps the dead letters with that code; empty for all.
	ErrorCode string
	// Replay produces the messages again; a dry run that counts them
	// otherwise.
	Replay bool
	// BatchSize bounds the messages produced at once.
	BatchSize int
}

// ReplayOutput is what [ReplayDeadLetters] found or did.
type ReplayOutput struct {
	// Pending counts the dead letters selected that are not replayed yet,
	// before the replay.
	Pending []kafka.DeadLetterCount
	// Result counts the replay's messages; zero in a dry run.
	Result kafka.ReplayResult
}

// ReplayDeadLetters counts the dead letters of in.OrganizationID that
// ClickHouse's events_dead_letter holds and have not been replayed, and
// with in.Replay produces their original messages again to their topics
// (architecture §7.1). It needs ClickHouse and the Kafka brokers.
func ReplayDeadLetters(ctx context.Context, cfg config.Config, in ReplayInput, tp trace.TracerProvider, mp metric.MeterProvider, prop propagation.TextMapPropagator) (ReplayOutput, error) {
	var out ReplayOutput
	org, err := core.ParseID[core.OrganizationID](in.OrganizationID)
	if err != nil {
		return out, fmt.Errorf("events replay-dead-letters: organization: %w", err)
	}
	if !in.From.IsZero() && !in.To.IsZero() && !in.From.Before(in.To) {
		return out, errors.New("events replay-dead-letters: --from must be before --to")
	}
	if cfg.ClickHouseURL == "" || len(cfg.KafkaBrokers) == 0 {
		return out, errors.New("events replay-dead-letters: needs TANVO_CLICKHOUSE_URL and TANVO_KAFKA_BROKERS")
	}
	conn, err := openClickHouse(ctx, cfg.ClickHouseURL)
	if err != nil {
		return out, err
	}
	defer func() { _ = conn.Close() }()
	r := kafka.Replayer{
		Topics:    topics(cfg),
		Store:     deadLetterStore{ch: clickhouse.New(conn, clickhouse.WithTracerProvider(tp))},
		BatchSize: in.BatchSize,
	}
	f := kafka.ReplayFilter{OrganizationID: org, From: in.From, To: in.To, ErrorCode: in.ErrorCode}
	if out.Pending, err = r.Count(ctx, f); err != nil {
		return out, fmt.Errorf("events replay-dead-letters: %w", err)
	}
	if !in.Replay {
		return out, nil
	}
	if r.Client, err = newKafkaClient(cfg, kafkaTelemetry{tp: tp, mp: mp, propagator: prop}, ""); err != nil {
		return out, err
	}
	defer r.Client.Close()
	if out.Result, err = r.Replay(ctx, f); err != nil {
		return out, fmt.Errorf("events replay-dead-letters: %w", err)
	}
	return out, nil
}
