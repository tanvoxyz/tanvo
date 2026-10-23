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
// # Usage buckets
//
// usage_buckets_15m holds the 15-minute buckets of the definitions in
// usage_bucket_definitions (see [eventstore.BucketDefiner]). They are not
// a materialized view: a view sees inserted blocks only, and could not take
// back the maximum or the latest value of a replaced event. Instead, after
// inserting its events, every [Store.Write] and [Store.Replace] reads the
// definitions of its codes and recomputes the buckets it touched from
// events_enriched FINAL, at a cost proportional to the events of those
// buckets. Every row of a recomputed bucket carries the same version, the
// sum of the sequences of the bucket's events: events never leave their
// bucket (the timestamp is part of their key) and a replacement raises the
// sequence, so the version grows with every change. Reads keep, of the
// rows FINAL, those of the highest version of each bucket; [Store.Replace]
// also tombstones (is_deleted) the rows that version no longer holds, which
// merges then drop. The version relies on events never leaving the store:
// deleting events (other than dropping whole months with their buckets)
// would lower it, and would need another versioning first.
//
// Concurrent writers of a bucket converge on the recomputation reading the
// most events provided each recompute reads every insert acknowledged
// before it. The maintenance therefore inserts with insert_quorum = 'auto'
// and reads with select_sequential_consistency = 1, which replicated tables
// (ReplicatedMergeTree, SharedMergeTree) need and a single node ignores.
// Every other read (usage queries, bucket reads, the deduplication lookup)
// is sequentially consistent too, so a lagging replica does not bill
// without events or buckets a quorum acknowledged. These settings are
// tested on a single node only; on a replicated deployment check whether
// the server version needs insert_quorum_parallel = 0 for
// select_sequential_consistency to hold, and the repair sweep's watermark
// should start a margin before the previous sweep, as it is the writers'
// clock.
// [Store.Write] also recomputes the buckets of the events it finds already
// stored, so that the retry of a write that failed between its insert and
// its maintenance heals them, and [Store.RepairBuckets] recomputes the
// buckets of every event stored since a watermark, for a periodic job to
// catch what remains (a retry that never came, a replica that lagged).
//
// The recompute reads every event of the touched buckets, so a write costs
// in proportion to the events already in its buckets: on a single local
// node (BenchmarkHotBucketWrite), a one-event write takes about 35 ms into
// an empty bucket, 57 ms into one of 10,000 events and 142 ms into one of
// 100,000, about 1 µs per event of the bucket over a fixed cost. A bucket
// is 15 minutes of one subscription and one code, so the cost grows with
// the traffic of a subscription times the number of batches it arrives in,
// not with history; batching the events of a hot subscription bounds it.
//
// [Store.DefineBuckets] writes a definition not ready, backfills it, then
// writes it ready; writers maintain definitions that are not ready yet, and
// reads use ready ones only, read again after the rows so that a definition
// dropped meanwhile sends the read back to the events. Dimensions declared
// with values fold the others into one value (see
// [metering.BucketDefinition]); [Store.BucketStats] reports the
// cardinality of a definition's rows.
//
// Values of numeric aggregations are read as Decimal(76, 26): digits beyond
// 26 decimal places are truncated, where PostgreSQL's numeric keeps them.
package clickhouse
