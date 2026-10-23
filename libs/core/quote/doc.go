// Package quote holds the sales flow of domain §21.1–21.3: a [Quote] has
// numbered [Version]s; approving a version generates an [OrderForm];
// signing the order form creates an [Order], whose execution applies the
// quote content (a subscription creation or amendment, or a one-off
// invoice) unless it is order-only.
//
// Numbers are "<prefix>-<YYYY>-<NNNN>": the creation year and a sequence
// per organization ([FormatNumber]). Expiring or voiding an order form cascades
// to its version.
package quote
