// Package event models usage events (domain §10.1): the ingestion
// contract, its validation and normalization, and [Log], the append-only
// log that deduplicates on (organization, external_subscription_id,
// transaction_id).
//
// This package does not aggregate events. Usage aggregation reads enriched
// events through the metering.Store port of libs/metering, which owns the
// enriched event shape and the aggregation queries; the server wires a Log
// and a metering.Store over the same storage.
package event
