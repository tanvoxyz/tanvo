// Package webhook holds outgoing notifications (domain §18): the domain
// [Event] every state change emits in its transaction (domain §2.3), the
// organization's [Endpoint]s, and the [Delivery] of an event to one
// endpoint with its retry lifecycle.
//
// Signing follows Standard Webhooks; the HTTP client, SSRF policy and
// signature computation live in libs/webhooks. This package holds the
// pure rules: event type filtering, endpoint limits, attempt counting and
// backoff.
package webhook
