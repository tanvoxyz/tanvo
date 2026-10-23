# Unported lago-api cases

The lago-api specs of the product catalog services
(`spec/services/{product_categories,products,product_filters,rate_cards,rate_card_rates,plan_rate_cards,rate_phases,rate_overrides,rate_properties}/*_spec.rb`)
and `spec/services/billable_metrics/destroy_service_spec.rb:78` are ported into
`*_test.go`. Each ported case has a `// source:` comment. This file lists the
cases that were not ported, grouped by reason.

## Activity logs

- Every "produces an activity log" case (`products/create_service_spec.rb:35`, `product_categories/*_service_spec.rb`, `product_filters/*_service_spec.rb`, `rate_cards/*_service_spec.rb`, `rate_card_rates/*_service_spec.rb`, `rate_cards/create_service_spec.rb:126`). The catalog services emit no activity log: Tanvo records activity for billing entities only.

## Typed inputs

- `rate_cards/create_service_spec.rb:55,72,81` and `rate_cards/update_service_spec.rb:85,160`: null or non-boolean `proration` / `regroup_paid_fees`. The proto fields are typed booleans and enums; an unset `regroup_paid_fees` is the standalone value.
- `rate_phases/create_service_spec.rb:92,121` and `rate_phases/update_service_spec.rb:52,217,225,240,249`: positions and cycle counts that are strings, blank or not integers. The proto fields are integers; an unset cycle count is the indefinite phase.
- `rate_overrides/create_service_spec.rb` structural fields (`billing_timing`, `currency`, `proration`, `display_on_invoice`, `regroup_paid_fees`, `applied_pricing_unit_code`) rejected as `not_overridable`: the `RateOverride` message has no such field.
- `rate_properties/normalize_ranges_service_spec.rb:63`: properties that are not an object. Tanvo properties are always a JSON object.

## Exact decimals

- `rate_properties/normalize_ranges_service_spec.rb:95` "upper bounds only differ beyond float precision", "does not fit a float", "more significant digits than a float compares exactly": Tanvo keeps every bound as an exact decimal and accepts them (see `ranges_test.go`).

## Unreachable states

- `rate_phases/create_service_spec.rb:62`: a plan rate card without phases. Every plan rate card is created with at least one phase and its last phase cannot be deleted.
- `rate_phases/destroy_service_spec.rb:73`: a single definite phase. The last phase is always indefinite, so deleting the only phase is `indefinite_phase_not_deletable` (ported).
- `rate_phases/update_service_spec.rb:268`, `rate_phases/destroy_service_spec.rb:95`: a phase deleted concurrently. The plan rate card's lock serializes the sequence operations, and the phases are read under it.
- `rate_phases/create_service_spec.rb:140`: no parent. Phases are created through a plan rate card only (contracts are not built).

## Locks

- `rate_phases/*_service_spec.rb` "locks the parent entry": the lock is taken (`plan_rate_card` scope) but the memstore cannot observe it.

## Contracts

Contracts come with the Contracts and Billing Segments unit (parity §1.22). Until then no plan is attached to a contract, so the lock checks always pass and these cases have nothing to set up:

- `plan_rate_cards/create_service_spec.rb:144`, `plan_rate_cards/update_service_spec.rb:22`, `plan_rate_cards/destroy_service_spec.rb:31`, `catalog_plans/destroy_service_spec.rb:49`: `plan plan_locked` on a plan with contracts.
- `rate_phases/create_service_spec.rb:131`, `rate_phases/update_service_spec.rb:259`, `rate_phases/destroy_service_spec.rb:84`: `rate_phase plan_locked` on a plan with contracts.
- `product_filters/update_service_spec.rb:111,116,136`: `values attached_to_subscriptions` for a filter a contract rate card prices.
- `rate_card_rates/update_service_spec.rb:171,182`: pending and active rates of a card a contract rate card applies.
- `spec/requests/api/v2/plan_rate_cards_controller_spec.rb:195,230`: the same lock over REST.

## Plan rate card list filters

- The GraphQL `RateCardListFiltering` of a plan's applied rate cards (product, filter, category, product type, overrides) and its product-category ordering are not offered: `ListPlanRateCards` returns the plan's cards in creation order, as the REST index does.
