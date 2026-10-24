// Package hubspot is the HubSpot [integration.CRMGateway]: it connects a
// portal by OAuth, deploys Tanvo's custom invoice and subscription objects
// and the Tanvo properties of companies and contacts, and creates or
// updates the customer's company or contact, the invoice and
// subscription records and their associations, calling the CRM API
// directly.
//
// # Connections
//
// A connection holds the OAuth grant in its secrets (access_token,
// refresh_token, token_expires_at) and the deployed schema in its
// settings: the object type IDs of the custom objects (invoices_object_type_id,
// subscriptions_object_type_id) and the version of each deployed object
// and property set. [Gateway.Deploy] returns what to store; an access
// token refreshed during a call is stored through the
// [integration.ConnectionStore] given with [WithConnectionStore].
//
// # Throttling
//
// Calls are limited per connection to HubSpot's 110 requests in 10
// seconds of an OAuth app installation.
package hubspot
