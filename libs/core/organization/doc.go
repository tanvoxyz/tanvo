// Package organization holds the tenant ([Organization]) and the legal
// issuers of its documents ([BillingEntity]), domain §4.
//
// The billing entity is authoritative for billing settings (time zone,
// currency, grace period, payment term, numbering, locale, email settings);
// the organization keeps only identity and access settings.
package organization
