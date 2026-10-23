# Unported spec cases

Spec cases of the Stripe integration that are not ported to `providers/stripe` (or `libs/core/payment`), each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case.

Unported: 182 cases.

## Reasons

- **orchestration** (133): Record orchestration: loads or saves rows, changes payment, invoice or customer status, enqueues jobs, delivers outgoing webhooks or writes activity logs. This belongs to apps/server and libs/billing, which consume the gateway's typed results; nothing here talks to Stripe beyond what is already ported.
- **lookup** (24): Record lookup by the event's metadata (missing customer, other organization, already linked, not found). ParseWebhook returns the Tanvo and Stripe identifiers as payment.Refs; resolving them against the database is apps/server's job.
- **caller** (12): Caller-side decision: which method to charge or whether the payable is eligible is chosen by the caller before calling the gateway (libs/billing or apps/server).
- **model** (7): Model concern of the integration domain (name, code uniqueness, settings accessors, persisted provider payment methods); the gateway reads settings through parseConn, covered by TestSettingsDefaults and TestValidateConnection.
- **job** (3): Job wrapper that only calls a service; the job layer lives in apps/server.
- **log** (1): Asserts a log line. The behavior (a non-cancelable intent is a success) is ported from cancel_service_spec.rb:74; the adapter logs at Info.
- **dev_version** (1): Deviation: the webhook endpoint's API version is pinned to stripe-go's (stripe.APIVersion), so there is no per-call override (app/services/payment_providers/stripe/register_webhook_service.rb:8).
- **dev_spt** (1): Deviation: the shared payment token is used without the stripe_shared_payment_token feature flag (app/services/payment_providers/stripe/payments/create_service.rb:149); feature gating belongs to the caller.

## Ported instead of deviating

- `webhooks/payment_intent_payment_failed_service_spec.rb:89` and `webhooks/payment_intent_succeeded_service_spec.rb:187` (unknown payable type): ported in TestParseWebhook. ParseWebhook returns `payment.ErrUnknownPayable`, which matches `payment.ErrRejected`, and no event; the handler acknowledges the delivery with a logged warning and updates no record, as lago's NameError updates nothing (`webhooks/base_service.rb` payment_service_klass).
- `credit_notes/refunds/stripe_service_spec.rb:360` (invalid refund status): ported in TestRefundStatusOf, TestRefund and TestParseWebhook. RefundStatusOf returns `payment.ErrUnknownStatus`, neither rejected nor retryable; the caller keeps the refund status unchanged, as lago's validation failure does.

## Cases

### services/payment_providers/stripe/customers/create_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 17 | "creates a payment_provider_customer" | orchestration |
| 48 | when provider customer is persisted > when provider payment methods are not present > "does not update payment methods" | orchestration |
| 60 | when provider customer is not persisted > when provider payment methods are present > "saves payment methods" | orchestration |
| 70 | when provider customer is not persisted > when provider payment methods are not present > "saves default payment method" | orchestration |
| 87 | when no provider customer id and should create on service > "does not enqueue FetchDefaultPaymentMethodJob" | orchestration |
| 118 | when removing the provider customer id and should create on service > "does not enqueue FetchDefaultPaymentMethodJob" | orchestration |
| 142 | when provider customer id is set > when sync with provider is blank > when provider customer exists > when provider payment methods require setup > "generates checkout url" | orchestration |
| 155 | when provider customer id is set > when sync with provider is blank > when provider customer exists > when provider payment methods do not require setup > "does not generate checkout url" | orchestration |
| 164 | when provider customer id is set > when sync with provider is blank > when provider customer exists > "enqueues FetchDefaultPaymentMethodJob" | orchestration |
| 171 | when provider customer id is set > when sync with provider is blank > when provider customer does not exist > "does not generate checkout url" | orchestration |
| 175 | when provider customer id is set > when sync with provider is blank > when provider customer does not exist > "does not create customer" | orchestration |
| 185 | when provider customer id is set > when sync with provider is true > "does not generate checkout url" | orchestration |

### services/payment_providers/stripe/customers/fetch_default_payment_method_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 40 | when provider_customer has no provider_customer_id > "returns result without creating payment method" | orchestration |
| 51 | when no payment method is found on Stripe > "returns result without creating payment method" | orchestration |
| 134 | when payment method is found on Stripe > when payment method already exists in Lago > "returns the existing payment method without creating a duplicate" | orchestration |

### services/payment_providers/stripe/expire_payment_intents_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 19 | "expires active payment intents of the provider's invoices" | orchestration |
| 23 | "is successful" | orchestration |
| 38 | "does not expire it" | orchestration |

### services/payment_providers/stripe/handle_event_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 49 | "routes the event to an other service" | orchestration |

### services/payment_providers/stripe/handle_incoming_webhook_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 12 | "checks the webhook" | orchestration |

### services/payment_providers/stripe/register_webhook_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 107 | when overriding version > "registers a webhook on stripe" | dev_version |

### services/payment_providers/stripe/payments/authorize_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 79 | with provider_method_id > "cancels the payment intent later" | orchestration |
| 107 | #find_provider_method_id > with payment_method > "uses payment_method#provider_method_id" | caller |
| 115 | #find_provider_method_id > without payment_method > "uses provider_customer#payment_method_id" | caller |

### services/payment_providers/stripe/payments/cancel_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 78 | "logs the non-cancelable state" | log |

### services/payment_providers/stripe/payments/create_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 127 | when the invoice has already been paid > "raises AlreadyPaidError and does not create a payment intent" | caller |
| 220 | when customer has a default shared payment token > when no other payment method is attached > when the stripe_shared_payment_token feature flag is disabled > "ignores the shared payment token even when no other payment method is attached" | dev_spt |

### services/payment_providers/stripe/payments/update_reference_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 71 | "skips the Stripe call" | caller |
| 77 | "returns success" | caller |

### services/payment_providers/stripe/webhooks/charge_dispute_closed_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 38 | when payable is an invoice > when dispute is lost > when invoice is draft > "does not updates invoice payment dispute lost" | orchestration |
| 45 | when payable is an invoice > when dispute is lost > when invoice is draft > "does not deliver webhook" | orchestration |
| 60 | when payable is an invoice > when dispute is lost > when invoice is finalized > "delivers a webhook" | orchestration |
| 86 | when payable is an invoice > when dispute is won > when invoice is draft > "does not updates invoice payment dispute lost" | orchestration |
| 93 | when payable is an invoice > when dispute is won > when invoice is draft > "does not deliver webhook" | orchestration |
| 108 | when payable is an invoice > when dispute is won > when invoice is finalized > "does not deliver webhook" | orchestration |
| 131 | when payable is a payment request > when dispute is lost > "flags all the invoices of the PaymentRequests" | orchestration |
| 156 | when payable is a payment request > when dispute is won > "does not call LoseDisputeService" | orchestration |

### services/payment_providers/stripe/webhooks/customer_cash_balance_transaction_created_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 72 | when several invoices had their intent canceled > "targets the oldest one" | orchestration |
| 99 | when only a newer invoice has an awaiting-funds payment open > "still opens an intent on the older invoice" | orchestration |
| 111 | when a new intent already replaced the failed one > "does not open another intent" | orchestration |
| 127 | when the invoice never had a payment attempt > "does not open an intent" | orchestration |
| 138 | when the customer does not use customer_balance > "does not open an intent" | orchestration |
| 183 | when no unpaid invoice matches the funded currency > "does not open an intent" | orchestration |
| 196 | when the invoice is already paid > "does not open an intent" | orchestration |
| 211 | when the stripe customer is unknown > "does not open an intent" | orchestration |

### services/payment_providers/stripe/webhooks/customer_updated_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 54 | when customer is not found > "returns an empty result" | lookup |
| 66 | when customer is not found > when stripe customer is deleted > "returns an empty result" | lookup |
| 84 | when customer is not found > when customer in metadata is not found > "returns an empty response" | lookup |
| 103 | when customer is not found > when customer in metadata exists > when customer is linked to another stripe customer > "returns an empty result" | lookup |
| 114 | when customer is not found > when customer in metadata exists > when customer is not linked to another stripe customer > "returns a not found error" | lookup |

### services/payment_providers/stripe/webhooks/payment_intent_payment_failed_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 64 | "routes the event to an other service" | orchestration |

### services/payment_providers/stripe/webhooks/payment_intent_succeeded_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 45 | "does not enqueue a payment receipt job" | orchestration |
| 58 | when issue_receipts_enabled is true", :premium > "enqueues a payment receipt job" | orchestration |
| 84 | when issue_receipts_enabled is true", :premium > "enqueues a payment receipt job" | orchestration |
| 160 | when payment belongs to a payment_request from another organization > "returns an empty result" | lookup |
| 166 | when payment belongs to a payment_request from another organization > "does not update the payment_status of the payment" | lookup |
| 171 | when payment belongs to a payment_request from another organization > "does not enqueue a payment receipt job" | lookup |

### services/payment_providers/stripe/webhooks/setup_intent_succeeded_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 34 | when stripe customer id is nil > "returns an empty result" | lookup |
| 54 | when payment method has no customer > "returns an empty result" | lookup |
| 89 | when provider customer id is set > "create a customer payment_method" | orchestration |
| 93 | when provider customer id is set > "create a customer payment_method with details" | orchestration |
| 118 | when provider customer id is set > when payment method type is not card > "creates a payment method with nil card details" | orchestration |
| 148 | when stripe customer is not found > when metadata is empty > "returns an empty result" | lookup |
| 160 | when stripe customer is not found > when customer in metadata exists in another organization > "returns an empty result" | lookup |
| 172 | when stripe customer is not found > when customer in metadata exists in this org > when is linked to another stripe customer > "returns an empty result" | lookup |
| 183 | when stripe customer is not found > when customer in metadata exists in this org > when is not linked to another stripe customer > "returns a not found error" | lookup |

### services/payment_provider_customers/stripe_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 22 | when customer is deleted > "returns a deleted_customer failure" | orchestration |
| 53 | when stripe customer is created and has customer_balance payment method > "enqueues StripeSyncFundingInstructionsJob" | orchestration |
| 73 | "creates the stripe customer" | orchestration |
| 84 | "delivers a success webhook" | orchestration |
| 101 | when customer already have a stripe customer id > "does not call stripe API" | orchestration |
| 117 | when no payment provider is connected > "does not call stripe API" | orchestration |
| 125 | when no payment provider is connected > "returns success" | orchestration |
| 148 | when payment provider has incorrect API key > "delivers an error webhook" | orchestration |
| 227 | when stripe customer provider_customer_id is present > when stripe raises an error > when stripe raises an invalid request error > "delivers an error webhook" | orchestration |
| 253 | when stripe customer provider_customer_id is present > when stripe raises an error > when stripe raises a permission error > "delivers an error webhook" | orchestration |
| 277 | when stripe customer provider_customer_id is present > when stripe raises an error > when stripe raises an authentication error > "delivers an error webhook" | orchestration |
| 303 | when stripe customer provider_customer_id is present > when no stripe error is raised > when stripe payment provider is present > "returns a successful result" | orchestration |
| 309 | when stripe customer provider_customer_id is present > when no stripe error is raised > when stripe payment provider is present > "does not deliver an error webhook" | orchestration |
| 317 | when stripe customer provider_customer_id is present > when no stripe error is raised > when stripe payment provider is not present > "does not call stripe API" | orchestration |
| 323 | when stripe customer provider_customer_id is present > when no stripe error is raised > when stripe payment provider is not present > "returns a successful result" | orchestration |
| 329 | when stripe customer provider_customer_id is present > when no stripe error is raised > when stripe payment provider is not present > "does not deliver an error webhook" | orchestration |
| 344 | when updating a stripe customer with customer_balance method > "enqueues StripeSyncFundingInstructionsJob" | orchestration |
| 360 | when stripe customer provider_customer_id is not present > when stripe payment provider is present > "does not call stripe API" | orchestration |
| 366 | when stripe customer provider_customer_id is not present > when stripe payment provider is present > "returns a successful result" | orchestration |
| 372 | when stripe customer provider_customer_id is not present > when stripe payment provider is present > "does not deliver an error webhook" | orchestration |
| 380 | when stripe customer provider_customer_id is not present > when stripe payment provider is not present > "does not call stripe API" | orchestration |
| 386 | when stripe customer provider_customer_id is not present > when stripe payment provider is not present > "returns a successful result" | orchestration |
| 392 | when stripe customer provider_customer_id is not present > when stripe payment provider is not present > "does not deliver an error webhook" | orchestration |
| 411 | "removes the customer payment method" | orchestration |
| 422 | when customer payment method is not the deleted one > "does not remove the customer payment method" | orchestration |
| 440 | when the customer has a payment method > "discards the payment method" | orchestration |
| 453 | when the customer has a payment method > when payment method does not exist > "does not raise an error" | orchestration |
| 466 | when customer is not found > "returns an empty result" | lookup |
| 477 | when customer is not found > when customer in metadata is not found > "returns an empty response" | lookup |
| 492 | when customer is not found > when customer in metadata exists > "returns a not found error" | lookup |
| 549 | without any customer > "does not deliver a webhook" | orchestration |
| 560 | when customer has no payment method to be setup > "does not deliver a webhook" | orchestration |
| 595 | when stripe raises an authentication error > "delivers an error webhook" | orchestration |

### services/payment_provider_customers/stripe/check_payment_method_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 69 | when payment method is not found on stripe > when a payment method exists > "returns a failed result and discards payment method" | orchestration |
| 84 | when payment method is not found on stripe > when a payment method exists > when customer is deleted > "returns a failed result and discards payment method" | orchestration |

### services/payment_provider_customers/stripe/sync_funding_instructions_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 26 | when customer is not eligible > "does not fetch Stripe funding instructions" | caller |

### services/payment_provider_customers/stripe/update_payment_method_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 15 | "updates the customer payment method" | orchestration |
| 23 | without payment_method > "creates a new one with provider_method_id" | orchestration |
| 40 | without payment_method > when the default flip fails > "keeps the already saved settings, to be reconciled by the retry" | orchestration |
| 51 | without payment_method > when the advisory lock cannot be acquired > "propagates the error so the job can retry" | orchestration |
| 62 | with existing payment_method > "set as default" | orchestration |
| 74 | when payment_method_id is nil > "does not create a PaymentMethod" | orchestration |
| 96 | with pending invoices > "enqueues jobs to reprocess the pending payment" | orchestration |
| 107 | with pending invoices > when invoices are not finalized > "does not enqueue jobs to reprocess pending payment" | orchestration |
| 118 | with pending invoices > when invoices are not ready for payment processing > "does not enqueue jobs to reprocess pending payment" | orchestration |
| 130 | when customer is deleted > "Fails with deleted_customer error" | orchestration |

### models/payment_providers/stripe_provider_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 11 | { is_expected.to validate_presence_of(:name) } | model |
| 14 | "validates uniqueness of the code" | model |
| 27 | "assigns and retrieve a setting" | model |
| 34 | "assigns and retrieve a setting" | model |

### models/payment_provider_customers/stripe_customer_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 109 | "assigns and retrieve a payment method id" | model |
| 126 | "returns provider payment methods" | model |
| 140 | { expect(stripe_customer).to be_require_provider_payment_id } | model |

### services/credit_notes/refunds/stripe_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 74 | "call SegmentTrackJob" | orchestration |
| 135 | when the provider was disconnected and reconnected > "attaches the refund to the provider customer that holds the payment" | caller |
| 170 | with an error on stripe > "produces an activity log" | orchestration |
| 211 | when credit note does not have a refund amount > "does not create a refund" | caller |
| 226 | when invoice does not have a payment > "does not create a refund" | caller |
| 241 | when payment provider customer was discarded > "creates a stripe refund and a refund" | caller |
| 265 | when dispute was lost > "does not create a refund" | caller |
| 299 | "calls SegmentTrackJob" | orchestration |
| 319 | when refund is not found > "returns an empty result" | lookup |
| 344 | when refund is not found > with invoice id in metadata > when invoice belongs to lago > "returns a not found failure" | lookup |
| 402 | when status is failed > "produces an activity log" | orchestration |

### services/invoices/payments/stripe_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 235 | "updates the payment and invoice status" | orchestration |
| 253 | "enqueues a SendWebhookJob for payment.succeeded" | orchestration |
| 264 | "syncs payment" | orchestration |
| 276 | when the customer has no accounting integration > "does not enqueue an accounting payment sync job" | orchestration |
| 298 | when status is failed > "updates the payment and invoice status" | orchestration |
| 321 | when status is failed > when the customer has an accounting integration syncing payments > "does not enqueue an accounting payment sync job" | orchestration |
| 346 | when status is failed > when the off-session charge was rejected for authentication and 3DS is supported > "updates the payment status but not the invoice status" | orchestration |
| 374 | when status is failed > when the off-session charge was rejected for authentication and 3DS is not supported > "updates the invoice status because no retry will follow" | orchestration |
| 409 | when status is failed > when the off-session charge was rejected for authentication and 3DS is not supported > when the invoice activates a payment-gated subscription > "updates the payment status but not the invoice status" | orchestration |
| 429 | when status is failed > when there is another payment in requires_action state for the invoice > "updates the payment status but not the invoice status" | lookup |
| 459 | when invoice is already payment_succeeded > "does not update the status of invoice and payment" | orchestration |
| 473 | with invalid status > "does not update the status of invoice and payment" | orchestration |
| 505 | when payment is not found and it is one time payment > "creates a payment and updates invoice payment status" | orchestration |
| 524 | when payment is not found and it is one time payment > when amount_cents kwarg is provided > "records the Payment with the provider-reported amount, not the invoice due amount" | orchestration |
| 538 | when payment is not found and it is one time payment > when amount_cents kwarg is not provided > "falls back to the invoice's total due amount" | orchestration |
| 560 | when payment is not found and it is one time payment > when invoice is not found > "raises a not found failure" | orchestration |
| 578 | when payment is not found > "returns an empty result" | orchestration |
| 600 | when payment is not found > with invoice id in metadata > "returns an empty result" | orchestration |
| 627 | when payment is not found > with invoice id in metadata > when the invoice is found for organization > "creates the missing payment and updates invoice status" | orchestration |
| 662 | when payment's payable belongs to another organization > "does not update the payment status" | lookup |

### services/payment_requests/payments/stripe_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 221 | "updates the payment, payment_request and invoice payment_status" | orchestration |
| 238 | "does not send payment requested email" | orchestration |
| 264 | when the payment request belongs to a dunning campaign > "resets the customer dunning campaign counters for the payment request currency" | orchestration |
| 276 | when the payment request belongs to a dunning campaign > when status is failed > "doest not reset the customer dunning campaign counters" | orchestration |
| 289 | when status is failed > "updates the payment, payment_request and invoice status" | orchestration |
| 307 | when status is failed > "sends a payment requested email" | orchestration |
| 345 | when invoices have offset amounts from credit notes > "updates invoices considering offset amounts" | orchestration |
| 360 | when invoices have offset amounts from credit notes > "marks invoices as paid even though paid amount doesn't equal total amount" | orchestration |
| 401 | when invoice is fully offset by credit note > "does not increase paid amount for fully offset invoice" | orchestration |
| 439 | when invoice is partially paid and has offset amount > "updates paid amount correctly" | orchestration |
| 456 | when payment_request and invoice is already payment_succeeded > "does not update the status of invoice, payment_request and payment" | orchestration |
| 466 | when payment_request and invoice is already payment_succeeded > "does not send payment requested email" | orchestration |
| 480 | when a failed webhook arrives after the invoice was already paid through another path > "leaves already-succeeded invoices untouched" | lookup |
| 494 | with invalid status > "does not update the payment_status of payment_request, invoice and payment" | orchestration |
| 502 | with invalid status > "returns an error" | orchestration |
| 509 | with invalid status > "does not send payment requested email" | orchestration |
| 536 | when payment is not found and it is one time payment > "creates a payment and updates payment request and invoice payment status" | orchestration |
| 564 | when payment is not found and it is one time payment > when payment request is not found > "raises a not found failure" | orchestration |
| 601 | when the payment is found by stripe payment id > "updates the payment status and related entities" | orchestration |
| 618 | when the payment is found by stripe payment id > "does not create a new payment" | orchestration |
| 627 | when payment is not found > "returns an empty result" | orchestration |
| 645 | when payment is not found > with payment request id in metadata > "returns an empty result" | orchestration |
| 668 | when payment is not found > with payment request id in metadata > when the payment request is found for organization > "creates the missing payment and updates payment_request status" | orchestration |
| 715 | when payment is not found > with payment request id in metadata > when the payment request is found for organization > when a concurrent writer has already persisted the payment > "returns a success result with the persisted payment" | orchestration |
| 734 | when payment belongs to a payment_request from another company > "returns an empty result" | lookup |
| 739 | when payment belongs to a payment_request from another company > "does not update the payment_status of payment_request, invoice and payment" | lookup |
| 770 | when payment's payable already has a successful payment > "returns an empty result" | orchestration |

### services/payments/set_payment_method_data_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 51 | with Stripe > when the payment method id is already set > "does not call stripe" | caller |
| 66 | with any other provider > | orchestration |

### jobs/payment_provider_customers/stripe_checkout_url_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 10 | "calls generate_checkout_url method" | job |

### jobs/payment_provider_customers/stripe_create_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | "calls the stripe create service" | job |

### jobs/payment_provider_customers/stripe_sync_funding_instructions_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 11 | "calls the funding instructions sync service" | job |
