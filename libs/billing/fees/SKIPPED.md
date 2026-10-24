# Unported lago specs: libs/billing/fees

These lago-api specs, or the cases in them, have no Go test in this package. Paths are relative to `spec/services/`. "Pricing" means the case only exercises charge-model math, which libs/pricing already covers. "Caller" means the behaviour belongs to the code that stores the fees (the invoicing and usage services), not to this package.

## Whole files

| Spec | Reason |
| :--- | :--- |
| `fees/amounts_service_spec.rb` | Pricing (`pricing.ComputeAmounts`) |
| `fees/apply_taxes_service_spec.rb`, `fees/apply_provider_taxes_service_spec.rb`, `fees/apply_provider_taxes_to_standalone_fees_service_spec.rb`, `commitments/apply_taxes_service_spec.rb` | Taxes are applied by the caller through kernel's tax resolver |
| `fees/projection_service_spec.rb` | Projected usage is outside this package's scope |
| `fees/estimate_pay_in_advance_service_spec.rb`, `fees/estimate_instant/**` | Estimates go through the API layer; `InAdvanceEvent.Estimate` is tested |
| `fees/charge_service_billing_segment_spec.rb`, `fees/charge_service/sources/billing_segment_spec.rb` | Billing segments and contracts are not modelled |
| `fees/charge_service/options_spec.rb`, `fees/charge_service/metered_item_spec.rb`, `fees/charge_service/sources/charge_spec.rb` | Lago's internal value objects; `Options` and `Scope` replace them |
| `fees/advance_charges_service_spec.rb`, `fees/advance_charges_to_datetime_filter_resolver_spec.rb` | Regrouping paid in-advance fees is the invoicing package's job |
| `fees/paid_credit_service_spec.rb` | Wallet credit fees belong to libs/billing/wallets |
| `fees/update_service_spec.rb`, `fees/destroy_service_spec.rb` | Fee CRUD belongs to the caller and the API |
| `fees/init_from_adjusted_charge_fee_service_spec.rb`, `fees/init_from_adjusted_fixed_charge_fee_service_spec.rb` | Covered through the adjusted cases of `ChargeFees` and `FixedChargeFee` |
| `fees/create_true_up_service_spec.rb` | Covered through the true-up cases of `ChargeFees`; the proration math is pricing |
| `fees/commitments/minimum/calculate_preview_fee_service_spec.rb` | Preview commitments are computed by the invoicing preview |
| `commitments/override_service_spec.rb` | Plan overrides belong to the catalog services |
| `commitments/fetch_invoices_service_spec.rb`, `commitments/dates_service_spec.rb`, `commitments/calculate_prorated_coefficient_service_spec.rb`, `commitments/calculate_amount_service_spec.rb` | Internal steps of `CommitmentFee`; tanvo unifies them (see the deviations in the report) and tests the result |
| `commitments/minimum/calculate_true_up_fee_service_spec.rb` | 4,508 lines of the same reduction over invoice and plan shapes; one representative case is ported, and the period selection differs on purpose |

## Cases in ported files

### fees/subscription_service_spec.rb
- :79, :86 (persisting, preview not persisting): the caller stores fees.
- :816, :1172, :1341 (customer time zone variants): kernel's date tests cover time zones; boundaries are passed in.
- :1002, :1052 (billed before, weekly and yearly in advance with a trial): same rule as :1027, which is ported.

### fees/charge_service_spec.rb
- :73–:143 (validations, billing context, contracts): lago internals and contracts.
- :227, :339 (persisting, preview): the caller stores fees.
- :231–:336, :861–:944, :2809–:2840, :2877–:2970, :3876–:3937, :4428–:4573, :4620–:4749 (presentation breakdowns): not modelled.
- :501, :513, :834, :846 (recurring weighted sum and custom aggregation per group): custom aggregation is not ported (domain §24); the per-group weighted sum is metering's.
- :546–:860 (pricing group keys combined with filters): grouping and filters are each tested; their combination is metering's `GroupBy`.
- :1169, :2378 (no pricing unit usage created): asserted inside the ported cases.
- :1298–:1447 (adjusted units with filters per aggregation type): the adjustment path does not depend on the aggregation; covered by :1262.
- :2842–:2873, :3061–:3092 (aggregation errors): lago stubs; tanvo returns the metering error wrapped.
- :3004 (adjusted fees loaded once): a lago query count.
- :3162–:3330 amount_details payload strings: amounts are ported; the detail shapes are pricing's.
- :3563–:4003 (charge cache middleware): the cache is the caller's `Options.Cache`.
- :4019 (apply taxes): caller.
- :4043–:4216, :4240–:4320 except :4230 and :4299 (`filtered_aggregations` skip optimisation): not modelled; every bucket is aggregated.
- :4321–:4427 (filter_by_group), :4574–:4619 (skip_grouping): current-usage API filters, handled by the usage package.
- :4750–:5025 (precomputed aggregations): ClickHouse serving is the event store's.

### fees/create_pay_in_advance_service_spec.rb
- :95–:255 (billing segments and products): not modelled.
- :290 (fee already exists), :303, :653 (webhooks), :1004–:1086 (taxes): caller.
- :315, :330 (stubbed failures): lago mocks.
- :469 (charge filter grouped_by): replaced by pricing group keys, ported at :422.
- :663 (estimate false on an unpersisted event): Go types rule it out.
- :753–:1003 (presentation breakdowns): not modelled.

### fees/fixed_charge_service_spec.rb
- :69–:105 (preview of an unpersisted subscription): previews use stored data in tanvo.
- :147, :167 (persisting, preview): caller.
- :202, :704, :720 (pay-in-advance and arrears boundaries): kernel's date service picks the boundaries.
- :217–:289 (event created after the period): ported at :228 and :250 in `TestFixedChargeFeeChangeOrder`; changes are ordered by creation as in lago.
- :487–:670 (upgrade with a paid fee of the previous subscription): `fixedChargeDeduction` is implemented; the lago setup depends on lago's fee lookups and is not ported yet.
- :690 (taxes), :790, :906 (adjusted fee linked to the new fee): caller.

### fees/build_pay_in_advance_fixed_charge_service_spec.rb
- :429, :536 (prorated increases): same rule as the ported non-prorated cases plus :337.
- :573, :609 (weekly, yearly prorated): period lengths come from libs/period.
- :674 (parent fixed charge): implemented; not ported because billingtest has no fixed-charge override fixture.
- :682–:907 (`fixed_charge_usage_delta_migration` flag): the migration flag is not modelled.

### fees/one_off_service_spec.rb
- :212–:271 (malformed boundary strings): parsing happens in the API; Go takes `time.Time`.

### fees/commitments/minimum/create_service_spec.rb
- :85 (commitment taxes are zero): taxes are the caller's; the fee is created untaxed and that is asserted in :59.
