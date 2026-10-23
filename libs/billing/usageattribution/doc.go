// Package usageattribution is the application service of usage
// attribution types (parity §1.42): it creates, updates, reads, lists and
// deletes the kinds of labels usage is attributed to, the levels of a
// customer's account tree or independent labels.
//
// Errors are those of package core: a missing type is
// core.NotFound("usage_attribution_type"), a missing parent
// core.NotFound("parent_usage_attribution_type"), an invalid input a
// *core.ValidationError with the field codes of the API.
package usageattribution
