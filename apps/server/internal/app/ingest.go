package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/eventstore/postgres"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/ingest/kafka"
	"github.com/tanvoxyz/tanvo/libs/store"
)

// ingestModes returns the ingestion mode of each organization
// (architecture §6): TANVO_INGEST_KAFKA_ORGANIZATIONS and
// TANVO_INGEST_POSTGRES_ORGANIZATIONS override TANVO_INGEST_MODE.
func ingestModes(cfg config.Config) (func(core.OrganizationID) ingest.Mode, error) {
	overrides := map[core.OrganizationID]ingest.Mode{}
	for mode, orgs := range map[ingest.Mode][]string{ingest.ModeKafka: cfg.IngestKafkaOrganizations, ingest.ModePostgres: cfg.IngestPostgresOrganizations} {
		for _, s := range orgs {
			id, err := core.ParseID[core.OrganizationID](s)
			if err != nil {
				return nil, fmt.Errorf("ingest: organization %q: %w", s, err)
			}
			overrides[id] = mode
		}
	}
	def := ingest.Mode(cfg.IngestMode)
	return func(org core.OrganizationID) ingest.Mode {
		if m, ok := overrides[org]; ok {
			return m
		}
		return def
	}, nil
}

// newCatalog returns the catalog ingestion reads, cached per process.
// changes invalidates the entries a catalog change makes stale; the
// others expire after ingest.DefaultCacheTTL.
func newCatalog(tx port.Transactor, changes *catalogChanges) *ingest.Cache {
	return changes.register(ingest.NewCache(ingest.RepoCatalog{Transactor: tx}))
}

// newIngestService returns the intake of the API role: postgres mode
// writes the raw log and the enriched events in one store transaction;
// kafka mode produces to the raw topic with producer (nil when no
// organization ingests through Kafka). In postgres mode the in-advance
// billing jobs and the usage signals of the events commit with them.
func newIngestService(cfg config.Config, st *store.Store, engine *billing.Engine, modeOf func(core.OrganizationID) ingest.Mode, producer *kgo.Client, changes *catalogChanges) (*ingest.Service, error) {
	ic := ingest.Config{
		Catalog:   newCatalog(st, changes),
		Mode:      ingest.Mode(cfg.IngestMode),
		ModeFor:   modeOf,
		Postgres:  txStore{store: st, schema: cfg.EventStoreSchema},
		InAdvance: inAdvanceBiller{engine: engine},
		Signals:   usageTracker{engine: engine},
	}
	if producer != nil {
		ic.Producer = kafka.RawProducer{Client: producer, Topic: topics(cfg).Raw}
	}
	s, err := ingest.NewService(ic)
	if err != nil {
		return nil, fmt.Errorf("ingest: %w", err)
	}
	return s, nil
}

// txStore is the storage of postgres mode: the store's raw event log and
// the PostgreSQL event store over the same pgx transaction, so that both
// commit together.
type txStore struct {
	store  *store.Store
	schema string
}

var _ ingest.TxStore = txStore{}

// InTx implements [ingest.TxStore].
func (s txStore) InTx(ctx context.Context, fn func(ctx context.Context, log event.Log, enriched ingest.EventWriter) error) error {
	return s.store.InPgxTx(ctx, func(ctx context.Context, r port.Repos, tx pgx.Tx) error {
		return fn(ctx, r.Events, postgres.New(tx, postgres.WithSchema(s.schema)))
	})
}

// consumers are the Kafka consumers of the consumer role and their
// clients.
type consumers struct {
	run     []*kafka.Consumer
	clients []*kgo.Client
}

// close closes the consumers' clients.
func (c *consumers) close() {
	for _, cl := range c.clients {
		cl.Close()
	}
}

// newConsumers creates the consumer role's pipelines (architecture §6.2):
// enrichment (raw topic → enriched topic, group <prefix>-enrich) and the
// ClickHouse writer (enriched topic → ClickHouse, then the in-advance
// billing jobs of the stored events and the usage signals, group
// <prefix>-write), the usage refresh consumer (usage signals →
// billing, group <prefix>-signals) and the dead letter store (dead
// letters → ClickHouse's events_dead_letter, group
// <prefix>-dead-letters). producer produces their outputs and
// dead letters.
func newConsumers(cfg config.Config, tx port.Transactor, ch *clickhouse.Store, engine *billing.Engine, producer *kgo.Client, tel kafkaTelemetry, log *slog.Logger, changes *catalogChanges) (*consumers, error) {
	t := topics(cfg)
	out := &consumers{}
	add := func(name, topic string, h kafka.Handler) error {
		cl, err := newKafkaClient(cfg, tel, group(cfg, name), kgo.ConsumeTopics(topic))
		if err != nil {
			return err
		}
		out.clients = append(out.clients, cl)
		c, err := kafka.NewConsumer(kafka.ConsumerConfig{
			Client:          cl,
			DeadLetters:     producer,
			DeadLetterTopic: t.DeadLetter,
			Handler:         h,
			Logger:          log.With("consumer", name),
			MeterProvider:   tel.mp,
		})
		if err != nil {
			return fmt.Errorf("kafka: consumer %s: %w", name, err)
		}
		out.run = append(out.run, c)
		return nil
	}
	enricher := ingest.NewEnricher(newCatalog(tx, changes), nil, nil)
	if err := add("enrich", t.Raw, kafka.Enrichment(enricher, kafka.EnrichedProducer{Client: producer, Topic: t.Enriched}, cfg.IngestEnrichConcurrency)); err != nil {
		out.close()
		return nil, err
	}
	write := kafka.Writer(ch, kafka.SignalProducer{Client: producer, Topic: t.Signals}, log)
	if err := add("write", t.Enriched, writeThenBill(write, kafka.InAdvance(inAdvanceBiller{engine: engine}))); err != nil {
		out.close()
		return nil, err
	}
	if err := add("signals", t.Signals, usageSignals(usageTracker{engine: engine}, log)); err != nil {
		out.close()
		return nil, err
	}
	if err := add("dead-letters", t.DeadLetter, kafka.DeadLetters(deadLetterStore{ch: ch}, nil, log)); err != nil {
		out.close()
		return nil, err
	}
	return out, nil
}

// deadLetterStore stores dead letters in ClickHouse's events_dead_letter
// and reads them back for replays.
type deadLetterStore struct {
	ch *clickhouse.Store
}

var (
	_ kafka.DeadLetterSink  = deadLetterStore{}
	_ kafka.DeadLetterStore = deadLetterStore{}
)

// StoreDeadLetters implements [kafka.DeadLetterSink].
func (s deadLetterStore) StoreDeadLetters(ctx context.Context, records []ingest.DeadLetterRecord) error {
	rows := make([]clickhouse.DeadLetter, len(records))
	for i, r := range records {
		rows[i] = clickhouse.DeadLetter{
			OrganizationID: r.OrganizationID, SubscriptionID: r.ExternalSubscriptionID, Code: r.Code, TransactionID: r.TransactionID,
			Timestamp: r.Timestamp, IngestedAt: r.IngestedAt, FailedAt: r.FailedAt, Event: string(r.Event),
			ErrorCode: r.ErrorCode, ErrorMessage: r.ErrorMessage, Topic: r.Topic, Partition: r.Partition, Offset: r.Offset,
		}
	}
	return s.ch.WriteDeadLetters(ctx, rows)
}

func deadLetterFilter(f kafka.ReplayFilter) clickhouse.DeadLetterFilter {
	return clickhouse.DeadLetterFilter{OrganizationID: f.OrganizationID.String(), FailedFrom: f.From, FailedTo: f.To, ErrorCode: f.ErrorCode}
}

// PendingDeadLetters implements [kafka.DeadLetterStore].
func (s deadLetterStore) PendingDeadLetters(ctx context.Context, f kafka.ReplayFilter, after *ingest.DeadLetterRecord, limit int) ([]ingest.DeadLetterRecord, error) {
	var pos *clickhouse.DeadLetterPosition
	if after != nil {
		pos = &clickhouse.DeadLetterPosition{FailedAt: after.FailedAt, Topic: after.Topic, Partition: after.Partition, Offset: after.Offset}
	}
	rows, err := s.ch.PendingDeadLetters(ctx, deadLetterFilter(f), pos, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ingest.DeadLetterRecord, len(rows))
	for i, r := range rows {
		out[i] = ingest.DeadLetterRecord{
			DeadLetter: ingest.DeadLetter{
				Event: []byte(r.Event), Topic: r.Topic, Partition: r.Partition, Offset: r.Offset,
				ErrorCode: r.ErrorCode, ErrorMessage: r.ErrorMessage, FailedAt: r.FailedAt,
			},
			OrganizationID: r.OrganizationID, ExternalSubscriptionID: r.SubscriptionID, Code: r.Code, TransactionID: r.TransactionID,
			Timestamp: r.Timestamp, IngestedAt: r.IngestedAt,
		}
	}
	return out, nil
}

// CountPendingDeadLetters implements [kafka.DeadLetterStore].
func (s deadLetterStore) CountPendingDeadLetters(ctx context.Context, f kafka.ReplayFilter) ([]kafka.DeadLetterCount, error) {
	counts, err := s.ch.CountPendingDeadLetters(ctx, deadLetterFilter(f))
	if err != nil {
		return nil, err
	}
	out := make([]kafka.DeadLetterCount, len(counts))
	for i, c := range counts {
		out[i] = kafka.DeadLetterCount(c)
	}
	return out, nil
}

// MarkReplayed implements [kafka.DeadLetterStore].
func (s deadLetterStore) MarkReplayed(ctx context.Context, dead []ingest.DeadLetterRecord, at time.Time) error {
	rows := make([]clickhouse.DeadLetter, len(dead))
	for i, d := range dead {
		rows[i] = clickhouse.DeadLetter{OrganizationID: d.OrganizationID, Topic: d.Topic, Partition: d.Partition, Offset: d.Offset}
	}
	return s.ch.MarkDeadLettersReplayed(ctx, rows, at)
}
