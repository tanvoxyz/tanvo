// Package anrok is the Anrok adapter of the tax provider port
// ([integration.TaxGateway], architecture §12). It calls the Anrok
// Transactions API directly (https://api.anrok.com, bearer API key):
// createEphemeral for quotes of drafts, previews and current usage,
// createOrUpdate for finalized invoices, standalone fees and credit
// notes, void and createNegation for voided invoices.
//
// # Wire format
//
// The request and the reading of the answer follow the integration
// lago v1.55.0 used through its broker: the customer is sent by ID
// (the integration customer's external ID, else the customer's), name
// and effective shipping address, with a generic VAT number when the
// customer has a tax identification number; each line carries the fee
// or charge ID, the mapped product code and the taxable amount in minor
// units. Lines of the answer are matched to the request by position; each
// jurisdiction's taxes become breakdown lines, and a jurisdiction that
// taxes nothing becomes a line of its not-taxed reason. The accounting
// and tax dates are capped 30 days after today
// (app/services/integrations/aggregator/taxes/invoices/payloads/anrok.rb:56-59).
//
// # Errors
//
// A 409 answer is a rejection coded by its type ("taxDateTooFarInFuture"),
// with the message "Service failure"; a 400 answer's text is the message of
// code "validationError" (lago taxes/base_service.rb:133-143).
// "transactionFrozenForFiling" is [integration.KindLocked] and
// "transactionIdNotFound" [integration.KindNotFound]. 429 is a request
// limit, 401 and 403 an authentication failure, 5xx and network failures
// unavailability.
//
// # Settings and secrets
//
// The secrets object holds "api_key". Requests are throttled at 9 per second
// per API key, lago's limit and key (config/initializers/throttling.rb:42-48,
// app/services/integrations/aggregator/base_service.rb:78-88). A request
// over the limit waits for its turn instead of failing the job with a
// throttling error to retry.
package anrok
