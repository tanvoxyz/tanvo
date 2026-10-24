# Unported spec cases

Spec cases of the MoneyHash integration that are not ported to `providers/moneyhash`, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case.

Unported: 45 cases.

## Reasons

- **orchestration** (21): Record orchestration: loads or saves rows, changes payment, invoice, payment request, customer or payment method records, enqueues jobs, delivers outgoing webhooks or writes security logs. This belongs to apps/server and libs/billing, which consume the gateway's typed results (payment.PaymentResult, payment.Event with its amount, status and payment.Refs); nothing here talks to MoneyHash beyond what is already ported.
- **lookup** (6): Record lookup (connection, customer, payable or payment method not found). ParseWebhook returns the Tanvo and MoneyHash identifiers as payment.Refs and provider IDs; resolving them is apps/server's job.
- **caller** (4): Caller-side decision taken before calling the gateway (whether a customer already exists on MoneyHash, whether the payable is already paid, has a zero amount or a connection at all); it lives in libs/billing.
- **model** (4): Model concern of the integration domain (connection name, code uniqueness, persisted provider customer columns); the gateway reads its settings through parseConn, covered by TestValidateConnection.
- **webhookurl** (1): The inbound webhook endpoint (webhooks/moneyhash/<organization>?code=<code>) is served and built by apps/server, which passes it to the adapter as payment.Connection.WebhookURL; TestSetupURL and TestCreatePayment check it is sent with every intent.
- **job** (1): Job wrapper that only calls a service; the job layer lives in apps/server.
- **backfill** (8): One-off data migration of lago's legacy moneyhash_customer.payment_method_id column into payment_methods rows. Tanvo stores saved cards as payment method rows from the start (payment.PaymentMethodAttached), so there is nothing to backfill.

## Ported with a deviation

- `services/payment_providers/moneyhash/validate_incoming_webhook_service_spec.rb:14` (valid signature): ported in TestVerifyWebhook with the spec's vector. Lago signs its re-serialization of the parsed body; the body as delivered is verified here.
- `services/payment_providers/moneyhash/handle_event_service_spec.rb:27`, `:40` (intent events): ported in TestParseWebhookPayments. Lago reads the payable type of an intent event from `intent.custom_fields`, which intent events lack, so it always takes them for invoices; the event's own `data.intent.custom_fields` are read here (TestParseWebhookPaymentRequest).
- `services/payment_providers/moneyhash/handle_event_service_spec.rb:171` (card details): ported in TestParseWebhookCards. payment.MethodDetails has no card holder name or issuer, so those two are not reported; the two-digit expiry year is reported as a four-digit year.
- `services/invoices/payments/moneyhash_service_spec.rb:101` and `services/payment_provider_customers/moneyhash_service_spec.rb:41` (payment and checkout URLs): ported in TestCreatePaymentURL and TestSetupURL. MoneyHash's embed URL is returned as is; lago appends a `lago_request` marker that nothing reads.
- `services/payment_providers/moneyhash/payments/create_service_spec.rb:53` (refused payment): ported in TestCreatePaymentFailures. The error code is the HTTP status as in lago; the message is the answer's field errors instead of the raw body.

## Cases

### jobs/database_migrations/backfill_moneyhash_payment_methods_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 18 | when moneyhash customer has a payment_method_id > "creates a payment method" | backfill |
| 24 | when moneyhash customer has a payment_method_id > "sets the correct attributes on the payment method" | backfill |
| 43 | when moneyhash customer has no payment_method_id > "does not create a payment method" | backfill |
| 51 | when payment method already exists for the moneyhash customer > "does not create a duplicate" | backfill |
| 59 | when a discarded payment method already exists for the moneyhash customer > "does not recreate the discarded payment method" | backfill |
| 76 | with organization_id filter > "only processes the specified organization" | backfill |
| 91 | when there is more work after the batch > "enqueues the next batch" | backfill |
| 103 | when there is no pending work > "does not enqueue any job" | backfill |

### jobs/payment_providers/moneyhash/handle_event_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 17 | "calls the handle event service" | job |

### models/payment_provider_customers/moneyhash_customer_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 18 | #payment_method_id > "returns the payment method id" | model |
| 24 | #require_provider_payment_id? > (unnamed) | model |

### models/payment_providers/moneyhash_provider_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 10 | (unnamed) | model |
| 16 | validations > "validates uniqueness of the code" | model |
| 74 | #webhook_end_point > "returns the correct webhook endpoint URL" | webhookurl |

### services/invoices/payments/moneyhash_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 27 | .call(:update_payment_status) > "creates a payment and updates the invoice payment status" | orchestration |
| 44 | .call(:update_payment_status) > "enqueues a SendWebhookJob for payment.succeeded" | orchestration |
| 57 | .call(:update_payment_status) > when amount_cents kwarg is provided > "records the Payment with that amount, not the invoice total" | orchestration |
| 72 | .call(:update_payment_status) > when amount_cents kwarg is not provided > "falls back to the invoice's total amount" | orchestration |

### services/payment_provider_customers/moneyhash_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 19 | .call(:create) > when provider_customer_id is already present > "does not call moneyhash customers API" | caller |
| 110 | .call(:create) > .call(:update_payment_method) > "returns result directly when lago_customer_id is not present" | lookup |
| 121 | .call(:create) > .call(:update_payment_method) > "returns result directly when customer is not found" | lookup |
| 133 | .call(:create) > .call(:update_payment_method) > "returns a failure when moneyhash customer is not found" | lookup |
| 147 | .call(:create) > .call(:update_payment_method) > "creates a PaymentMethod record" | orchestration |
| 166 | .call(:create) > .call(:update_payment_method) > "returns the payment_method in the result" | orchestration |
| 209 | .call(:create) > .call(:update_payment_method) > when card_details is empty > "does not call UpdateDetailsService" | orchestration |
| 223 | .call(:create) > .call(:update_payment_method) > "finds existing PaymentMethod instead of creating duplicate" | orchestration |
| 243 | .call(:create) > .call(:update_payment_method) > "does not create PaymentMethod when payment_method_id is nil" | orchestration |
| 293 | .call(:create) > .call(:delete_payment_method) > "clears the default payment_method_id when it matches" | orchestration |
| 304 | .call(:create) > .call(:delete_payment_method) > "does not clear payment_method_id when it does not match" | orchestration |
| 327 | .call(:create) > .call(:delete_payment_method) > when the customer has a payment method > "soft-deletes the PaymentMethod record" | orchestration |
| 337 | .call(:create) > .call(:delete_payment_method) > when the customer has a payment method > "does not fail when PaymentMethod record does not exist" | orchestration |
| 350 | .call(:create) > .call(:delete_payment_method) > "returns a failure when moneyhash customer is not found" | lookup |

### services/payment_providers/moneyhash/handle_event_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 223 | #handle_card_event > when event is card_token.deleted > when not the same card > "does not clear the default payment_method_id" | orchestration |
| 242 | #handle_card_event > when event is card_token.deleted > when a PaymentMethod record exists > "soft-deletes the PaymentMethod record" | orchestration |

### services/payment_providers/moneyhash/handle_incoming_webhook_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 25 | when failing to find the provider > "returns an error" | lookup |

### services/payment_providers/moneyhash_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 30 | #create_or_update > it_behaves_like "produces a security log", "integration.created" | orchestration |
| 45 | #create_or_update > it_behaves_like "produces a security log", "integration.updated" | orchestration |

### services/payment_requests/payments/moneyhash_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 107 | .call(:create) > when payment should not be processed > when payment already succeeded > "returns success without payment" | caller |
| 118 | .call(:create) > when payment should not be processed > when moneyhash provider is missing > "returns success without payment" | caller |
| 130 | .call(:create) > when payment amount is zero > "marks payment as succeeded without processing" | caller |
| 220 | .call(:update_payment_status) > when payment exists > "updates payment, payable and invoices status" | orchestration |
| 242 | .call(:update_payment_status) > when payment does not exist > "creates a new payment" | orchestration |
| 264 | .call(:update_payment_status) > when payment does not exist > when payable is not found > "returns not found error" | lookup |
| 286 | .call(:update_payment_status) > when payment already succeeded > "does not update the status" | orchestration |
| 308 | .call(:update_payment_status) > when a failed webhook arrives after the invoices were already paid through another path > "leaves already-succeeded invoices untouched" | orchestration |
