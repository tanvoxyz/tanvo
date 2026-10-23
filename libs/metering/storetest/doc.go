// Package storetest holds the conformance cases of [metering.Store]
// implementations, ported from the lago specs of the billable metric
// aggregations and of the event stores.
//
// [Run] runs every case against a store: the store-level cases call one
// [metering.Store] method, the aggregation cases run [metering.Aggregate],
// [metering.PerEvent] or [metering.Breakdown] over it. Each case records
// the spec it comes from in its Source.
package storetest
