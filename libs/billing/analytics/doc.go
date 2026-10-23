// Package analytics serves the revenue analytics of an organization (parity
// §1.44): gross revenue, MRR, invoice collection, invoiced usage and
// overdue balance, by month. It resolves the billing entity and customer
// filters and computes the reports with the store's
// [analytics.Repository].
//
// [analytics.Repository]: https://pkg.go.dev/github.com/tanvoxyz/tanvo/libs/core/analytics#Repository
package analytics
