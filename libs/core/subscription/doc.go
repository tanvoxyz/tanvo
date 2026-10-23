// Package subscription models subscriptions (domain §9): the subscription
// aggregate with its lifecycle, editable fields and invoice grouping key,
// its activation rules (§9.3), fixed charge events (§7.5), applied usage
// thresholds (§7.9) and lifetime usage (§10.3), and the pure rule matching
// an event to its subscription (§10.2).
//
// Billing periods are computed by libs/period from
// [Subscription.PeriodSubscription]. Other aggregates (plans, customers,
// invoices) are referenced by typed ID; rules needing their state take the
// facts as parameters.
package subscription
