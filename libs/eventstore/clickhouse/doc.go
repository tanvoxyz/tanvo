// Package clickhouse is the ClickHouse event store: a [metering.Store] and
// an [eventstore.Writer] over the native protocol of clickhouse-go, for the
// ingest consumers' batched writes.
//
// # Schema
//
// events_raw is the ingestion ledger, sorted by (organization, transaction
// ID); events_enriched holds the rows the queries read, sorted by
// (organization, code, subscription, timestamp, transaction ID) and
// partitioned by month. Both are ReplacingMergeTree tables versioned by the
// ingestion sequence the writer assigns (monotonic per process, seeded by
// the clock):
//
//   - events_raw's version decreases with the sequence, so that merges keep
//     the first accepted event of a transaction ID;
//   - events_enriched's version is the sequence, so that the latest copy of
//     an event (re-enrichment, replacement) wins. Queries read it FINAL in a
//     subquery bounded by the sorting key, and apply property filters after
//     deduplication.
//
// # Deduplication
//
// ClickHouse has no unique constraint: [Store.Write] reads the ledger for
// the transaction IDs of the batch, drops the known ones and the repeats
// within the batch, then inserts events_enriched and events_raw, in that
// order, so that a failed write retried as a whole never reports its own
// events as duplicates. The check and the insert are not atomic: the ingest
// consumers must give each transaction ID a single writer at a time, which
// the Kafka partition key, the deduplication key (organization,
// transaction ID), guarantees. Should two writers race anyway, the ledger
// still converges on the first event, and the enriched copies with the
// same timestamp collapse into one.
//
// There is one events_enriched row per accepted event, not per (event,
// charge filter): charge filters are matched at query time.
//
// Values of numeric aggregations are read as Decimal(76, 26): digits beyond
// 26 decimal places are truncated, where PostgreSQL's numeric keeps them.
package clickhouse
