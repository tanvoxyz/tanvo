// Package kafka runs ingestion over Kafka (architecture §7.1): the
// producers of the events.raw, events.enriched, dead letter and usage
// refresh topics, and the consumers that enrich raw events, write enriched
// events to the event store in batches and route in-advance events to
// billing.
//
// Every consumer runs a [Consumer]: it polls a batch, hands it to a
// [Handler] that decides each record's [Disposition], and commits, per
// partition, only the prefix of records that were processed. A record
// that will never succeed goes to the dead letter topic and counts as
// processed; a record that may succeed later is retried from its offset
// (the partition is rewound), unless it is older than [MaxRetryAge], in
// which case it goes to the dead letter topic too, as lago's
// events-processor does.
//
// The enrichment consumer reads events.raw, keyed by (organization,
// transaction ID), and produces to events.enriched keyed by
// (organization, external subscription ID) (see the partitioning section
// of package ingest): the writer consumer of a partition is the only
// writer of the usage buckets of its subscriptions. Consumers block
// rebalances during a poll's processing and commit by hand, so a live
// member never shares a partition; a member that lost its session can, and
// the event store stays correct under that overlap.
package kafka
