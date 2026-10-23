// Package ingest takes usage events in (architecture §7.1, domain §10): it
// validates and normalizes them, evaluates billable metric expressions,
// deduplicates them on (organization, transaction ID) and stores or
// streams them, depending on the ingestion mode.
//
// # Intake
//
// [Service.Create] and [Service.CreateBatch] are the event API, minus HTTP
// (apps/server decodes requests into [Input] and renders the results). A
// batch is all or nothing: when one event is invalid, nothing is stored
// and a [*BatchError] reports every invalid event by position, as
// lago-api's Events::CreateBatchService does. A re-sent transaction ID is
// not an error: it is acknowledged as a duplicate ([Outcome.Duplicate])
// and dropped (domain §24 item 11).
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
