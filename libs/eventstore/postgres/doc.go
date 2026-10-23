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
// containment.
//
// A unique constraint on a partitioned table must include the partition
// keys, so ingestion deduplication by (organization, transaction ID) cannot
// be a constraint of events: event_transactions, partitioned by hash of
// organization, is the ledger of accepted transaction IDs. [Store.Write]
// inserts into the ledger and the events of the rows it accepted in one
// statement, so that concurrent writers of one transaction ID resolve on
// the ledger's primary key and the first wins.
//
// Monthly partitions are created by events_create_partitions, which writes
// create on demand for the months of their batches, and dropped by
// events_drop_partitions for retention. [Migrate] applies the schema; it is
// self-contained so that libs/store can take it over as migrations.
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
// previous writer committed. A row is keyed by the SHA-256 of the
// canonical text of its dimensions (jsonb), which an index entry always
// holds whatever the length of the property values. Dimensions declared
// with values fold the others into one value (see
// [metering.BucketDefinition]); [Store.BucketStats] reports the
// cardinality of a definition's rows. events_drop_partitions drops the
// buckets of the months it drops.
package postgres
