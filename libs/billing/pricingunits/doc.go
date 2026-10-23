// Package pricingunits is the application service of pricing units
// (domain §7.6): it creates, updates, reads and lists the custom units in
// which charges can be priced. Pricing units are never deleted, and their
// code never changes.
//
// Errors are those of package core: a missing pricing unit is
// core.NotFound("pricing_unit"), an invalid input a *core.ValidationError
// with the field codes of the API.
package pricingunits
