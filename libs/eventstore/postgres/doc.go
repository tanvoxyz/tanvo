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
package postgres
