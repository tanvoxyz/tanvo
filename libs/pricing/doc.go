// Package pricing is the charge-model engine: it turns the result of a
// metric aggregation into an amount, a unit amount and the per-model amount
// details of a fee. It is pure: no I/O, no clock, no database.
//
// A [PricingStructure] couples typed [Properties] (one struct per
// [ChargeModel]) with the charge's proration, currency and catalog flags.
// [Apply] prices an [AggregationResult] in arrears (with optional projection
// to the full period), [ApplyInAdvance] prices a single pay-in-advance event
// as the difference between the totals with and without it, and [Amounts]
// converts a model result into the minor-unit amounts of a fee, through an
// optional [AppliedPricingUnit], with deductions and minimum-commitment
// true-ups.
//
// [Validate] checks raw (JSON-decoded) properties the way an API does before
// they are stored, [FilterProperties] keeps only the keys a model uses and
// [DefaultProperties] returns the properties of a charge created without
// any.
//
// Rounding is explicit and happens only where a fee is built: amounts in
// minor units are rounded half up to the currency's exponent; every
// intermediate value keeps full precision, and quotients keep
// [money.DivisionPrecision] significant digits.
package pricing
