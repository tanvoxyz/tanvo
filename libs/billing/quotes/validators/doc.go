// Package validators checks the billing items of a quote version before it
// is updated or approved (lago QuoteVersions::Validators).
//
// [Validate] runs two passes, as lago does. The structural pass checks the
// shape of billing_items against the JSON schema of the order type
// (schema/*.json, generated from the lago v1.55.0 schema definitions) with
// a small in-package interpreter of the keywords those schemas use, and
// reports each failure as billing_items.<pointer> with the schema's
// x-error code or "is_invalid". Only when the shape is valid does the
// business pass run: it resolves the quoted plans, charges, add-ons,
// coupons, metrics, payment methods, billing entity and target subscription
// through the repositories and checks them against the deal.
//
// Errors are grouped by field in the order each field first failed, the
// codes of a field in the order they were found, which is the order of
// lago's error hash. SKIPPED.md lists the lago checks and spec examples
// that are not ported, with the reason.
package validators
