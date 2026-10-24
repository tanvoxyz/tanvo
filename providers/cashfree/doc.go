// Package cashfree is the Cashfree adapter of the payment provider port
// ([payment.Gateway], architecture §12): connection settings, payment
// links for invoices and payment requests, and verification and decoding
// of payment link webhooks.
//
// # Client
//
// Calls are plain JSON over net/http against Cashfree's Payment Gateway
// API version 2023-08-01, with the connection's client ID and secret in
// the x-client-id and x-client-secret headers, through one shared HTTP
// client whose transport adds a per-connection rate limit and
// OpenTelemetry instrumentation through the injected providers. Cashfree's
// Go SDK pins a newer API version through a mutable package variable, so
// it is not used. Calls are not retried in process: a failure worth
// retrying is returned as a [payment.ErrRetryable] provider error and
// retried by the caller's job.
//
// # Settings and secrets
//
// A connection's settings object holds "success_redirect_url" (at most
// 1024 characters, a valid URL; https://cashfree.com/ when absent) and
// "sandbox", which selects Cashfree's sandbox environment; its secrets
// object holds "client_id" and "client_secret", both required. The client
// secret also signs webhooks.
//
// # Payments
//
// Cashfree is used through payment links only: customers, saved methods,
// merchant-initiated charges, refunds and cancellation are
// [payment.ErrUnsupported]. A link carries the Tanvo customer and payable
// in its link_notes; its PAID webhook is a [payment.PaymentEvent] for a
// one-time payment, which the handler records.
package cashfree
