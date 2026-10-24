# Unported spec cases

Spec cases of the Flutterwave integration that are not ported to `providers/flutterwave`, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case.

Unported: 34 cases.

## Reasons

- **orchestration** (22): Record orchestration: loads or saves rows, changes payment, invoice, payment request or customer records, counts payment attempts, sums paid amounts, resets dunning counters, sends emails, enqueues jobs, delivers outgoing webhooks or writes security logs. This belongs to apps/server and libs/billing, which consume the gateway's typed results (payment.PaymentURL, payment.PaymentEvent with its verified amount, status, OneTime flag and payment.Refs, typed provider errors); nothing here talks to Flutterwave beyond what is already ported. The status mapping these cases rely on is ported in TestStatusOf.
- **model** (8): Model concern of the integration domain (connection name and code uniqueness, code changes propagated to customers, partial updates, finding a connection by code, Ruby data structures and delegations); the gateway reads its settings through parseConn, covered by TestValidateConnection and TestRegisterWebhook.
- **lookup** (1): Record lookup (connection not found). apps/server resolves the connection from the webhook URL before calling VerifyWebhook.
- **job** (2): Sidekiq queue selection of a job wrapper; the job layer lives in apps/server.
- **errors** (1): Lago's event handler logs and swallows every processing error. ParseWebhook returns typed errors instead (payment.ErrUnknownPayable, payment.ErrInvalidPayload, retryable verification failures, TestParseWebhookErrors), and apps/server decides whether to acknowledge the delivery.

## Ported with a deviation

- `services/invoices/payments/flutterwave_service_spec.rb:229`, `:284` and `services/payment_requests/payments/flutterwave_service_spec.rb:49`, `:284` (page parameters): ported in TestCreatePaymentURL and TestAmountOf. The amount is the exact decimal in the currency's exponent instead of a float; a payment request's reference is `tanvo_payment_request_<id>`.
- `services/payment_requests/payments/flutterwave_service_spec.rb:42`, `:295` (payment request pages): ported in TestCreatePaymentURL. Lago posts a payment request's page to the API root instead of `/payments`, and names the request by `lago_payment_request_id`, which its webhook does not read, so its payment request charges never find their payable. Both payables are posted to `/payments` here, and a payment request's meta also carries `tanvo_payable_id` and `tanvo_payable_type`, the keys the webhook reads; a `tanvo_payment_request_` reference also names the request.
- `services/invoices/payments/flutterwave_service_spec.rb:259` (refused page): ported in TestCreatePaymentURLFailures. The error code is the HTTP status as in lago; the message is Flutterwave's message instead of the raw body.
- `services/payment_providers/flutterwave/webhooks/charge_completed_service_spec.rb:104`, `:343` (verified charges): ported in TestParseWebhook. The payment's provider ID is Flutterwave's transaction ID instead of the invoice or payment request ID, which is reported in payment.Refs.
- `services/payment_providers/flutterwave/webhooks/charge_completed_service_spec.rb:281` (verification HTTP error): ported in TestParseWebhookErrors. Lago logs it and drops the event; a retryable failure (network, 5xx, 429) is returned here so the delivery is retried. A verification Flutterwave refuses (4xx) yields no event, as in lago (TestParseWebhookNoEvent).
- `services/payment_providers/flutterwave/webhooks/charge_completed_service_spec.rb:521` (meta missing): ported in TestParseWebhookNoEvent. A reference that is not a Tanvo ID names no payable, so nothing is verified; lago verifies first and then finds no record.

## Cases

### jobs/payment_providers/flutterwave/handle_event_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 29 | queue configuration > when SIDEKIQ_PAYMENTS is true > "uses the payments queue" | job |
| 39 | queue configuration > when SIDEKIQ_PAYMENTS is false or not set > "uses the providers queue" | job |

### models/payment_providers/flutterwave_provider_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 10 | validations > (unnamed) | model |
| 16 | validations > "validates uniqueness of the code" | model |
| 85 | FlutterwavePayment > "defines a data structure for payments" | model |

### services/invoices/payments/flutterwave_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 31 | .call(:update_payment_status) > when creating a new payment > "creates a new payment and updates invoice status" | orchestration |
| 47 | .call(:update_payment_status) > when creating a new payment > "increments payment attempts on the invoice" | orchestration |
| 52 | .call(:update_payment_status) > when creating a new payment > "sets correct payment details" | orchestration |
| 67 | .call(:update_payment_status) > when creating a new payment > "enqueues a SendWebhookJob for payment.succeeded" | orchestration |
| 97 | .call(:update_payment_status) > when updating existing payment > "updates the existing payment status" | orchestration |
| 110 | .call(:update_payment_status) > when updating existing payment > "updates invoice payment status" | orchestration |
| 134 | .call(:update_payment_status) > when payment is not found > "returns not found failure" | orchestration |
| 166 | .call(:update_payment_status) > when payment already succeeded > "returns early without processing" | orchestration |
| 190 | .call(:update_payment_status) > when payment status calculation includes total paid amount > "calculates total paid amount correctly" | orchestration |
| 297 | private methods > #increment_payment_attempts > "increments payment attempts on invoice" | orchestration |
| 311 | private methods > #update_invoice_payment_status > "calls invoice update service with correct parameters" | orchestration |
| 323 | private methods > #update_invoice_payment_status > when payment status is not succeeded > "sets ready_for_payment_processing to true" | orchestration |

### services/payment_provider_customers/flutterwave_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 50 | private methods > #customer > "delegates to flutterwave_customer" | model |

### services/payment_providers/flutterwave/handle_event_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 42 | #call > when event is charge.completed > "returns success even if the service raises an error" | errors |

### services/payment_providers/flutterwave/handle_incoming_webhook_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 82 | #call > when payment provider is not found > "returns service failure" | lookup |

### services/payment_providers/flutterwave_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 36 | #create_or_update > when creating a new provider > it_behaves_like "produces a security log", "integration.created" | orchestration |
| 55 | #create_or_update > when updating an existing provider > "updates the existing provider" | model |
| 65 | #create_or_update > when updating an existing provider > it_behaves_like "produces a security log", "integration.updated" | orchestration |
| 86 | #create_or_update > when updating an existing provider > when code is updated > "updates the provider and associated customer codes" | model |
| 111 | #create_or_update > when partial update with only specific fields > "updates only the provided fields" | model |
| 156 | #create_or_update > when finding existing provider by code > "finds and updates the existing provider by code" | model |

### services/payment_requests/payments/flutterwave_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 84 | .call(:generate_payment_url) > when HTTP client raises an error > "sends webhook notification about payment failure" | orchestration |
| 111 | .call(:update_payment_status) > when creating a new payment > "creates a new payment and updates payment request status" | orchestration |
| 127 | .call(:update_payment_status) > when creating a new payment > "increments payment attempts on the payment request" | orchestration |
| 155 | .call(:update_payment_status) > when updating existing payment > "updates the existing payment status" | orchestration |
| 176 | .call(:update_payment_status) > when payment is not found > "returns not found failure" | orchestration |
| 211 | .call(:update_payment_status) > when payment already succeeded > "returns early without processing" | orchestration |
| 243 | .call(:update_payment_status) > when payment fails > "sends payment failure email" | orchestration |
| 260 | .call(:update_payment_status) > when a failed webhook arrives after the invoice was already paid through another path > "leaves the already-succeeded invoice untouched" | orchestration |
