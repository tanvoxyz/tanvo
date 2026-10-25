# Unported lago-api cases

These are the cases of `spec/services/credits/**`, `spec/services/coupons/**` and
`spec/services/applied_coupons/**` that have no Go test in this package, each with
the reason. Ported cases cite their source with a `// source:` comment.

## Locking and concurrency

The caller holds the customer locks (`kernel.LockCustomer`) in its own transaction.
Timing out on an advisory lock is a store concern, which the memstore can't reproduce.

| Source | Reason |
| :--- | :--- |
| `credits/applied_coupon_service_spec.rb:35` | "without lock" → `no_lock_acquired`: Tanvo takes the lock itself instead of checking that the caller holds it |
| `credits/applied_coupon_service_spec.rb:46` | The "with lock acquired" wrapper: every ported case runs under the lock |
| `credits/credit_note_service_spec.rb:53` | Concurrent lock wrapper |
| `credits/applied_prepaid_credits_service_spec.rb:761`, `:772`, `:780` | Lock timeout: a PostgreSQL integration test belongs in `libs/store` |
| `applied_coupons/create_service_spec.rb:270`, `:281`, `:289` | Same, for the coupon lock |
| `applied_coupons/recredit_service_spec.rb:46`, `:99` | `with_lock` spies: `Restore` runs in the caller's transaction under the customer lock |
| `credits/allocate_prepaid_credits_by_wallets_service_spec.rb:82`–`:84` | Spies proving that no lock or balance service is called; the side-effect-free check (`:73`) is ported |

## Activity logs, jobs and spies

| Source | Reason |
| :--- | :--- |
| `coupons/create_service_spec.rb:35`, `coupons/update_service_spec.rb:45`, `coupons/destroy_service_spec.rb:32`, `applied_coupons/create_service_spec.rb:46`, `applied_coupons/terminate_service_spec.rb:21`, `credits/applied_prepaid_credits_service_spec.rb:120` | Activity logs are a server concern (audit), outside libs/billing |
| `credits/allocate_prepaid_credits_by_wallets_service_spec.rb:80` | `SendWebhookJob` not enqueued: covered by the side-effect-free check |
| `credits/progressive_billing_service_spec.rb:577` | Spy on `Subscriptions::ProgressiveBilledAmount`; the amount is tested directly in `TestProgressiveBilledAmount` |
| `credits/applied_coupons_service_spec.rb` "already loaded association" (`:86` context) | ActiveRecord association caching has no Go counterpart |

## API layer

| Source | Reason |
| :--- | :--- |
| `coupons/create_service_spec.rb:73` | Non-ISO 8601 `expiration_at`: parsing belongs to the API layer; `time.Time` can't be malformed |
| `coupons/create_service_spec.rb:145`, `:227`; `coupons/update_service_spec.rb:92`, `:119`, `:136`, `:197` | GraphQL vs REST context (ids vs codes): the API layer resolves codes to ids; the id-based behaviour is ported |

## Billing entities

| Source | Reason |
| :--- | :--- |
| `credits/applied_prepaid_credits_service_spec.rb:686`, `:727` | Lago never gates wallets by billing entity, only by currency. Tanvo has the same rule: `walletsForApplication` filters by currency and balance only, and the wallet currency mismatch is covered by `:660`/`:675`. A literal port needs a second billing-entity fixture in billingtest, which only shows that a field is never read |

## Deviations (tested with Tanvo's behaviour)

| Source | Lago | Tanvo |
| :--- | :--- | :--- |
| `credits/applied_coupon_service_spec.rb:76` | A zero base creates a credit of 0 and consumes the coupon | The coupon is skipped and left untouched (`TestApplyCouponZeroBase`) |
| `applied_coupons/create_service_spec.rb:214` | An inactive coupon answers `coupon_not_found` | `coupon_not_active` |
| `coupons/update_service_spec.rb` (applied coupon) | Locked fields are silently ignored | Rejected with a validation error |
| `credits/progressive_billing_service_spec.rb:534` | Creates a credit note of zero | Reports the note items; the credit notes service decides whether to create it |
| `credits/applied_coupons_service_spec.rb:350` | The fixture totals are inconsistent | The expected totals are recomputed from the fees |
| `credits/credit_note_service_spec.rb:170` | The fixture's invoice total disagrees with its fees | Fee-level amounts are not checked |
