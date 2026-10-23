// Package port declares the ports the domain needs from its environment:
// the transaction boundary with every repository ([Transactor], [Repos]),
// advisory locks, document sequences, the job queue, the clock and ID
// generator (re-exported from core), the document renderer, the blob store,
// the mailer and the webhook emitter.
//
// libs/store implements [Transactor] on PostgreSQL and libs/core/memstore
// in memory; apps/server wires the other ports.
//
// The event store is not a port of this package: aggregation over usage
// events goes through metering.Store in libs/metering, which core does not
// import. Event ingestion (deduplication by transaction ID) is
// event.Log, part of [Repos].
package port
