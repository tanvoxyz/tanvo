// Package flutterwave is the Flutterwave adapter of the payment provider
// port ([payment.Gateway], architecture §12): connection settings and the
// webhook secret, hosted checkout pages for invoices and payment requests,
// and verification and decoding of charge webhooks.
//
// # Client
//
// Calls are plain JSON over net/http against Flutterwave's v3 API, with
// the connection's secret key as a bearer token, through one shared HTTP
// client whose transport adds a per-connection rate limit and
// OpenTelemetry instrumentation through the injected providers.
// Flutterwave publishes no maintained Go SDK. Calls are not retried in
// process: a failure worth retrying is returned as a [payment.ErrRetryable]
// provider error and retried by the caller's job.
//
// # Settings and secrets
//
// A connection's settings object holds "success_redirect_url" (at most
// 1024 characters, a valid URL; https://www.flutterwave.com/ng when
// absent); its secrets object holds "secret_key", required, and
// "webhook_secret", the secret hash Flutterwave sends in the verif-hash
// header of every webhook. RegisterWebhook generates the webhook secret
// when the connection has none; the merchant copies it to the Flutterwave
// dashboard. Flutterwave serves test and live keys from one API; a
// FLWSECK_TEST- key is a test connection.
//
// # Payments
//
// Flutterwave is used through hosted checkout pages only: customers, saved
// methods, merchant-initiated charges, refunds and cancellation are
// [payment.ErrUnsupported]. A page carries the Tanvo customer and payable
// in its meta; a successful charge.completed webhook is verified against
// Flutterwave's transaction API and becomes a [payment.PaymentEvent] for a
// one-time payment, which the handler records.
package flutterwave
