// Package products is the application service of the product catalog
// (domain §21.4): product categories, products and their filters, rate
// cards with their rates, and the rate cards applied to plans with their
// phases and rate overrides. Each write runs in one transaction.
//
// A catalog object is attached once a plan prices it: a plan rate card
// applies its rate card, or a rate card of its product, filter or
// category. Attachment locks codes and refuses deletion. A rate card is
// attached to subscriptions once a contract applies it, which freezes its
// rates in effect; a plan a contract uses is locked, so its rate cards and
// phases no longer change. Contracts come with a later unit (parity
// §1.22): until then nothing is attached to subscriptions or locked.
//
// Deletion discards: the row keeps its deleted_at and drops out of every
// read but lookup by ID, which the services treat as not found.
//
// Errors are those of package core: a missing object is
// core.NotFound(resource), an invalid input a *core.ValidationError with
// the field codes of the API.
package products
