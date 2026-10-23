// Package quotes runs the quote flow of domain §21: a quote and its
// versions, the order form generated when a version is approved, the order
// created when the order form is signed, and the execution of that order
// into subscriptions, applied coupons, wallets or a one-off invoice (lago
// Quotes::*, QuoteVersions::*, OrderForms::* and Orders::* services,
// v1.55.0).
//
// The billing items of a version are validated by package validators; the
// changes of a quote, its versions, order forms and orders are serialized
// by the lock of the quote. SKIPPED.md lists what is not ported.
package quotes
