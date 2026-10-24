// Package moneyhash is the MoneyHash adapter of the payment provider port
// ([payment.Gateway], architecture §12): connection with its webhook
// signature key, customers, card tokenization through a checkout intent,
// merchant-initiated payments with a saved card token, payment links, and
// verification and decoding of webhooks.
//
// # Client
//
// MoneyHash publishes no Go SDK, so calls are plain JSON over net/http
// with the connection's API key in the x-Api-Key header, through one
// shared HTTP client whose transport adds a per-connection rate limit and
// OpenTelemetry instrumentation through the injected providers. Calls are
// not retried in process: a failure worth retrying is returned as a
// [payment.ErrRetryable] provider error and retried by the caller's job.
//
// # Settings and secrets
//
// A connection's settings object holds "flow_id" (required, at most 20
// characters) and "sandbox", which selects MoneyHash's staging
// environment; its secrets object holds "api_key" and "signature_key",
// fetched from MoneyHash by [Gateway.RegisterWebhook].
//
// # Webhooks
//
// MoneyHash has no webhook endpoints to register: every intent carries
// the URL of its notifications, [payment.Connection.WebhookURL].
// Deliveries are signed with the organization's signature key in the
// MoneyHash-Signature header.
//
// # Payment methods
//
// A MoneyHash payment method is a card token. SetupURL returns a checkout
// that tokenizes a card; its card_token.created webhook yields
// [payment.PaymentMethodAttached] for the Tanvo customer named in the
// token's custom fields, and card_token.deleted
// [payment.PaymentMethodDetached]. MoneyHash's API for listing tokens is
// not used: saved methods are known from webhooks.
//
// # Differences from lago
//
// Each deliberate difference is noted where it is implemented, with its
// lago reference, and listed in docs/parity.md.
package moneyhash
