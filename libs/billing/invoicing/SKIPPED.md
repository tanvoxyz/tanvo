# Unported lago-api cases (libs/billing/invoicing)

Cases from `spec/services/invoices/**` (customer_usage and lifetime excluded) and
`invoices/preview*` that have no test here, with the reason.

## Owned elsewhere
- `paid_credit_service_spec.rb` (22), `finalize_open_credit_service_spec.rb`: wallet credit invoices, in `wallets`.
- `payments/**`, `lose_dispute_service_spec.rb`: payments module.
- `generate_pdf_service_spec.rb`, `generate_xml_service_spec.rb`: documents module.
- `update_service_spec.rb` (25), `metadata/update_service_spec.rb`, `delete_service_spec.rb` (13), `refresh_search_terms_service_spec.rb`, `sync_salesforce_id_service_spec.rb`, `update_*issuing_date*_service_spec.rb`: invoice administration in the API layer.
- `issuing_date_service_spec.rb`, `create_generating_service_spec.rb`: `kernel.NewInvoice`, tested in `kernel`.
- `compute_amounts_from_fees_spec.rb`, `aggregate_amounts_and_taxes_from_fees_spec.rb`, `apply_taxes_service_spec.rb`, `apply_provider_taxes_service_spec.rb`, most of `compute_taxes_and_totals_service_spec.rb` (17): `libs/tax` and `kernel.TaxResolver`.

## Not ported yet (behaviour implemented)
- `calculate_fees_service_spec.rb` (138): the fee rules of `fees.go` (trials, upgrades, yearly charges billed monthly, in-advance plans, fixed charges, commitments) are ported but only the main paths are tested.
- `create_invoice_subscription_service_spec.rb` (11): boundaries per reason; covered indirectly.
- `create_pay_in_advance_fixed_charges_service_spec.rb` (54): pay-in-advance fixed charges are billed through `Bill` (starting invoices); no dedicated service.
- `finalize_batch_service_spec.rb`, `retry_batch_service_spec.rb`: batch wrappers; `FinalizeDueDrafts` is tested.
- `transition_to_final_status_service_spec.rb` (9): finalize/close per billing entity and customer settings; `TestBillZeroAmount` covers the three main rows.
- `preview/*` (6 files): plan-change and termination previews, coupon previews and subscription lookups; `Preview` covers persisted, new and terminated subscriptions without input coupons.
- `refresh_draft_service_spec.rb:90` (upgrade invoices), `:175`/`:183` (credit notes re-attached to new fees).
- `regenerate_from_voided_service_spec.rb`: fee adjustments (`fees_params`), purchase order numbers, invoice subscription and applied usage threshold duplication.

## Not implemented (see the deviations of the report)
- Gated subscriptions and activation rules (`subscription_service_spec.rb:912`, `retry_service_spec.rb:118`).
- Invoice custom sections (`apply_invoice_custom_sections_service_spec.rb`, every "custom sections" context).
- Purchase order numbers, integrations (Hubspot, NetSuite, Salesforce, Segment), activity logs, revenue analytics, VIES checks (`ensure_completed_vies_check_service_spec.rb`, `finalize_pending_vies_invoice_service_spec.rb`), premium email settings.
- Billing segments and contracts (`create_pay_in_advance_charge_service_spec.rb:150`).
- Concurrency guards (stale object, lock acquisition, racing voids).
- Void with a credit note (`void_service_spec.rb:203`).
- `build_regeneration_preview_service_spec.rb`, `preview_context_service_spec.rb`.
