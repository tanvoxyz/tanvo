// Package analytics holds the revenue analytics of an organization
// (parity §1.44): monthly gross revenue, MRR, invoice collection,
// invoiced usage and overdue balance, computed by the store from the
// invoices and fees. The figures are amounts in minor units, by month
// (UTC) and currency, from the organization's creation month to the
// current month.
package analytics
