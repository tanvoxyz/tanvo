// Package subscriptions is the application service of the subscription
// and customer usage API (domain §9, §10.3): it creates subscriptions
// (creating the customer an external ID names when it does not exist),
// reads, lists and updates them, changes their plan, terminates and
// cancels them, reads and sets their lifetime usage, and computes their
// current and past usage per charge. The lifecycle itself (activation,
// plan changes, termination and their invoices) is package lifecycle's,
// the usage computation package usage's; this package resolves the API's
// references, validates the requests and joins both in one transaction.
//
// Errors are those of package core: a missing object is
// core.NotFound(<resource>), an invalid input a *core.ValidationError
// with the field codes of the API, a refused operation a *core.RuleError.
package subscriptions
