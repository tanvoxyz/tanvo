// Package ingest takes usage events in (architecture §7.1, domain §10): it
// validates and normalizes them, evaluates billable metric expressions,
// deduplicates them on (organization, external subscription ID,
// transaction ID) and stores or streams them, depending on the ingestion
// mode.
//
// # Intake
//
// [Service.Create] and [Service.CreateBatch] are the event API, minus HTTP
// (apps/server decodes requests into [Input] and renders the results). A
// batch is all or nothing: when one event is invalid, nothing is stored
// and a [*BatchError] reports every invalid event by position, as
// lago-api's Events::CreateBatchService does. A re-sent event (same
// organization, external subscription ID and transaction ID, whatever its
// code and timestamp) is not an error: it is acknowledged as a duplicate
// ([Outcome.Duplicate]) and dropped (domain §24 item 11). One transaction
// ID under two subscriptions is two events.
//
// Two modes write accepted events:
//
//   - [ModePostgres] writes the raw event ([event.Log]) and its enriched
//     row ([eventstore.Writer]) in one transaction ([TxStore]), and routes
//     in-advance events ([InAdvanceBiller]) and usage refresh signals
//     ([UsageSignaler]) inside the same transaction, so that they commit
//     with the events or not at all;
//   - [ModeKafka] produces raw events to the events.raw topic
//     ([RawProducer]); the consumers of package kafka enrich them, write
//     them to ClickHouse in batches, route in-advance events and signal
//     usage refreshes. Duplicates of stored events are dropped by the
//     ClickHouse writer's ledger, after the API answered.
//
// # Partitioning
//
// The two topics are keyed apart. events.raw is keyed by the
// deduplication key, (organization, external subscription ID, transaction
// ID) ([PartitionKey]): enrichment is stateless, so the events of a busy
// subscription spread over the enrichment consumers, while the copies of a
// key stay in order on one partition. events.enriched is keyed by (organization,
// external subscription ID of the payload) ([SubscriptionPartitionKey],
// [Enriched.Key]): one writer consumer stores every event of a
// subscription, so that it maintains the subscription's usage buckets
// alone and merges each batch into them instead of recomputing them. The
// key is read from the payload, not from the subscription enrichment
// resolved, so that it does not depend on the catalog; an event without a
// subscription ID keys on the organization alone.
//
// Both keys hold the subscription of the deduplication key, so every
// copy of a key goes through one partition of each topic, in order: the
// writer consumer of a subscription sees the first copy of a key before
// the later ones, and the event store needs no resolution between copies.
// A partition moving between consumers (the consumers block rebalances
// while a batch is processed, but a member that lost its session can still
// be writing) briefly gives a subscription two writers, which replay the
// same copies; the event stores tolerate it.
//
// The enriched keying caps the throughput of one subscription at one
// writer consumer: a subscription sending more events than a consumer
// writes lags behind instead of spreading over consumers. The events
// without a subscription ID of an organization share one key, and one
// consumer, too.
//
// # Enrichment
//
// An [Enricher] resolves an event to its subscription (by external
// subscription ID and timestamp, with the recurring-metric fallback of
// domain §10.2), its billable metric (by code), the metric's expression
// value, the aggregated value, the precise amount and the grouping keys,
// and looks up the metric's charges on the subscription's plan only to
// decide whether the event goes to in-advance billing. It produces one
// [Enriched] row per event, with no charge or filter column (architecture
// §7.2). Catalog reads go through a [Catalog]; [Cache] keeps them in memory
// and is refreshed by change notifications ([Cache.Invalidate]).
package ingest
