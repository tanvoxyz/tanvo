// Package clickhouse is the ClickHouse event store: a [metering.Store] and
// an [eventstore.Writer] over the native protocol of clickhouse-go, for the
// ingest consumers' batched writes.
//
// # Schema
//
// events_raw is the ingestion ledger, sorted by its deduplication key
// (organization, external subscription, transaction ID) and not
// partitioned, so that merges collapse every copy of a key; events_enriched
// holds the rows the queries read, sorted by (organization, code,
// subscription, timestamp, transaction ID) and partitioned by month. Both
// are ReplacingMergeTree tables versioned by the ingestion sequence the
// writer assigns (monotonic per process, seeded by the clock in
// nanoseconds):
//
//   - events_raw's version decreases with the sequence for the copies of
//     [Store.Write], so that merges keep the first, and increases with it,
//     above every write, for the copies of [Store.Replace], so that the
//     latest replacement wins;
//   - events_enriched's version is the sequence, so that the latest copy of
//     an event (re-enrichment, replacement) wins, and is_deleted tombstones
//     the copy a replacement moved to another code or timestamp. Queries
//     read it FINAL in a subquery bounded by the sorting key, and apply
//     property filters after deduplication.
//
// events_raw also keeps the events' [metering.EventMetadata] (the API
// request's user agent and address), the only record of it for the events
// ingested through Kafka. bucket_repairs records the runs of
// [Store.RepairRecentBuckets].
//
// # Deduplication
//
// The deduplication key is (organization, external subscription,
// transaction ID), as in the PostgreSQL store: one transaction ID under
// two subscriptions is two events, and a copy of a key at another code or
// timestamp is a duplicate.
//
// ClickHouse has no unique constraint: [Store.Write] reads the ledger for
// the keys of the batch, drops the known ones and the repeats within the
// batch, then inserts events_enriched and events_raw, in that order, so
// that a failed write retried as a whole never reports its own events as
// duplicates. The check and the insert are not atomic: they rely on one
// writer at a time per key. The enriched-events topic is keyed by
// (organization, external subscription ID) (package ingest), so every copy
// of a key lands on one partition, in order, and has one consumer at a
// time, which sees the first copy before the later ones. A partition
// moving between consumers can briefly have two, the new one replaying
// what the old one had not committed: both write the same copies, which are
// one events_enriched row, and each writes a later copy of a key only after
// its own write of the earlier one, so finds it. No resolution between
// copies is needed. The API's direct writes go through the same topic.
//
// The keying caps the throughput of one subscription at one writer
// consumer, and the events without a subscription of an organization share
// one partition key (organization/): a subscription sending more than a
// consumer writes lags behind instead of spreading over consumers.
//
// [Store.Replace] reads the ledger for the current copy of each key,
// raises its sequences above every copy written under the key, tombstones
// in events_enriched the copies it moves to another code or timestamp, and
// recomputes the buckets of the new and the moved copies.
//
// There is one events_enriched row per accepted event, not per (event,
// charge filter): charge filters are matched at query time.
//
// # Usage buckets
//
// usage_buckets_15m holds the 15-minute buckets of the definitions in
// usage_bucket_definitions (see [eventstore.BucketDefiner]). They are not
// a materialized view: a view sees inserted blocks only, and could not take
// back the maximum or the latest value of a replaced event. Each row records the version of its bucket (the count of the
// bucket's writes: one above the highest version a write read), the writer
// (a random ID per write) and the highest sequence of the events it holds.
// Reads take the rows FINAL, without tombstones.
//
// After inserting its events, [Store.Write] maintains the buckets of the
// codes with definitions, in one of two ways:
//
//   - merge: in one statement per batch, the new events, sent as an
//     external table, are aggregated and combined with each touched
//     bucket's current rows (counts and sums add, the maximum and last
//     times take the greatest, the latest value the most recent by
//     timestamp, then sequence), written one version up. Its cost is
//     proportional to the batch and the rows of its buckets, not to the
//     events of the buckets. A bucket merges only when every new event's
//     sequence is above the highest sequence its rows hold: otherwise a
//     recompute since the insert may already hold them. After its insert,
//     the merge checks the rows below its version too: a recompute that
//     read its events and inserted, at the version the merge built on,
//     after the merge read the state, holds a sequence at or above the
//     merge's lowest, and the bucket is recomputed;
//   - recompute: the bucket is aggregated again from events_enriched FINAL,
//     one version up, and the current rows it no longer holds are
//     tombstoned. [Store.Replace], the buckets a merge cannot take, and the
//     retry heal below go this way, for the touched buckets only.
//
// Writers of one process serialize per bucket on in-process locks. Writers
// of different processes (a rebalance, a replacement, a backfill) check
// after their insert for rows
// of another writer at their version: a merge conflicts with any of them,
// a recompute with merges only (two recomputes at a version both hold
// every event stored before them). A conflicting bucket is recomputed a
// version higher, with a random backoff, at most ten rounds. Rows of a
// higher version need no check: every write above descends, version by
// version, from a write at this one. The check relies on the rows of the
// version still being there, which a background merge with higher
// versions could collapse in the milliseconds between insert and check;
// [Store.RepairBuckets] heals that.
//
// These rules converge provided each read sees every insert acknowledged
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
//
// [Store.Write] also heals the buckets of the duplicates whose stored copy
// is younger than the heal window ([WithHealWindow], an hour by default)
// and whose bucket holds no event as recent as the copy (no row records
// its sequence or a higher one): the retry of a write that failed between
// its insert and its maintenance recomputes them, and duplicates of
// maintained copies cost one read of the bucket state.
//
// [Store.RepairBuckets] is required for correctness: the checks above miss
// rows a background merge collapsed before them, a failed write may never
// be retried, and a replica may lag. It recomputes the buckets of every
// event stored since a watermark. Schedule it every 5 to 15 minutes, with
// the watermark at the start of the previous run minus a margin above the
// longest write and the clock skew between writers (10 minutes is ample);
// overlapping runs only recompute buckets again.
//
// [Store.DefineBuckets] writes a definition not ready, backfills it by
// recomputing its buckets in pages of a thousand, then writes it ready;
// writers maintain definitions that are not ready yet, and reads use ready
// ones only, read again after the rows so that a definition dropped
// meanwhile sends the read back to the events. Dimensions declared with
// values fold the others into one value (see
// [metering.BucketDefinition]); [Store.BucketStats] reports the
// cardinality of a definition's rows.
//
// # Latency
//
// Ingestion latency per write on one local node (OrbStack, a shared and
// loaded machine: compare rows, not absolute values), TestIngestLatency
// with TANVO_LATENCY=1: one writer, a cold bucket (empty) or a hot one
// (100,000 events), 0, 1 or 5 definitions on the code, batches of 1, 100
// and 1000 events. p50 / p95 / p99 in ms, then events/s:
//
//	defs bucket   batch=1                 batch=100               batch=1000
//	0    cold     14.9 / 20.6 / 33.9  63  17.4 / 25.1 / 31.0  5500  28.1 / 35.9 / 35.9 35207
//	0    hot      16.0 / 22.5 / 25.5  59  17.0 / 21.8 / 24.4  5688  23.6 / 43.1 / 43.1 38577
//	1    cold     53.7 / 64.8 / 93.7  18  51.2 / 59.2 / 60.0  1906  61.5 / 75.8 / 75.8 15798
//	1    hot      57.4 / 67.2 / 77.6  17  56.8 / 65.0 / 70.3  1755  63.5 / 71.4 / 71.4 15503
//	5    cold     53.4 / 62.1 / 82.3  18  56.9 / 71.2 / 78.1  1729  64.5 / 73.7 / 73.7 15012
//	5    hot      58.6 / 76.2 / 91.5  17  59.8 / 72.3 / 81.9  1647  72.1 / 80.2 / 80.2 13853
//
// A hot bucket now costs what a cold one does. The previous maintenance,
// which recomputed every touched bucket from its events, measured p50s of
// 88.7, 101 and 91.6 ms into a hot bucket with one definition and 210, 242
// and 267 ms with five (cold: 33 to 46 ms); server-side, a one-event write
// into a hot bucket with five definitions read 1,238 rows instead of
// 216,966 and took 130 ms of server time instead of 249. The merge costs
// a fixed planning overhead on cold buckets. Writes read the ledger once:
// copies under one deduplication key share a partition and need no
// resolution read after the insert, which the rows without definitions
// show (2 to 4 ms below the 17.5 to 20.8 ms measured with it).
// BenchmarkHotBucketWrite measures the hot path against the size of the
// bucket.
//
// Values of numeric aggregations are read as Decimal(76, 26): digits beyond
// 26 decimal places are truncated, where PostgreSQL's numeric keeps them.
package clickhouse
