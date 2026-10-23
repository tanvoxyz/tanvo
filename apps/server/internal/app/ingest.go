package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/eventstore/postgres"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/ingest/kafka"
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
// Entries expire after ingest.DefaultCacheTTL; catalog changes are not
// yet signalled to the cache.
func newCatalog(tx port.Transactor) *ingest.Cache {
	return ingest.NewCache(ingest.RepoCatalog{Transactor: tx})
}

// newIngestService returns the intake of the API role: postgres mode
// writes the raw log and the enriched events in one store transaction;
// kafka mode produces to the raw topic with producer (nil when no
// organization ingests through Kafka). In-advance billing and usage
// signals are not wired yet (libs/billing).
func newIngestService(cfg config.Config, tx port.Transactor, modeOf func(core.OrganizationID) ingest.Mode, producer *kgo.Client) (*ingest.Service, error) {
	ic := ingest.Config{
		Catalog:  newCatalog(tx),
		Mode:     ingest.Mode(cfg.IngestMode),
		ModeFor:  modeOf,
		Postgres: txStore{tx: tx, schema: cfg.EventStoreSchema},
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
// the PostgreSQL event store, written in one store transaction.
type txStore struct {
	tx     port.Transactor
	schema string
}

var _ ingest.TxStore = txStore{}

// InTx implements [ingest.TxStore].
func (s txStore) InTx(ctx context.Context, fn func(ctx context.Context, log event.Log, enriched ingest.EventWriter) error) error {
	return s.tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		tx, err := captureTx(ctx, r)
		if err != nil {
			return err
		}
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
// ClickHouse writer (enriched topic → ClickHouse and usage signals,
// group <prefix>-write). producer produces their outputs and dead
// letters. The in-advance billing and usage refresh consumers need
// libs/billing and are not wired yet.
func newConsumers(cfg config.Config, tx port.Transactor, ch *clickhouse.Store, producer *kgo.Client, tel kafkaTelemetry, log *slog.Logger) (*consumers, error) {
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
		})
		if err != nil {
			return fmt.Errorf("kafka: consumer %s: %w", name, err)
		}
		out.run = append(out.run, c)
		return nil
	}
	enricher := ingest.NewEnricher(newCatalog(tx), nil, nil)
	if err := add("enrich", t.Raw, kafka.Enrichment(enricher, kafka.EnrichedProducer{Client: producer, Topic: t.Enriched}, cfg.IngestEnrichConcurrency)); err != nil {
		out.close()
		return nil, err
	}
	if err := add("write", t.Enriched, kafka.Writer(ch, kafka.SignalProducer{Client: producer, Topic: t.Signals}, log)); err != nil {
		out.close()
		return nil, err
	}
	return out, nil
}
