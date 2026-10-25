// Package streaming delivers the usage-refreshed events to the streaming
// destinations of an organization (parity §1.40, lago
// EventDestinations::CustomerUsage::RefreshedService,
// EventDestinations::CustomerFullUsage::RefreshedService and
// DeliverEventJob): one record per active subscription of a customer,
// with its current or full usage and the ongoing usage of the customer's
// wallets, after every refresh of the customer's ongoing balances.
//
// The records are written through a [port.StreamProducer]; the
// destinations, their claims and the delivery locks are in
// [port.Repos].
package streaming
