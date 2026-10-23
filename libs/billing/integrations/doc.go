// Package integrations is the application service of the connections to
// payment, accounting, tax and CRM providers (domain §17): it creates,
// updates, deletes, reads and lists integrations, keeps their item
// mappings and collection mappings, and lists the provider items fetched.
// Each provider's settings and credentials are checked as lago's provider
// models do; the credentials are sealed with a [Sealer] before they reach
// the store (architecture §15).
//
// The operations that call a provider (the OAuth flows, the item and
// subsidiary fetches, the webhook registration at Stripe, the NetSuite
// RESTlet and Avalara company lookups) are not part of this package.
//
// Errors are those of package core: a missing integration is
// core.NotFound("integration"), an invalid input a *core.ValidationError
// with the field codes of the API.
package integrations
