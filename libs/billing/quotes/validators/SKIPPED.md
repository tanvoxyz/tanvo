# Skipped lago-api cases (quote version validators)

Cases of `spec/services/quote_versions/validators*/**` not ported to `libs/billing/quotes/validators`, and the checks of `app/services/quote_versions/validators/**` (v1.55.0) that are skipped or narrowed, each with its reason.

## Specs

| Source | Reason |
| :--- | :--- |
| `one_off_validator_spec.rb:87`, `subscription_creation_validator_spec.rb:119` (billing items with symbol keys) | Billing items reach Tanvo as JSON, whose keys are strings; there is nothing to normalize. |

Every other example is ported: `structural_test.go` (testdata `one_off_structural.json`, `subscription_creation_structural.json`), `oneoff_test.go`, `subscription_test.go` and `amendment_test.go`. `business_validator_spec.rb:895`, which runs the overlapping pair through `AppliedCoupons::CreateService`, is ported as `TestCouponOverlapMatchesApplication` over `coupon.CheckApplication`, the rule coupon application enforces.

## Checks

| Source | Reason |
| :--- | :--- |
| `subscription_creation/schema.rb:9`, `:117` (`Charge::CHARGE_MODELS`, `custom` included) | Tanvo has no `custom` charge model (`catalog.ChargeModelValues`), so the schema enum leaves it out and a quoted `custom` is `invalid_value`. |
| `subscription_creation/schema.rb:7`, `one_off/schema.rb` (`Currencies::ACCEPTED_CURRENCIES`) | The currency enums and `currency_list` are `money.Currencies()`: Tanvo lists MRU, SLE and STN, and drops the withdrawn HRK, MRO, SLL and STD (`app/models/concerns/currencies.rb:60`, `:91`, `:122`, `:125`). |
| `app/services/charges/validators/percentage_service.rb:69` (`return unless License.premium?`) | License gates are dropped: the per-transaction minimum and maximum are always validated. |
| `app/services/charges/validators/base_service.rb:41` (deprecated `grouped_by`) | Tanvo accepts only `pricing_group_keys`; the deprecated key is not read. |
| `subscription_creation/business_validator.rb:391`, `:400` (`plans_by_metric_id`, `metrics_by_plan_id`) | Read the other way round: the metrics charged by each plan a known coupon is limited to (`Charges.ListByPlan`, kept charges; a deleted plan charges none, as lago's join on the kept plan has it). A discarded metric discards its charges (`BillableMetrics::DestroyService`), so lago's join on the kept metric needs no counterpart. The pairs compared by `coupon.Overlaps` are the same. |
| `base_structural_validator.rb` (json_schemer) | The schemas are interpreted by `schema.go`, which implements the keywords they use with json_schemer 2.5's keyword order, error codes (`x-error`) and formats (`uuid`, `date-time`). |
| `Utils::Datetime.valid_format?`, `parse_iso8601` (`subscription_creation/business_validator.rb:45`, `:301`, `:310`) | `datetime.go` reproduces `Date._iso8601` for the calendar and ordinal forms with a year. The week and truncated forms have no year, so `TimeZone#iso8601` rejects them, and so does Tanvo. |
| `BigDecimal(String)` (`Validators::DecimalAmountService`, `business_validator.rb:585`) | `decimal.go` reproduces BigDecimal's string syntax (whitespace, `_` between digits, `d` exponents, `Infinity`/`NaN`) over `money.Decimal`; there is no Ruby Float anywhere. |
