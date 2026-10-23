// Package metrics is the application service of billable metrics
// (domain §7.1): it creates, updates, deletes, reads and lists metrics,
// and evaluates expressions against sample events. Each write runs in one
// transaction with its side effects: the filter changes trim the charge
// filters that use removed values and flag the draft invoices billing the
// metric for refresh, a deletion removes the metric's charges, and every
// write emits its billable_metric.* webhook event.
//
// Errors are those of package core: a missing metric is
// core.NotFound("billable_metric"), an invalid input a
// *core.ValidationError with the field codes of the API.
package metrics
