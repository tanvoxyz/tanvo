# Skipped lago-api cases (lifecycle)

Cases of `spec/services/subscriptions/**` not ported to `libs/billing/lifecycle`, with the reason.

| Source | Reason |
| :--- | :--- |
| `spec/services/subscriptions/dates/**`, `dates_service_spec.rb`, `terminated_dates_service_spec.rb` | Covered by libs/period. |
| `*_spec.rb` Hubspot contexts (`activate_service_spec.rb:58`, `:328`, `terminate_service_spec.rb:25`, `free_trial_billing_service_spec.rb:65`, …) | CRM sync is libs/integrations, driven by the subscription webhooks. |
| Activity log expectations (`produces a … activity log`) | Activity logs are the domain events emitted here (webhook.Event). |
| `activate_service_spec.rb:48`, `:182`, `:315`, `:865`; `activate_all_pending_service_spec.rb:36`, `:76`; `create_service_spec.rb:87` | Fixed charge events at activation: Tanvo applies the fixed charge's own units when a subscription has no event (subscription.UnitsAt), so no event is recorded; unit overrides belong to the subscription API. |
| `activate_service_spec.rb:322`, `:405`, `:501` (`ActivationRules::BillCurrentPeriodJob`) | Catch-up billing after a gated period is invoicing's periodic run. |
| `activate_service_spec.rb:392`, `:488`, `:665`, `:681`, `:882`; `terminate_service_spec.rb:44`, `:572` (`BillNonInvoiceableFeesJob`) | Non-invoiceable in-advance fees are closed out by invoicing when it bills the terminating subscription; the lifecycle issues one bill request. |
| `activate_service_spec.rb:688`, `:694`, `:703` (split per purchase order) | Grouping by purchase order number is invoicing's (domain §9.6); the lifecycle sends one request. |
| `terminate_service_spec.rb:55` (concurrent termination), `:193`, `:213`, `:251` (status races before the lock) | Serialized by the subscription lock taken before the status is read; needs a real PostgreSQL store (libs/store integration tests). |
| `terminate_service_spec.rb:270` (not found) | Not-found is the API layer's. The invalid option values (`:489`, `:585`) are ported: Terminate refuses them with `core.InvalidEnum`. |
| `create_service_spec.rb:54`–`:700`, `:816`–`:1100` (payment method, plan overrides, usage thresholds, custom sections, connections, billing_time/ending_at/consolidate validation, API context, license checks) | Subscription input validation and attached resources are the subscription API's; Create receives a built subscription.Subscription. |
| `create_service_spec.rb:1767`–`:2030` (activation rule params) | Rules are stored by the caller before Create; their evaluation at activation is ported (activate_test.go). |
| `create_service_spec.rb:2036`–`:2220` (billing entity binding) | Billing entity resolution is the API's; ChangePlan copies the override (nextSubscription). |
| `plan_upgrade_service_spec.rb`, `plan_downgrade_service_spec.rb` field-copy details (invoice custom sections, entitlements, usage thresholds of the new subscription) | Attached resources of the new subscription are copied by the subscription API. |
| `update_service_spec.rb` | Editing fields does not bill; only plan overrides, which are a plan change (ChangePlan) handled by the API. |
| `free_trial_billing_service_spec.rb` `already_billed_on_day_one?` | Legacy guard for organizations upgraded during a trial; not applicable to Tanvo. |
| `activation_rules/**` (apply, validate, payment) | Rule storage and payment resolution belong to the subscription API and payments; the lifecycle evaluates rules and activates. `cancel_service_spec.rb` and `expire_service_spec.rb` are ported (cancel_incomplete_test.go), save their PSP cancel and wallet recredit cases (libs/billing/SKIPPED.md, clock job specs). |
| Other `spec/services/subscriptions/*` (termination alerts, usage thresholds, chargeable fees, flag refresh, …) | Not lifecycle billing (alerts, usage, progressive billing packages). |
