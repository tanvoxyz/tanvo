// Package addons is the application service of add-ons (domain §7.5): it
// creates, updates, deletes, reads and lists the organization's add-ons
// and sets their taxes. Each write runs in one transaction; a deletion
// also deletes the add-on's fixed charges on every plan.
//
// Errors are those of package core: a missing add-on is
// core.NotFound("add_on"), a missing tax core.NotFound("tax"), an invalid
// input a *core.ValidationError with the field codes of the API.
package addons
