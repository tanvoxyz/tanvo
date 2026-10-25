# Skipped lago cases

Cases of the lago specs listed in the porting brief that are not ported to `usage`, with the reason. Every ported case carries a `// source:` comment in the tests.

## spec/services/invoices/customer_usage_service_spec.rb

| Lines | Case | Reason |
| :--- | :--- | :--- |
| 1027 | Charge cache gate without the granular lifetime usage integration | Tanvo has no premium gates (see the 620–638 row). |
| 85 | Does not query adjusted fees | Covered by `fees.Options.SkipAdjustedFees`, set unconditionally. |
| 187–416 | Tax provider integration (single line item per grouped charge, non-taxable fees excluded, rate dilution, provider error) | Owned by `kernel.TaxResolver.Compute` and the tax provider; usage only passes the in-memory invoice with `Draft: true`. |
| 620–638 | `full_usage` without the granular lifetime usage integration | Tanvo has no premium gates (domain §4.1): `Options.FullUsage` is allowed whenever the charge filter and the proration rules allow it (ported in `full_test.go`). |
| 1126, 1130 | Bucket read failure falls back to the events, or raises under the forced gate | The buckets live in the event store, so a failed bucket read is a failed events read too and the error is returned; there is no forced gate (libs/metering SKIPPED.md, `same-store` and `no-gate`). The other cases of 1071–1731 are ported in libs/billing/buckets_test.go (`TestCurrentUsageFromBuckets`, `TestUsageBucketsCache`). |
| 1142 | One provider reads ClickHouse once for the whole computation | Tanvo's buckets are keyed by dimensions and each charge bucket reads its own totals (libs/metering SKIPPED.md, `prefetch`). |
| 1153 | Organization flag off | No realtime usage flag (parity §1.50). |
| 1284–1333, 1387, 1434 | The billing period's events pre-filter resolved from the buckets | Tanvo has no events pre-filter query to skip (libs/metering SKIPPED.md, `prefilter`). |

## spec/services/lifetime_usages/**

| Spec | Case | Reason |
| :--- | :--- | :--- |
| check_thresholds_service_spec.rb:41, 49, 84, 124, 147 | Creates the progressive billing invoice; tax provider error | Invoicing creates the invoice (`invoicing.Service.ProgressiveBilling`); usage only returns the crossed thresholds. |
| check_thresholds_service_spec.rb:33, 68, 76, 116, 139 | Webhook `subscription.usage_threshold_reached` | Ported as the crossed thresholds; emitting the webhook after the invoice is the engine's job. |
| flag_refresh_from_invoice_service_spec.rb:38 | Draft invoice | lago only checks success; covered by the type check. |
| flag_refresh_from_invoice_service_spec.rb:55–67 | No plan thresholds, organization lifetime usage flag | Tanvo always maintains lifetime usage (domain §10.3), there is no organization flag: every subscription invoice flags. |
| usage_thresholds/check_service_spec.rb (`[:subscription, :plan]` loop) | Plan-attached copy of each mapping | The mapping is ported once on the pure `CrossedThresholds`; the subscription/plan selection is tested in `TestThresholds`. |

## spec/services/usage_monitoring/**

All files test alerts (create/update/destroy, process alert, wallet and lifetime alerts, subscription activity tracking). Alerts are out of the `usage` scope (thresholds only); they are ported, where billing evaluates them, in libs/billing (`scenarios_monitoring_test.go`, `scenarios_alerts_test.go`, `scenarios_alerts_wallets_test.go`).

## spec/services/usage_thresholds/**

| Spec | Reason |
| :--- | :--- |
| update_service_table_spec.rb, override_service_spec.rb | Threshold CRUD and plan overrides belong to the catalog services, not usage. |
