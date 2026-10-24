# Unported spec cases

Spec cases of the Cashfree integration that are not ported to `providers/cashfree`, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case.

Unported: 24 cases.

## Reasons

- **orchestration** (21): Record orchestration: loads or saves rows, changes payment, invoice, payment request or customer records, resets dunning counters, sends emails, enqueues jobs, delivers outgoing webhooks or writes security logs. This belongs to apps/server and libs/billing, which consume the gateway's typed results (payment.PaymentURL, payment.PaymentEvent with its amount, status, OneTime flag and payment.Refs); nothing here talks to Cashfree beyond what is already ported. The status mapping these cases rely on is ported in TestStatusOf.
- **caller** (1): Caller-side decision: Cashfree customers have no provider customer ID (lago CashfreeCustomer#require_provider_payment_id? is false), so libs/billing does not create one before charging; the adapter's CreateCustomer is payment.ErrUnsupported (TestCustomers).
- **model** (2): Model concern of the integration domain (connection code changes propagated to customers, updating an existing connection); the gateway reads its settings through parseConn, covered by TestValidateConnection.

## Ported with a deviation

- `services/invoices/payments/cashfree_service_spec.rb:239`, `:245` and `services/payment_requests/payments/cashfree_service_spec.rb:329` (payment link parameters): ported in TestCreatePaymentURL. The amount is the exact decimal in the currency's exponent instead of `cents / 100.to_f`; the link ID is a random 26-character ID instead of `<uuid>.<payment attempts>`; a missing email or name is left out instead of sent as null; a payment request's issuing date note is a date instead of its creation time, and is sent only when set.
- `services/invoices/payments/cashfree_service_spec.rb:295` (refused link): ported in TestCreatePaymentURLFailures. The error code is the HTTP status as in lago; the message is Cashfree's message and error code instead of the raw body.
- `services/payment_providers/cashfree/webhooks/payment_link_event_service_spec.rb:21`, `:48` (PAID links): ported in TestParseWebhook. The payment's provider ID is the Cashfree link ID instead of the invoice or payment request ID, which is reported in payment.Refs.
- `services/payment_providers/cashfree/payments/create_service_spec.rb:44` (automatic payment): lago records a pending payment without calling Cashfree, waiting for a webhook that never names it; CreatePayment is payment.ErrUnsupported here (TestUnsupportedCharges) and customers pay through payment links.

## Cases

### models/payment_provider_customers/cashfree_customer_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 11 | #require_provider_payment_id? > (unnamed) | caller |

### services/invoices/payments/cashfree_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 55 | .call(:update_payment_status) > "updates the payment and invoice payment_status" | orchestration |
| 73 | .call(:update_payment_status) > "enqueues a SendWebhookJob for payment.succeeded" | orchestration |
| 93 | .call(:update_payment_status) > when status is failed > "updates the payment and invoice status" | orchestration |
| 122 | .call(:update_payment_status) > when invoice is already payment_succeeded > "does not update the status of invoice and payment" | orchestration |
| 144 | .call(:update_payment_status) > with invalid status > "does not update the payment_status of invoice" | orchestration |
| 175 | .call(:update_payment_status) > when payment is not found and it is one time payment > "creates a payment and updates invoice payment status" | orchestration |

### services/payment_providers/cashfree_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 32 | .create_or_update > it_behaves_like "produces a security log", "integration.created" | orchestration |
| 62 | .create_or_update > when code was changed > "updates payment provider codes of all customers" | model |
| 83 | .create_or_update > when organization already have a cashfree provider > "updates the existing provider" | model |
| 103 | .create_or_update > when organization already have a cashfree provider > it_behaves_like "produces a security log", "integration.updated" | orchestration |

### services/payment_requests/payments/cashfree_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 79 | .call(:update_payment_status) > "updates the payment and invoice payment_status" | orchestration |
| 94 | .call(:update_payment_status) > "does not send payment requested email" | orchestration |
| 120 | .call(:update_payment_status) > when the payment request belongs to a dunning campaign > "resets the customer dunning campaign counters for the payment request currency" | orchestration |
| 138 | .call(:update_payment_status) > when the payment request belongs to a dunning campaign > when status is failed > "doest not reset the customer dunning campaign counters" | orchestration |
| 157 | .call(:update_payment_status) > when status is failed > "updates the payment, payment_request and invoices status" | orchestration |
| 182 | .call(:update_payment_status) > when status is failed > "sends a payment requested email" | orchestration |
| 204 | .call(:update_payment_status) > when payment_request and invoices is already payment_succeeded > "does not update the status of invoices, payment_request and payment" | orchestration |
| 214 | .call(:update_payment_status) > when payment_request and invoices is already payment_succeeded > "does not send payment requested email" | orchestration |
| 234 | .call(:update_payment_status) > when a failed webhook arrives after the invoices were already paid through another path > "leaves already-succeeded invoices untouched" | orchestration |
| 254 | .call(:update_payment_status) > with invalid status > "does not update the payment_status of payment_request, invoices and payment" | orchestration |
| 262 | .call(:update_payment_status) > with invalid status > "returns an error" | orchestration |
| 269 | .call(:update_payment_status) > with invalid status > "does not send payment requested email" | orchestration |
| 294 | .call(:update_payment_status) > when payment is not found and it is one time payment > "creates a payment and updates invoice payment status" | orchestration |
