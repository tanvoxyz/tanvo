// Package customsections manages invoice custom sections (domain §11.9),
// the titled texts printed on invoices: their creation, update, deletion,
// reading and listing (lago app/services/invoice_custom_sections and the
// InvoiceCustomSection GraphQL resolvers). Deleting a section removes its
// selections by billing entities, customers and subscriptions with it.
//
// Errors are those of package core: a missing section is
// core.NotFound("invoice_custom_section"), an invalid input a
// *core.ValidationError with the field codes of the API.
package customsections
