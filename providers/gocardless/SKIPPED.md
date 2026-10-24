# Unported spec cases

Spec cases of the GoCardless integration that are not ported to `providers/gocardless`, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case.

Unported: 66 cases.

## Reasons

- **orchestration** (33): Record orchestration: loads or saves rows, changes payment, invoice, payment request, credit note, refund, customer or payment method records, enqueues jobs, delivers outgoing webhooks, sends emails, tracks analytics or writes activity and security logs. This belongs to apps/server and libs/billing, which consume the gateway's typed results (payment.PaymentResult, payment.RefundResult, payment.Event); nothing here talks to GoCardless beyond what is already ported.
- **lookup** (6): Record lookup (connection, customer, payment, refund or payment method not found). ParseWebhook returns the Tanvo and GoCardless identifiers as payment.Refs and provider IDs; resolving them is apps/server's job.
- **caller** (11): Caller-side decision taken before calling the gateway (whether a customer already exists on GoCardless, whether an invoice has a payment to refund, whether a dispute was lost, which provider customer holds the payment); it lives in libs/billing or apps/server.
- **model** (3): Model concern of the integration domain (associations, code uniqueness, persisted provider customer columns); the gateway reads settings through parseConn, covered by TestSettings and TestValidateConnection.
- **job** (5): Job wrapper that only calls a service; the job layer lives in apps/server.
- **backfill** (8): One-off data migration of lago's legacy gocardless_customer.provider_mandate_id column into payment_methods rows. Tanvo stores mandates as payment method rows from the start (payment.PaymentMethodAttached), so there is nothing to backfill.

## Ported with a deviation

- `services/payment_providers/gocardless/handle_event_service_spec.rb:55` (unknown payable type): ported in TestParseWebhookUnknownPayable. Lago raises NameError for the whole delivery; as one GoCardless delivery batches several events, ParseWebhook skips the event with a logged warning and returns the others.
- `services/payment_providers/gocardless/webhooks/mandate_created_service_spec.rb:62` (mandate fetch fails): ported in TestParseWebhookMandateCreatedFetchFails. Lago swallows every fetch error; only a missing mandate yields no event here, and other failures fail the delivery so GoCardless redelivers it.

## Cases

### models/payment_providers/gocardless_provider_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 11 | (unnamed) | model |
| 16 | validations > "validates uniqueness of the code" | model |

### models/payment_provider_customers/gocardless_customer_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 11 | #require_provider_payment_id? > (unnamed) | model |

### jobs/invoices/payments/gocardless_create_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | "calls the stripe create service" | job |

### jobs/database_migrations/backfill_gocardless_payment_methods_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 18 | when gocardless customer has a provider_mandate_id > "creates a payment method" | backfill |
| 24 | when gocardless customer has a provider_mandate_id > "sets the correct attributes on the payment method" | backfill |
| 43 | when gocardless customer has no provider_mandate_id > "does not create a payment method" | backfill |
| 51 | when payment method already exists for the gocardless customer > "does not create a duplicate" | backfill |
| 59 | when a discarded payment method already exists for the gocardless customer > "does not recreate the discarded payment method" | backfill |
| 76 | with organization_id filter > "only processes the specified organization" | backfill |
| 91 | when there is more work after the batch > "enqueues the next batch" | backfill |
| 103 | when there is no pending work > "does not enqueue any job" | backfill |

### jobs/credit_notes/refunds/gocardless_create_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 12 | "delegates to the gocardless refund service" | job |

### jobs/payment_requests/payments/gocardless_create_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 16 | "calls the stripe create service" | job |

### jobs/payment_provider_customers/gocardless_create_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | "calls the gocardless create service" | job |

### jobs/payment_provider_customers/gocardless_checkout_url_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 10 | "calls generate_checkout_url method" | job |

### services/invoices/payments/gocardless_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 42 | .call(:update_payment_status) > "updates the payment and invoice payment_status" | orchestration |
| 59 | .call(:update_payment_status) > "enqueues a SendWebhookJob for payment.succeeded" | orchestration |
| 69 | .call(:update_payment_status) > it_behaves_like "syncs payment" | orchestration |
| 80 | .call(:update_payment_status) > when the customer has no accounting integration > "does not enqueue an accounting payment sync job" | orchestration |
| 92 | .call(:update_payment_status) > when status is failed > "updates the payment and invoice status" | orchestration |
| 114 | .call(:update_payment_status) > when status is failed > when the customer has an accounting integration syncing payments > "does not enqueue an accounting payment sync job" | orchestration |
| 129 | .call(:update_payment_status) > when invoice is already payment_succeeded > "does not update the status of invoice and payment" | orchestration |
| 142 | .call(:update_payment_status) > with invalid status > "does not update the payment_status of invoice" | orchestration |

### services/credit_notes/refunds/gocardless_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 192 | #create > when invoice does not have a payment > "does not create a refund" | caller |
| 207 | #create > when dispute was lost > "does not create a refund" | caller |
| 222 | #create > when payment provider customer was discarded > "creates a gocardless refund" | caller |
| 251 | #create > when the provider was disconnected and reconnected > "attaches the refund to the provider customer that holds the payment" | caller |
| 271 | #update_status > "updates the refund status" | orchestration |
| 285 | #update_status > "calls SegmentTrackJob" | orchestration |
| 303 | #update_status > when refund is not found > "returns an empty result" | lookup |
| 333 | #update_status > when status is failed > "delivers an error webhook" | orchestration |
| 357 | #update_status > when status is failed > "produces an activity log" | orchestration |

### services/payment_providers/gocardless_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 45 | .create_or_update > it_behaves_like "produces a security log", "integration.created" | orchestration |
| 74 | .create_or_update > when code was changed > "updates payment provider codes of all customers" | orchestration |
| 113 | .create_or_update > when organization already have a gocardless provider > it_behaves_like "produces a security log", "integration.updated" | orchestration |

### services/payment_requests/payments/gocardless_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 91 | .call(:update_payment_status) > "does not send payment requested email" | orchestration |
| 117 | .call(:update_payment_status) > when the payment request belongs to a dunning campaign > "resets the customer dunning campaign counters for the payment request currency" | orchestration |
| 129 | .call(:update_payment_status) > when the payment request belongs to a dunning campaign > when status is failed > "doest not reset the customer dunning campaign counters" | orchestration |
| 142 | .call(:update_payment_status) > when status is failed > "updates the payment, payment_request and invoice status" | orchestration |
| 157 | .call(:update_payment_status) > when status is failed > "sends a payment requested email" | orchestration |
| 167 | .call(:update_payment_status) > when payment is not found > "returns a not found error" | lookup |
| 184 | .call(:update_payment_status) > when payment_request and invoice is already payment_succeeded > "does not update the status of invoice, payment_request and payment" | orchestration |
| 194 | .call(:update_payment_status) > when payment_request and invoice is already payment_succeeded > "does not send payment requested email" | orchestration |
| 208 | .call(:update_payment_status) > when a failed webhook arrives after the invoice was already paid through another path > "leaves already-succeeded invoices untouched" | orchestration |
| 222 | .call(:update_payment_status) > with invalid status > "does not update the payment_status of payment_request, invoice and payment" | orchestration |
| 230 | .call(:update_payment_status) > with invalid status > "returns an error" | orchestration |
| 237 | .call(:update_payment_status) > with invalid status > "does not send payment requested email" | orchestration |

### services/payment_provider_customers/gocardless_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 51 | .call(:create) > "delivers a success webhook" | orchestration |
| 59 | .call(:create) > "triggers checkout job" | orchestration |
| 72 | .call(:create) > when customer already have a gocardless customer id > "does not call gocardless API" | caller |

### services/payment_providers/gocardless/customers/create_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 19 | .call > "creates a payment_provider_customer" | orchestration |
| 32 | .call > when no provider customer id and should create on service > "enqueues a job to create the customer on the provider" | orchestration |
| 52 | .call > when removing the provider customer id and should create on service > "updates the provider customer" | orchestration |
| 82 | .call > when provider customer id is set > when sync with provider is blank > when provider customer exists > "generates checkout url" | caller |
| 87 | .call > when provider customer id is set > when sync with provider is blank > when provider customer exists > "does not create customer" | caller |
| 94 | .call > when provider customer id is set > when sync with provider is blank > when provider customer does not exist > "does not generate checkout url" | caller |
| 99 | .call > when provider customer id is set > when sync with provider is blank > when provider customer does not exist > "does not create customer" | caller |
| 109 | .call > when provider customer id is set > when sync with provider is true > "does not generate checkout url" | caller |
| 114 | .call > when provider customer id is set > when sync with provider is true > "does not enqueue a job to create the customer on the provider" | caller |

### services/payment_providers/gocardless/webhooks/mandate_cancelled_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 51 | #call > "clears the gocardless customer provider_mandate_id" | orchestration |
| 67 | #call > when payment method is not found > "does not destroy any payment method" | lookup |
| 71 | #call > when payment method is not found > "returns a successful result without payment method" | lookup |

### services/payment_providers/gocardless/webhooks/mandate_created_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 50 | #call > "updates the gocardless customer provider_mandate_id" | orchestration |
| 83 | #call > when gocardless customer is not found > "does not create a payment method" | lookup |
| 87 | #call > when gocardless customer is not found > "returns a successful result without payment method" | lookup |
