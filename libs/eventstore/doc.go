// Package eventstore holds the event stores of Tanvo: the implementations of
// [metering.Store] that run the aggregation queries of the billable metrics
// in a database, and the [Writer] the ingestion paths store events with.
//
// Two backends implement both interfaces:
//
//   - package postgres keeps the events in a PostgreSQL table partitioned by
//     month and by hash of organization, with a deduplication ledger keyed by
//     (organization, transaction ID);
//   - package clickhouse keeps them in ClickHouse ReplacingMergeTree tables,
//     events_raw (the ledger) and events_enriched (the rows the queries
//     read), written in batches by the ingest consumers.
//
// Both pass the conformance cases of package metering/storetest, which
// define the semantics of every query against the reference
// [metering.MemoryStore].
//
// Both also maintain the usage buckets of [BucketDefiner] definitions with
// every write and implement [metering.BucketReader] over them; package
// metering answers eligible aggregations from them through
// [metering.UsageBucketStore], with the results of the events.
//
// # Deduplication
//
// Ingestion deduplicates by (organization, transaction ID): the first
// accepted event wins, and [Writer.Write] reports every later event with a
// known transaction ID as a duplicate without storing it. Corrections and
// re-enrichment go through [Replacer.Replace], which stores events as the
// latest copy of their (organization, subscription, code, transaction ID,
// timestamp) key — the key the queries read events by, as
// [metering.Store] specifies.
package eventstore
