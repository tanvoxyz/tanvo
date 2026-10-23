// Package metering implements the aggregation semantics of billable
// metrics: how the events of a billing period become billable units.
//
// The aggregations (count, sum, max, unique count with add/remove
// operations, time-weighted sum, latest, their prorated and recurring
// variants, per-day breakdowns, pricing-group variants and the per-event
// increments of pay-in-advance charges) are written against [Store], the
// narrow set of queries they need from an event store. [MemoryStore] is the
// reference implementation over a slice of events; libs/eventstore
// implements the same interface on PostgreSQL and ClickHouse and checks
// itself against the shared cases in package storetest.
//
// Charge filters decide which events a charge (or one of its filters)
// bills: [MatchChargeFilter] picks the filter an event is billed on and
// [ChargeFilterScope] turns a filter into the [Filter] of a [Query].
//
// Every quantity is a [money.Decimal]; times are UTC instants and the
// customer's time zone is passed explicitly wherever days are counted.
package metering
