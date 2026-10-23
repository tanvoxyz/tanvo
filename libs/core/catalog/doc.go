// Package catalog holds what an organization sells (domain §7).
//
// Part one is the priced catalog: billable metrics and their filters
// (§7.1), plans and their override children (§7.2), charges (§7.3) and
// charge filters (§7.4), add-ons and fixed charges (§7.5), and pricing
// units with the pricing unit applied to a charge (§7.6). Charge
// properties are kept as a JSON object ([Properties]); libs/pricing
// validates and evaluates them per model. The pure functions here cover
// the cross-entity rules: the charge validation matrix ([ValidateCharge]),
// charge filter values, codes, matching and precedence, default codes, and
// the fields locked once a metric is charged or a plan subscribed.
//
// Part two covers taxes (§7.7), plan commitments (§7.8), usage thresholds
// (§7.9), and features, privileges and entitlements (§7.10).
//
// Every entity is scoped to an organization; the repositories are the
// interfaces implemented by libs/store and memstore.
package catalog
