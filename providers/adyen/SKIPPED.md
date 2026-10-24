# Unported spec cases

Spec cases of the Adyen integration that are not ported to `providers/adyen`, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case.

Unported: 69 cases.

## Reasons

- **orchestration** (42): Record orchestration: loads or saves rows, changes payment, invoice, payment request, credit note or customer status, enqueues jobs, delivers outgoing webhooks, sends emails or writes activity logs. This belongs to apps/server and libs/billing, which consume the gateway's typed results (payment.PaymentResult, payment.RefundResult, payment.Event); nothing here talks to Adyen beyond what is already ported.
- **lookup** (9): Record lookup (organization, connection, customer, refund or invoice not found, or of another organization). ParseWebhook returns the Tanvo and Adyen identifiers as payment.Refs; resolving them, and choosing the connection of an inbound webhook, is apps/server's job.
- **caller** (1): Caller-side decision: whether a customer needs a setup link is decided by the caller before calling SetupURL (libs/billing or apps/server).
- **model** (5): Model concern of the integration domain (associations, code uniqueness, persisted provider customer columns); the gateway reads settings through parseConn, covered by TestSettings and TestValidateConnection.
- **job** (4): Job wrapper that only calls a service; the job layer lives in apps/server.
- **backfill** (8): One-off data migration of lago's legacy adyen_customer.payment_method_id column into payment_methods rows. Tanvo stores saved methods as rows from the start (payment.PaymentMethodAttached), so there is nothing to backfill.

## Ported instead of deviating

- `services/payment_providers/adyen/handle_event_service_spec.rb:107` (unknown payable type): ported in TestParseWebhook. ParseWebhook returns `payment.ErrUnknownPayable`, which matches `payment.ErrRejected`, and no event; the handler acknowledges the delivery and updates no record, as lago's NameError updates nothing.
- `services/credit_notes/refunds/adyen_service_spec.rb:321` (invalid refund status) is orchestration: ParseWebhook emits only the succeeded and failed refund statuses, so an invalid status cannot reach the caller from Adyen.

## Cases

### models/payment_providers/adyen_provider_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | (unnamed) | model |
| 9 | (unnamed) | model |
| 14 | validations > "validates uniqueness of the code" | model |

### models/payment_provider_customers/adyen_customer_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 18 | #payment_method_id > "returns the payment method id" | model |
| 24 | #require_provider_payment_id? > (unnamed) | model |

### services/payment_providers/adyen/customers/create_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 15 | .call > "creates a payment_provider_customer" | orchestration |
| 28 | .call > when no provider customer id and should create on service > "enqueues a job to create the customer on the provider" | orchestration |
| 48 | .call > when removing the provider customer id and should create on service > "updates the provider customer" | orchestration |
| 78 | .call > when provider customer id is set > when sync with provider is blank > when provider customer exists > "generates checkout url" | orchestration |
| 83 | .call > when provider customer id is set > when sync with provider is blank > when provider customer exists > "does not create customer" | orchestration |
| 90 | .call > when provider customer id is set > when sync with provider is blank > when provider customer does not exist > "does not generate checkout url" | orchestration |
| 95 | .call > when provider customer id is set > when sync with provider is blank > when provider customer does not exist > "does not create customer" | orchestration |
| 106 | .call > when provider customer id is set > when sync with provider is true > "does not generate checkout url" | orchestration |
| 111 | .call > when provider customer id is set > when sync with provider is true > "does not enqueue a job to create the customer on the provider" | orchestration |

### services/payment_providers/adyen/handle_incoming_webhook_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 61 | #call > when organization does not exist > "returns an error" | lookup |
| 74 | #call > when payment provider does not exist > "returns an error" | lookup |
| 100 | #call > when multiple payment providers exists and no code is provided > "returns an error" | lookup |

### services/payment_providers/adyen/webhooks/chargeback_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 27 | #call > when dispute is lost > when invoice is draft > "does not updates invoice payment dispute lost" | orchestration |
| 34 | #call > when dispute is lost > when invoice is draft > "does not deliver webhook" | orchestration |

### services/credit_notes/refunds/adyen_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 77 | #create > "call SegmentTrackJob" | orchestration |
| 137 | #create > with an error on adyen > "produces an activity log" | orchestration |
| 170 | #create > when invoice does not have a payment > "does not create a refund" | orchestration |
| 185 | #create > when dispute was lost > "does not create a refund" | orchestration |
| 200 | #create > when payment provider customer was discarded > "creates a adyen refund and a refund" | orchestration |
| 230 | #create > when the provider was disconnected and reconnected > "attaches the refund to the provider customer that holds the payment" | lookup |
| 260 | #update_status > "calls SegmentTrackJob" | orchestration |
| 280 | #update_status > when refund is not found > "returns an empty result" | lookup |
| 291 | #update_status > when refund is not found > with invoice id in metadata > "returns an empty result" | lookup |
| 305 | #update_status > when refund is not found > with invoice id in metadata > when invoice belongs to lago > "returns a not found failure" | lookup |
| 321 | #update_status > when status is not valid > "fails" | orchestration |
| 363 | #update_status > when status is failed > "produces an activity log" | orchestration |

### services/invoices/payments/adyen_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 52 | .call(:update_payment_status) > "updates the payment and invoice payment_status" | orchestration |
| 70 | .call(:update_payment_status) > "enqueues a SendWebhookJob for payment.succeeded" | orchestration |
| 81 | .call(:update_payment_status) > it_behaves_like "syncs payment" | orchestration |
| 93 | .call(:update_payment_status) > when the customer has no accounting integration > "does not enqueue an accounting payment sync job" | orchestration |
| 129 | .call(:update_payment_status) > when status is failed > when the customer has an accounting integration syncing payments > "does not enqueue an accounting payment sync job" | orchestration |
| 145 | .call(:update_payment_status) > when invoice is already payment_succeeded > "does not update the status of invoice and payment" | orchestration |
| 159 | .call(:update_payment_status) > with invalid status > "does not update the payment_status of invoice" | orchestration |
| 182 | .call(:update_payment_status) > when payment is not found and it is one time payment > "creates a payment and updates invoice payment status" | orchestration |
| 210 | .call(:update_payment_status) > when the one time payment references an invoice of another organization > "does not create a payment and returns a not found failure" | lookup |

### services/payment_requests/payments/adyen_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 146 | .call(:update_payment_status) > "updates the payment, payment_request and invoices payment_status" | orchestration |
| 160 | .call(:update_payment_status) > "does not send payment requested email" | orchestration |
| 186 | .call(:update_payment_status) > when the payment request belongs to a dunning campaign > "resets the customer dunning campaign counters for the payment request currency" | orchestration |
| 198 | .call(:update_payment_status) > when the payment request belongs to a dunning campaign > when status is failed > "doest not reset the customer dunning campaign counters" | orchestration |
| 211 | .call(:update_payment_status) > when status is failed > "updates the payment, payment_request and invoices status" | orchestration |
| 229 | .call(:update_payment_status) > when status is failed > "sends a payment requested email" | orchestration |
| 246 | .call(:update_payment_status) > when payment_request and invoices is already payment_succeeded > "does not update the status of invoices, payment_request and payment" | orchestration |
| 256 | .call(:update_payment_status) > when payment_request and invoices is already payment_succeeded > "does not send payment requested email" | orchestration |
| 270 | .call(:update_payment_status) > when a failed webhook arrives after the invoices were already paid through another path > "leaves already-succeeded invoices untouched" | orchestration |
| 284 | .call(:update_payment_status) > with invalid status > "does not update the payment_status of payment_request, invoices and payment" | orchestration |
| 292 | .call(:update_payment_status) > with invalid status > "returns an error" | orchestration |
| 299 | .call(:update_payment_status) > with invalid status > "does not send payment requested email" | orchestration |
| 313 | .call(:update_payment_status) > when payment is not found and it is one time payment > "creates a payment and updates payment request and invoices payment status" | orchestration |

### services/payment_provider_customers/adyen_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 38 | .call(:create) > when customer does not have an adyen customer id yet > "delivers a success webhook" | orchestration |
| 54 | .call(:create) > when customer already has an adyen customer id > "does not call adyen API" | caller |
| 155 | .call(:generate_checkout_url) > when adyen payment provider is nil > "returns a not found error" | lookup |
| 171 | .call(:generate_checkout_url) > when adyen payment provider is present > "delivers a success webhook" | orchestration |

### jobs/credit_notes/refunds/adyen_create_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 12 | "delegates to the adyen refund service" | job |

### jobs/invoices/payments/adyen_create_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | "calls the stripe create service" | job |

### jobs/payment_providers/adyen/handle_event_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 17 | "calls the handle event service" | job |

### jobs/payment_requests/payments/adyen_create_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 16 | "calls the stripe create service" | job |

### jobs/database_migrations/backfill_adyen_payment_methods_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 18 | when adyen customer has a payment_method_id > "creates a payment method" | backfill |
| 24 | when adyen customer has a payment_method_id > "sets the correct attributes on the payment method" | backfill |
| 43 | when adyen customer has no payment_method_id > "does not create a payment method" | backfill |
| 51 | when payment method already exists for the adyen customer > "does not create a duplicate" | backfill |
| 59 | when a discarded payment method already exists for the adyen customer > "does not recreate the discarded payment method" | backfill |
| 76 | with organization_id filter > "only processes the specified organization" | backfill |
| 91 | when there is more work after the batch > "enqueues the next batch" | backfill |
| 103 | when there is no pending work > "does not enqueue any job" | backfill |
