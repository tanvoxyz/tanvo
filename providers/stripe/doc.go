// Package stripe is the Stripe adapter of the payment provider port
// ([payment.Gateway], architecture §12): customers, payment methods and
// setup pages, PaymentIntents for invoices, payment requests and wallet
// credit invoices, hosted Checkout pages, refunds of credit notes, bank
// transfer funding through the customer balance, webhook endpoint
// registration, signature verification and event decoding.
//
// # Client
//
// Calls go through the official github.com/stripe/stripe-go (MIT). Stripe's
// form encoding of nested parameters, its error envelope, list pagination
// and its retry rules (connection errors and 429 lock timeouts, keeping
// the idempotency key across attempts) are subtle and
// versioned with the API; the official client tracks them, so a
// hand-written client would only re-derive them. The client is configured
// per call with the connection's secret key over one shared backend, whose
// HTTP transport adds a per-connection rate limit and OpenTelemetry
// instrumentation through the injected providers. Webhook payloads are
// decoded by this package from the few fields it needs, so events survive
// API version changes of unrelated fields.
//
// # Settings and secrets
//
// A connection's settings object holds "success_redirect_url",
// "webhook_endpoint_id", "supports_3ds" and
// "require_terms_of_service_consent"; its secrets object holds
// "secret_key" and "webhook_secret".
//
// # Differences from lago
//
// Each deliberate difference is noted where it is implemented, with its
// lago reference, and listed in docs/parity.md §1.33–1.35.
package stripe
