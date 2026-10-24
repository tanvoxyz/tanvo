// Package avalara is the Avalara AvaTax adapter of the tax provider port
// ([integration.TaxGateway], architecture §12). It calls the AvaTax REST
// API v2 directly with HTTP basic authentication (account ID and license
// key), on rest.avatax.com or, for test deployments, sandbox-rest.avatax.com
// (lago chooses the sandbox outside production,
// app/services/integrations/aggregator/base_service.rb:62-63).
//
// # Operations
//
//   - Quotes create an uncommitted SalesOrder, which AvaTax does not record.
//   - Reports create or adjust the document coded by the transaction ID: a
//     SalesInvoice, or a ReturnInvoice with negative amounts for a voided
//     invoice or a credit note, committed so it counts in the filings.
//   - Voids void the document coded by the invoice ID in the connection's
//     company ("DocVoided"); a locked document is [integration.KindLocked].
//   - The connection's company is found by its code; its ID addresses the
//     customer records the adapter creates and updates, coded by the
//     Tanvo customer ID.
//
// # Wire format
//
// The request carries what lago v1.55.0 sends through its broker
// (app/services/integrations/aggregator/taxes/invoices/payloads/avalara.rb):
// the customer code (the integration customer's external ID), the
// customer's effective shipping address as destination and the billing
// entity's address as origin, the tax identification number, and one line
// per fee with its ID as line number, the mapped item code, the units and
// the amount in major units. Answer lines are matched by line number; each
// tax detail becomes a breakdown line with its rate and its tax in minor
// units.
//
// # Errors
//
// An AvaTax error envelope is a rejection coded by its error code
// ("CannotModifyLockedTransaction") with the message "Service failure"
// (lago taxes/base_service.rb:133-143). 429 is a request limit, 401 and
// 403 an authentication failure, 5xx and network failures unavailability.
//
// # Settings and secrets
//
// The settings object holds "account_id", "company_code" and, once found,
// "company_id"; the secrets object holds "license_key". Requests are
// throttled at 10 per second per account (lago throttles every Avalara
// call on one global key, config/initializers/throttling.rb:49-54; AvaTax's
// limits are per account).
package avalara
