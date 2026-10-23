// Package integration holds the connections of an organization to external
// services (domain §17): payment, accounting, tax, CRM and identity
// providers ([Integration]), their per-customer links
// ([IntegrationCustomer]), the item and collection mappings used to sync
// documents ([Mapping], [CollectionMapping]), the cached external catalog
// ([Item]) and the record of synced objects ([Resource]).
//
// Several connections per provider are allowed, each with a code; at most
// one per category is the default (domain §17.1).
package integration
