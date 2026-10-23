# Unported lago-api cases

The lago-api specs `spec/services/wallets/**` and `spec/services/wallet_transactions/**`
are ported into the `*_test.go` files of this package and into
`kernel/wallets_balance_test.go`. Each ported case has a `// source:` comment.
This file lists the cases that were not ported, grouped by reason.

## Transport, parsing and lookup

Tanvo services take typed inputs. The API layer resolves organizations, customers,
external IDs and codes, and it parses strings.

- `wallets/create_service_spec.rb`, `wallets/validate_service_spec.rb`, `wallets/update_service_spec.rb`: the GraphQL and REST context switches; the customer lookup by `external_customer_id`; a blank or invalid `organization_id`; `billing_entity_code` resolution.
- `wallets/validate_service_spec.rb`: expiration given as an integer or a non-date string; `rate_amount` and credits given as `"foobar"`.
- `wallet_transactions/validate_service_spec.rb:38,47,59,63`: the wallet looked up through a customer (`customer_id`). The `wallet_not_found` case is ported.
- `wallet_transactions/validate_service_spec.rb:75,84,199`: credits given as `"foobar"`. The negative and ≥ 10^25 cases carry the same codes and are ported.
- `wallet_transactions/validate_service_spec.rb:275,365`: metadata with a nested value, and a name that is not a string. Metadata and names are typed.
- `wallet_transactions/validate_service_spec.rb:116`, `wallets/create_service_spec.rb` "10^25 − 1 valid": amounts are `int64` minor units. 10^25 − 1 credits overflow them, so the input is rejected as `invalid_amount` instead.
- `wallet_transactions/create_from_params_service_spec.rb:229`: a `nil` name next to an absent name. Both are `nil` in Go.

## Features outside this package or not in Tanvo

- Purchase order numbers: `create_from_params_service_spec.rb:183`, `threshold_top_up_service_spec.rb` (four cases), `create_interval_wallet_transactions_service_spec.rb:455,482,519`, `invoices/paid_credit_service_spec.rb:60–102,285–310`. Tanvo does not have purchase order numbers.
- Invoice custom sections: `create_from_params_service_spec.rb:483,506,523`, `threshold_top_up_service_spec.rb` (two cases), `create_interval_wallet_transactions_service_spec.rb:534`, the `paid_credit_service` custom-section cases. These belong to the invoice document layer.
- `voided_invoice_id` on recredit (`recredit_service_spec.rb:49`, `create_from_params_service_spec.rb:199`): `wallet.Transaction` has no field for the voided invoice. See the report.
- Billing entity snapshots (`create_service_spec.rb:187–227`, `void_service_spec.rb:129`, `paid_credit_service_spec.rb:111–126,336`): `wallet.Transaction` has no `BillingEntityID`, so a credit invoice uses the wallet's current entity.
- Wallet metadata and `partial_metadata` on update. Wallet metadata is a core `Metadata` value, and the API replaces it.
- `events_targeting_wallets`: fees targeting a wallet code (`allocate_ongoing_usage_by_wallets_service_spec.rb:280,300`). That is a premium integration of usage routing.
- `wallets/realtime_refresh_service_spec.rb`: lago's synchronous refresh on event ingestion. Tanvo refreshes through the job queue.
- `wallet_transactions/payments/generate_payment_url_service_spec.rb`: payment URLs belong to the payments package.
- Activity logs, Segment tracking, the e-mail sending of `paid_credit_service_spec.rb:201–222`. Tanvo has none of these. The `ProcessWalletAlertsJob` enqueues of the balance specs are ported in libs/billing `TestWalletAlertsEnqueued`.
- Premium and freemium gating of the wallet limit and of `invoice_requires_successful_payment`. Tanvo applies the organization's limit directly.

## Mechanics with no Go equivalent

- Lock acquisition failures (`void_service_spec.rb:715–737`) and stale-object retries (`decrease_service_spec.rb` "when wallet is stale"; `allocate_ongoing_usage_by_wallets_service_spec.rb:320` stale in-memory balance). Tanvo locks rows inside `Store.InTx` and reloads the wallet under the lock.
- The `awaiting_wallet_refresh` assertions of `decrease_service_spec.rb:43,63`, `update_service_spec.rb:75–91` and `create_from_params_service_spec.rb:76`. Every one of these paths flags through `kernel.EnqueueWalletRefresh`, whose flag rule (active wallet required) is covered by the store port tests and by the create and terminate tests; `skip_refresh` does not exist in Tanvo.
- `skip_refresh` and `skip_single_wallet_update` (`decrease_service_spec.rb` skip-refresh cases, `refresh_ongoing_usage_service_spec.rb:94`, `update_ongoing_service_spec.rb:110`), and `last_ongoing_balance_sync_at` (`update_ongoing_service_spec.rb:32,62`). The customer-wide refresh writes every wallet at once, and `wallet.Wallet` has no ongoing-sync timestamp.
- The `CreateJob` parameter shape (`threshold_top_up_service_spec.rb:35` and the other `have_enqueued_job` assertions). Top-ups run inline, so the tests assert the transactions created rather than the job arguments.
- `void_service_spec.rb:197`: the stale snapshot of the remaining amount. `void` re-reads the inbound transaction under the lock. The race itself cannot be set up through the service API.
- `prepaid_credit_job_spec.rb:48,54–59,101–121`: job retry policy and argument defaults.
- `recurring_transaction_rules/terminate_service_spec.rb:22,34`: `Rule.Terminate` in `libs/core/wallet` covers these, and `TestExpireWallets` exercises it.

## Different behavior (ported with the Tanvo outcome)

- Paid credits settle inline when the credit invoice does not require a successful payment (`create_from_params_service_spec.rb:69`: the balance is 3200 rather than 2200, because the purchase is included).
- Granted credits use the invoiceable conversion, as lago's `WalletCredit` does: whole minor units only (`create_from_params_service_spec.rb:539`).
- `threshold_top_up_service_spec.rb:88`: a rule that keeps the wallet limits, with paid credits below the minimum, returns `paid_credits/amount_below_minimum`. Lago instead enqueues a job that fails later.
- `void_service_spec.rb:207`: voiding the remaining amount of a fully consumed grant is rejected during validation (`voided_transaction_id/no_remaining_amount`) instead of succeeding silently.
- A void defaults to the wallet's priority, like the other transactions.
- Error codes:
  - wallet priority: `value_is_out_of_range` (lago `value_is_invalid`)
  - currency: `currency_invalid`
  - the wallet limit: `customer_id/wallets_limit_reached`
  - metadata: `metadata.items/…` and `items[i].key`
- An interval rule has a nil threshold (lago `0.0`).
- A target rule requires `target_ongoing_balance` at creation.
- Explicit null credits on a rule update keep the current value, where lago coerces them to 0. A nil `transaction_name` also keeps the current name.
