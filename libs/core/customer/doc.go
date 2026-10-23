// Package customer holds the billed party, [Customer] (domain §6): its
// identity, addresses, billing settings that override the billing entity's,
// numbering counters and metadata.
//
// Settings resolve customer → billing entity → UTC everywhere ([Defaults]
// carries the billing entity's values, so this package never imports
// package organization). Rules that depend on the customer's other
// aggregates, such as editability and the currency lock, are pure
// functions of [Documents].
package customer
