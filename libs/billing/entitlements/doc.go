// Package entitlements is the application service of features and
// entitlements (domain §7.10). It creates, updates, deletes, reads and
// lists features with their privileges, and grants features to plans and
// to subscriptions: a subscription inherits its plan's entitlements, which
// it overrides with values of its own and narrows with removals.
//
// Each write runs in one transaction with its webhook events: feature.*
// for features, plan.updated for every plan whose entitlements change
// (including through a change of one of their features), and
// subscription.updated for the subscription overrides.
//
// Errors are those of package core: a missing object is core.NotFound
// with the resource name of the API ("feature", "privilege", "plan",
// "subscription", "entitlement"), an invalid input a *core.ValidationError
// with the field codes of the API.
package entitlements
