# Skipped lago-api cases (quotes)

These are the cases of the quote flow specs (`spec/services/quotes/**`, `quote_versions/**`, `order_forms/**`, `orders/**`) that are not ported to `libs/billing/quotes`, with the reason for each. Cases of the version validators are in `validators/SKIPPED.md`.

| Source | Reason |
| :--- | :--- |
| `without premium license`, `when license is not premium`, `when the order_forms feature flag is disabled`, and `when feature flag is disabled` contexts in every spec | Tanvo has no premium license and no feature flags, so the quote flow is always available. |
| `produces a … activity log` expectations | The activity logs are the domain events emitted here (webhook.Event). |
| `with concurrent mutations` (`wraps the work in a per-quote lock`, `re-checks the status under the lock …`) | The quote lock is taken before the status is read again. A real race needs a PostgreSQL store; the lock is a memstore no-op. |
| `when the quote lock cannot be acquired` (`returns a concurrency conflict instead of raising`) | Tanvo's lock waits instead of failing fast, so a lock error is returned as is rather than mapped to concurrency_conflict. |
| `quotes/lock_service_spec.rb` | Lock acquisition is the store's (port.Locker). |
| `when saving fails` and `when order creation fails` (`rolls back without persisting a blob`, `mark_as_signed_service_spec.rb:228`, `:265`) | The blob is written before the transaction commits; an orphan blob after a rollback is harmless and is not referenced. |
| `when the … creation fails with a non-validation failure` (`approve_service_spec.rb:150`, `create_service_spec.rb:214`) | These stub the inner service. Tanvo's errors propagate through the transaction, so nothing is persisted. |
| `orders/execute_service_spec.rb:52`, `:64`, `:85` (`delegates to …`) | The dispatch is internal; the execution of each order type is tested directly. |
| `orders/one_off/execute_service_spec.rb:256` (taxes deferred to a provider) | Provider taxes on one-off invoices are covered by invoicing's tests. |
| `quotes/create_service_spec.rb:243` (organization does not exist) | The organization comes from the authenticated principal. |
| `webhooks/quotes/**`, `webhooks/order_forms/**`, `webhooks/orders/**` | The payloads are rendered by the webhooks library from the emitted objects. |
