// Package billing is the billing engine of Tanvo (architecture §8): it
// composes the services of its sub-packages over the ports of libs/core.
// apps/server builds one [Engine] with the store, the event store and the
// providers, and libs/jobs calls it from its handlers.
//
// Every multi-step change runs in one transaction with the customer and
// subscription locks of domain §6; every instant comes from the Clock
// port; domain events go through the WebhookEmitter port and follow-up
// work through jobs enqueued in the same transaction.
package billing
