// Package postgres is the PostgreSQL event store: a [metering.Store] and an
// [eventstore.Writer] over pgx.
//
// # Schema
//
// The events table is partitioned by month of timestamp (declarative range
// partitioning), each month by hash of organization. Its primary key,
// (organization, subscription, code, timestamp, transaction ID), is the key
// the queries read events by and serves them as an index: every query is an
// index range scan of one subscription and code over a time window, pruned
// to the months of the window. A GIN index (jsonb_path_ops) on the
// properties serves charge filters and pricing group values, written as
// containment. The events' [metering.EventMetadata] is not stored: the
// store's raw event log (public.events), written in the same transaction,
// keeps it.
//
// A unique constraint on a partitioned table must include the partition
// keys, so ingestion deduplication by (organization, external subscription,
// transaction ID) cannot be a constraint of events: event_transactions,
// partitioned by hash of organization, is the ledger of the accepted keys,
// recording where each key's event is (code, timestamp). [Store.Write]
// inserts into the ledger and the events of the rows it accepted in one
// statement, so that concurrent writers of one key resolve on the ledger's
// primary key and the first wins. [Store.Replace] reads the ledger to find
// the copy a replacement moves to another code or timestamp, locks the
// buckets of both, locks the ledger rows and checks they did not change,
// then deletes the moved copy and points the ledger at the replacement in
// the statement storing it. [Migrate] refuses a ledger keyed by
// (organization, transaction ID), which predates the key.
//
// Monthly partitions are created by events_create_partitions, which writes
// create on demand for the months of their batches, and dropped by
// [Store.DropPartitions] (events_drop_partitions) for retention, which
// takes an ACCESS EXCLUSIVE lock on events for the time of the drop: run
// it off peak, with a lock_timeout. [Migrate]
// applies the schema; it is self-contained so that libs/store can take it
// over as migrations.
//
// The event store can live in a PostgreSQL schema of its own
// ([MigrateOptions].Schema and [WithSchema], the same name): every
// statement then qualifies the tables, the sequence, the functions and
// their calls with it, the functions qualify the partitions they create
// and drop, and the advisory-lock keys of the partition DDL and of the
// usage buckets include it, so that the store works whatever the
// search_path of its connections, in a transaction of a caller too, and
// two event stores of one database (another's events table in public, for
// one) stay apart. Without a schema, identifiers are unqualified and
// resolved by the search_path.
//
// # Usage buckets
//
// usage_buckets holds the 15-minute buckets of the definitions in
// usage_bucket_definitions (see [eventstore.BucketDefiner]), maintained in
// the transaction of every write:
//
//   - every [Store.Write] and [Store.Replace] first takes a shared advisory
//     lock per (organization, code) of its batch, then, when definitions
//     apply, an exclusive advisory lock per touched (organization,
//     subscription, code, bucket), in key order;
//   - [Store.Write] adds the events it accepted to their bucket rows in the
//     statement that stores them (INSERT … ON CONFLICT DO UPDATE: counts
//     and sums add, the maximum and the last times take the greatest, the
//     latest value the most recent by timestamp, then sequence);
//   - [Store.Replace] deletes the rows of the buckets it touched and
//     aggregates them again from the events, in later statements of its
//     transaction, so that a replaced copy taking back a maximum or a
//     latest value, or moving to other dimension values, corrects its
//     bucket;
//   - [Store.DefineBuckets] takes the (organization, code) lock exclusively
//     only to insert the definition not ready: the writers that ran before
//     have committed, and every later one maintains the new buckets. It
//     then backfills the buckets in chunks of the events in (subscription,
//     timestamp) order ([WithBackfillChunk]), each chunk in a transaction
//     of its own that takes the locks of its buckets, deletes their rows
//     and aggregates them again, alongside the writers. The definition is
//     then marked ready; [Store.ReadBuckets] reads ready definitions only,
//     and checks in the statement reading the rows that the definition is
//     still in place.
//
// The bucket locks serialize the writers of a bucket, so that each
// statement, under READ COMMITTED, reads the bucket rows and events the
// previous writer committed. They stay even though the enriched-events
// topic gives each subscription a single consumer: in ModePostgres the API
// writes directly, outside any partition; [Store.Replace] and the backfill
// of [Store.DefineBuckets] delete and aggregate again the buckets that
// writers add to; and a partition moving between consumers can briefly
// have two. The upsert alone is atomic per row, but not against a
// statement deleting and reaggregating the bucket from events it read
// before the upsert committed. A row is keyed by the SHA-256 of the
// canonical text of its dimensions (jsonb), which an index entry always
// holds whatever the length of the property values. Dimensions declared
// with values fold the others into one value (see
// [metering.BucketDefinition]); [Store.BucketStats] reports the
// cardinality of a definition's rows. events_drop_partitions drops the
// buckets of the months it drops.
//
// # Latency
//
// Ingestion latency per write on one local node (OrbStack, a shared and
// loaded machine), TestIngestLatency with TANVO_LATENCY=1 in a non-public
// schema: 1 or 4 concurrent writers of distinct subscriptions, a cold
// bucket (empty) or a hot one (100,000 events), 0, 1 or 5 definitions on
// the code. p50 / p99 in ms, then events/s:
//
//	defs bucket writers  batch=1             batch=100            batch=1000
//	0    cold   1        1.1 / 3.1   820     3.0 / 3.6   33240    20.7 / 41.4  44144
//	0    cold   4        1.5 / 3.8   2477    4.0 / 8.3   83614    29.6 / 41.6  130463
//	0    hot    1        0.8 / 1.5   1158    4.0 / 13.2  21371    19.7 / 28.7  46094
//	0    hot    4        1.7 / 14.3  1792    6.3 / 12.2  59601    27.1 / 40.0  139018
//	1    cold   1        1.2 / 3.1   796     4.9 / 5.5   20589    38.9 / 43.3  25810
//	1    cold   4        1.6 / 3.0   2349    6.4 / 11.0  58759    44.2 / 57.2  84633
//	1    hot    1        1.3 / 2.2   792     5.5 / 34.6  14885    37.3 / 39.9  26415
//	1    hot    4        1.6 / 2.5   2448    7.9 / 29.9  44037    44.8 / 62.7  84796
//	5    cold   1        1.4 / 3.4   660     11.7 / 19.0 8004     102.6 / 131.9 9384
//	5    cold   4        1.9 / 3.4   2034    12.9 / 21.7 29320    119.1 / 197.3 31863
//	5    hot    1        1.4 / 2.4   680     10.9 / 36.7 7535     96.6 / 104.9 10291
//	5    hot    4        1.8 / 3.8   2129    13.1 / 24.3 27594    101.8 / 146.2 37584
//
// The upsert costs in proportion to the batch and the definitions, not to
// the events of the buckets; the locks cost nothing measurable when the
// writers' subscriptions differ, as the enriched-events partitioning makes
// them.
package postgres
