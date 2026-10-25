// Package xero is the Xero adapter of the accounting port
// ([integration.AccountingGateway] and [integration.OAuthGateway],
// architecture §12). It calls the Xero Accounting API directly with the
// connection's OAuth 2.0 grant.
//
// # Operations
//
//   - Contacts, invoices (ACCREC), credit notes (ACCRECCREDIT) and payments
//     are created with the bodies lago v1.55.0 builds
//     (app/services/integrations/aggregator/*/payloads/xero.rb and their
//     base payloads), mapped to the Accounting API as its broker's Xero
//     actions map them (create-contact, update-contact, create-invoice,
//     create-credit-note, create-payment), with summarizeErrors=false: a
//     record Xero refuses is a [integration.ProviderError] coded
//     [integration.CodeValidationError] whose message joins its
//     validation errors with ". ".
//   - Items are read from GET Items, identified by their code; ledger
//     accounts from GET Accounts, the active ones only.
//
// # OAuth
//
// The deployment's Xero app ([OAuthClient]) authorizes a connection with
// the authorization code grant: [Gateway.ExchangeCode] stores the tokens
// and the tenant the grant authorized. Access tokens are refreshed when
// they expire within a minute; Xero rotates the refresh token, so the
// new grant is stored at once through the [integration.ConnectionStore].
//
// # Settings and secrets
//
// The settings hold "tenant_id" and "tenant_name"; the secrets
// "access_token", "refresh_token" and "token_expires_at" (RFC 3339).
//
// # Throttling
//
// Calls are throttled per tenant at Xero's limit of 60 per minute, as a
// bucket of 60 refilled at one per second, and at 5000 per UTC day: a
// call over the day's quota is a request limit retried the next day. A 429
// answer is a request limit with Xero's Retry-After delay.
package xero
