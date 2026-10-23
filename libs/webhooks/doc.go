// Package webhooks delivers outgoing webhooks and receives provider
// webhooks (architecture §11, domain §15.6 and §18).
//
// Outgoing: an [Emitter] records a domain event and enqueues a fan-out job;
// [Dispatcher.FanOut] creates one delivery per subscribed endpoint and
// enqueues its delivery job; [Dispatcher.Deliver] makes one attempt,
// signed per Standard Webhooks ([Sign]), through an HTTP client that
// refuses private network targets ([NewClient], [Blocked]), and schedules
// the next attempt with the backoff of libs/core/webhook. Delivery is at
// least once: receivers deduplicate on the webhook-id header.
//
// Incoming: [Inbound.Receive] verifies a provider webhook with its
// [Source], stores it and enqueues its processing; [Inbound.Process] hands
// it to the source's handler and records the outcome.
package webhooks
