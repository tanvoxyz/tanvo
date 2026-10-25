# Unported spec cases

Spec cases of the usage-refreshed streaming events that are not ported, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case of the files in `testdata/specs.json` (checked by `go run ./tools/speccov`).

Cases: 81. Ported: 64. Unported: 17.

## Reasons

- **refresh** (16): This module owns only the streaming block of the wallet refresh (refresh_wallets_service_spec.rb:102, the cases cited by libs/billing/streaming/service_test.go); the refresh itself is ported in libs/billing/wallets.
- **unknown-type** (1): A destination cannot claim an unknown event type: the schema refuses it (streaming_destinations_event_types_check in libs/store/schema/29_streaming.sql) and so does Destination.Validate, so no stored destination asks for another type.

## Cases

### services/customers/refresh_wallets_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 77 | "calls Wallets::Balance::RefreshOngoingUsageService for each active wallet" | refresh |
| 87 | "refreshes the wallet balances" | refresh |
| 98 | "marks customer as not awaiting wallet refresh" | refresh |
| 248 | when the destination asks for another event type > "enqueues nothing" | unknown-type |
| 260 | when charges have presentation_group_keys > "calls CustomerUsageService with UsageFilters::WITHOUT_PRESENTATION_FILTER" | refresh |
| 273 | when charges have presentation_group_keys > "calls AggregationFactory with presentation_by as empty array" | refresh |
| 316 | when generating invoices are included > "returns current usage for customer including generating invoices" | refresh |
| 330 | when generating invoices are excluded > "returns current usage for customer excluding generating invoices" | refresh |
| 356 | when failed to calculate current usage > "fails with an error" | refresh |
| 390 | when the customer has multiple wallets > "refreshes every wallet together" | refresh |
| 401 | when the customer has multiple wallets > "updates last_ongoing_balance_sync_at on every wallet" | refresh |
| 408 | when the customer has multiple wallets > "returns all active wallets in the result" | refresh |
| 476 | when there are wallet billable metric limitations > "only counts targeted billable metric fees for the targeted wallet" | refresh |
| 487 | when there are wallet billable metric limitations > "counts remaining fees for the unrestricted wallet" | refresh |
| 589 | when customer has multi-currency subscriptions > "calculates ongoing balance separately per currency" | refresh |
| 628 | when ongoing usage cascades across multiple wallets > "fills the priority wallet then spills the overflow onto the next" | refresh |
| 640 | when ongoing usage cascades across multiple wallets > when the priority wallet has an active threshold-based recurring rule > "keeps the legacy behavior: the threshold wallet goes negative and does not cascade" | refresh |
