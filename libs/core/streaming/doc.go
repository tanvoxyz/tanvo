// Package streaming models streaming destinations (parity §1.40): the
// operator-configured sinks, such as an Amazon Kinesis data stream, that
// receive a customer's refreshed usage each time its wallets are refreshed.
// It holds the destination entity and its validation, the event types a
// destination claims, the repositories of destinations and of delivery
// locks, and the structured delivery log shared by the producer and the
// billing service.
package streaming
