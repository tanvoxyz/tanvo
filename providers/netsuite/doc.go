// Package netsuite is the Oracle NetSuite adapter of the accounting port
// ([integration.AccountingGateway], architecture §12). It calls the
// connection's RESTlet (script_endpoint_url) and the SuiteTalk REST
// SuiteQL endpoint directly, signed with OAuth 1.0a token-based
// authentication (HMAC-SHA256, realm = the account ID).
//
// # Operations
//
//   - Contacts, invoices, credit memos and customer payments are created by
//     POSTing to the RESTlet the record bodies lago v1.55.0 sends through
//     its broker (app/services/integrations/aggregator/*/payloads/netsuite.rb);
//     contacts are updated with PUT. The RESTlet answers the record ID.
//   - Items and subsidiaries are read with SuiteQL (the broker's syncs);
//     an invoice already created by an earlier attempt is found by its
//     tranid (lago invoices/reconcile_service.rb).
//   - Ledger accounts are not fetched ([integration.ErrUnsupported]): the
//     NetSuite mappings carry the account on each item.
//
// # Errors
//
// The code of a failed answer is read where lago reads it
// (aggregator/base_service.rb:159-176: "type", error.payload.name,
// error.payload.error.code, error.code), else "unexpected_error";
// SSS_REQUEST_LIMIT_EXCEEDED is a request limit, INVALID_LOGIN_ATTEMPT an
// authentication failure.
//
// # Settings and secrets
//
// The settings hold "account_id", "client_id", "token_id" and
// "script_endpoint_url"; the secrets "client_secret" and "token_secret".
// Requests are throttled at 10 per second per client ID (lago
// config/initializers/throttling.rb:40-45, keyed by client ID).
package netsuite
