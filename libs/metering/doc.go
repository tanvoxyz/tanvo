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
// # Usage buckets
//
// Event stores may maintain 15-minute usage buckets declared by
// [BucketDefinition]s: per external subscription, code, UTC bucket and
// combination of dimension property values, the count, sum, maximum and
// latest value of the events, their precise amounts and their last event
// and ingestion times. Buckets are keyed by property values, not by
// charge: charge filters and pricing group values are matched on the
// dimensions at read time, as on event properties. A filter key whose
// metric filter declares values keeps those and folds every other value
// into [BucketOtherValue], which bounds the rows and keeps charge filters
// exact, as they only take declared values. [UsageBucketStore]
// answers count, sum, max and latest queries from the whole buckets of a
// window and the partial buckets at its edges from the events, so that its
// results are exactly those of the events; [HourlyUsage] breaks the usage
// of a count or sum charge down by hour of the customer's time zone, over
// the series [HourlySeriesFor] builds from the charge filters.
// [MemoryStore.Buckets] is the reference [BucketReader].
//
// Every quantity is a [money.Decimal]; times are UTC instants and the
// customer's time zone is passed explicitly wherever days are counted.
package metering
