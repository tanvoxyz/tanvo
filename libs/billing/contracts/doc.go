// Package contracts is the application service of contracts (domain
// §21.4): subscriptions priced by the rate cards applied to them rather
// than by their plan's charges. It creates, updates, activates and
// terminates contracts, materializes a plan's rate cards onto them,
// authors their rate cards and phases while they are pending, keeps each
// rate card's billing clock, and stores the billing segments each card's
// schedule makes due. The segments are invoiced by package invoicing.
//
// Errors are those of package core: a missing object is
// core.NotFound(<resource>), an invalid input a *core.ValidationError
// with the field codes of the API.
package contracts
