// Package taxes is the application service of taxes (domain §7.7): it
// creates, updates, deletes, reads and lists the organization's taxes.
// Each write runs in one transaction with its side effects: applying a tax
// to the organization attaches it to the default billing entity, a change
// flags for refresh the draft invoices of the customers the tax applies to
// (before and after the change), and a deletion detaches the tax from
// every owner and flags the same drafts.
//
// Errors are those of package core: a missing tax is core.NotFound("tax"),
// an invalid input a *core.ValidationError with the field codes of the
// API.
package taxes
