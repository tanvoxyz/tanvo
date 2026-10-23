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
package kafka
