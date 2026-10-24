// Package adyen is the Adyen adapter of the payment provider port
// ([payment.Gateway], architecture §12): zero-amount authorization links
// that save a card for a shopper, card-on-file payments of invoices and
// payment requests, payment links, cancellations, refunds of credit notes,
// HMAC verification of notifications and their decoding.
//
// # Client
//
// Calls go through the official github.com/adyen/adyen-go-api-library
// (MIT, Checkout API v71), configured per call with the connection's API
// key and environment over one shared HTTP client, whose transport adds a
// per-connection rate limit and OpenTelemetry instrumentation through the
// injected providers. Notifications are decoded by this package from the
// fields it needs.
//
// # Customers
//
// Adyen has no customer object: a shopper is identified by a shopper
// reference, the customer's external ID, once a zero-amount authorization
// saved one of its cards. CreateCustomer and UpdateCustomer therefore
// return [payment.ErrUnsupported]; SetupURL creates the authorization link,
// and its AUTHORISATION notification yields [payment.PaymentMethodAttached]
// whose ProviderCustomerID is the shopper reference to store as the
// provider customer ID.
//
// # Settings and secrets
//
// A connection's settings object holds "merchant_account", "live_prefix"
// and "success_redirect_url"; its secrets object holds "api_key" and
// "hmac_key". A connection with a live prefix calls Adyen's live
// environment, any other its test environment.
//
// # Differences from lago
//
// Each deliberate difference is noted where it is implemented, with its
// lago reference, and listed in docs/parity.md §1.34–1.35.
package adyen
