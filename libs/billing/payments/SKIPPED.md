# Unported spec cases

Spec cases of the payment connections, billing object connections and payment methods that are not ported, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case of the files in `testdata/specs.json` (checked by `go run ./tools/speccov`).

Cases: 293. Ported: 237. Unported: 56.

## Reasons

- **codes** (8): Connection codes: a connection is named by its integration's code (unique per organization), so there is no per-connection code to set, derive, rename or keep unique. The rule that does exist, one live connection per vendor and customer (base_customer.rb:32), is checked by UpsertConnection.
- **provider-agnostic** (8): Payment requests are charged through the provider-neutral payment.Gateway port; the Adyen and GoCardless variants differ only in the adapter, which is tested in providers/adyen and providers/gocardless, while the shared outcomes are ported against the Stripe cases.
- **customer-provider** (6): The customer's payment_provider and payment_provider_code fields: Tanvo keeps no denormalized copy of the customer's provider. The connection in effect is the only one, else the default one (payment.DefaultProviderCustomer), which the is_default cases cover.
- **lock** (4): The customer's payment method lock (port.Locker, a transaction-scoped advisory lock) blocks until it is held instead of failing after a timeout, so there is no lock acquisition failure; a failure to lock is a store error that fails the transaction.
- **paid-race** (4): A payment request settled by another payment path between the preparation and the charge: requestAlreadyPaid (libs/billing/payments/request_charge.go) rereads the request in its own transaction and deletes only a pending payment with no provider reference that is not succeeded, but the interleaving of two transactions is not reproducible in a service test.
- **tenant** (4): Every repository query filters by organization_id, and a webhook is resolved to its connection's organization first, so a payment whose payable belongs to another organization is never loaded: it is an unknown payment, acknowledged without change, as the ported stripe_service_spec.rb:578 case checks.
- **async** (3): The job enqueue of call_async: Tanvo enqueues kernel.JobCreateRequestPayment for every provider through enqueueRequestCollection, and the vendor is chosen when the job runs, so there is no per-vendor job to assert.
- **batch** (2): No batch replacement of a customer's connections: they are created or updated one at a time (UpsertCustomerConnection) and removed explicitly (DeleteCustomerConnection), so there is no array to omit, leave nil or match by ID.
- **details-delete** (2): Deleting detail keys: the only caller inserts (moneyhash_service.rb:89-94), and the details are typed fields (payment.MethodDetails), not an open hash.
- **multi** (2): Kept deviation: a customer has several payment connections, one per vendor, with one default, as CreateConnectionService allows (create_connection_service_spec.rb:135-152); the batch path of v1.55.0 still caps the array at one, which is the transitional limit of the same feature, not a rule.
- **typed** (2): The connections field is a typed map of ConnectionChoice messages in the API (libs/proto, protovalidate), so a choice, or the field itself, cannot be anything but an object.
- **api-version** (1): The Stripe adapter pins its API version (providers/stripe); there is no per-call version override to register a webhook with.
- **customer** (1): A payment method is created for a provider customer, which always belongs to a customer; there is no call without one.
- **lock-mock** (1): A mock assertion that Customers::LockService is called. setDefault takes the customer's payment method lock (libs/billing/payments/methods.go), and the lock's serialization is the store's, covered by the porttest locks case.
- **manual** (1): The reserved lago_manual connection: manual collection is a payment method type in Tanvo, not a pseudo-connection (payment.Method, parity §1.33).
- **query-count** (1): An ActiveRecord query-count assertion on the routing of a preloaded collection. Tanvo routes one billing object per call (kernel.ConnectionRouting reads its choices and its customer's connections), so there is no collection preload to count.
- **race** (1): Two interleaved transactions are not reproducible in a service test. The live (provider customer, provider method) key of payment_methods (libs/store/schema/15_payment.sql) makes the losing insert a conflict, after which findOrCreateMethod reads the winner back (libs/billing/payments/events.go); the key is covered by the porttest payment methods case.
- **rails** (1): Rails and ActiveRecord mechanics with no Go counterpart: associations and preloading. The domain types and their repositories are tested in libs/core and the store's porttest suites.
- **receipts** (1): Payment receipts are always issued (parity: issue_receipts is a premium integration flag in v1.55.0 and Tanvo has no premium gates), so there is no organization without receipts.
- **status-typed** (1): Payment statuses are typed: each adapter maps its vendor's statuses to payment.State, and a status it does not know is acknowledged without change (payment.ErrUnknownStatus, libs/billing/payments/events.go), so no free-form status string reaches the update.
- **type-ref** (1): A connection names one integration, by ID or by code, so a vendor type alone never has to be resolved to one of several integrations.
- **upsert** (1): An update names the connection by its integration; for a customer without one it creates it (create_connection_service_spec.rb:63-90), so there is no update of a missing connection.

## Cases

### models/billing_object_connection_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 23 | it do | rails |

### models/concerns/connection_resolvable_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 39 | "does not grow with the size of the collection" | query-count |

### services/billing_object_connections/validate_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 110 | when the choice is not an object > "returns an invalid_connection_choice error" | typed |
| 119 | when connections is not an object > "returns an invalid_connections error" | typed |

### services/invoices/payments/stripe_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 473 | with invalid status > "does not update the status of invoice and payment" | status-typed |
| 662 | when payment's payable belongs to another organization > "does not update the payment status" | tenant |

### services/payment_methods/create_from_provider_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 21 | without customer > "fails" | customer |
| 35 | when the advisory lock cannot be acquired > "raises a lock acquisition failure" | lock |

### services/payment_methods/find_or_create_from_provider_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 93 | when a concurrent process creates the same PaymentMethod > "rescues RecordNotUnique and returns the existing record" | race |
| 145 | with set_as_default: true > when the advisory lock cannot be acquired > "raises a lock acquisition failure" | lock |

### services/payment_methods/set_as_default_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 25 | when payment method exists > "serializes under a per-customer advisory lock" | lock-mock |
| 51 | when payment method exists > when the advisory lock cannot be acquired > "returns a lock acquisition failure that wraps the original exception" | lock |
| 60 | when payment method exists > when the advisory lock cannot be acquired > "does not change the default payment method" | lock |

### services/payment_methods/update_details_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 77 | when deletions > when the keys does not exist > "does nothing" | details-delete |
| 89 | when deletions > with existing keys > "remove the keys" | details-delete |

### services/payment_provider_customers/create_connection_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 93 | with a valid payment provider > "sets the customer payment provider settings" | customer-provider |
| 106 | when no code is given > "derives the code from the payment provider" | codes |
| 128 | when the organization has several providers of the same type > when the payment provider code is not given > "cannot resolve which provider to connect to" | type-ref |
| 149 | when the customer already has a connection > "does not change the customer payment provider settings" | customer-provider |
| 160 | when the customer already has payment provider settings > "does not overwrite them" | customer-provider |
| 167 | when the customer already has payment provider settings > "still creates the connection as the default one" | customer-provider |
| 178 | when the code is already used by another connection of the customer > "returns a validation failure" | codes |
| 183 | when the code is already used by another connection of the customer > "does not create the payment provider customer" | codes |

### services/payment_provider_customers/create_or_update_batch_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 19 | when the array is nil > "does not change the connections" | batch |
| 27 | when the array carries more than one connection > "fails as only one connection is allowed for now" | multi |
| 34 | when the array carries more than one connection > "does not create any connection" | multi |
| 42 | when creating the manual connection > "creates the reserved manual connection" | manual |
| 56 | when a connection is omitted from the array > "discards the omitted connection" | batch |
| 68 | when an existing connection is referenced by id > "updates it instead of discarding it" | codes |

### services/payment_provider_customers/destroy_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 67 | with a payment provider customer > when the connection is the customer's active provider > "clears the customer's payment provider and code" | customer-provider |
| 80 | with a payment provider customer > when the connection is not the customer's active provider > "leaves the customer's payment provider untouched" | customer-provider |

### services/payment_provider_customers/update_connection_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 19 | when payment provider customer is not found > "returns an error" | upsert |
| 33 | with a non-stripe connection > when updating the code > "updates the code" | codes |
| 38 | with a non-stripe connection > when updating the code > "returns the payment provider customer" | codes |
| 55 | with a stripe connection > when only the code is updated > "does not run the provider customer upsert" | codes |
| 64 | with a stripe connection > when updating both code and provider payment methods > "updates the code" | codes |

### services/payment_providers/stripe/register_webhook_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 107 | when overriding version > "registers a webhook on stripe" | api-version |

### services/payment_providers/stripe/webhooks/payment_intent_succeeded_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 45 | when payment intent event (api_version: #{version}) > "does not enqueue a payment receipt job" | receipts |
| 160 | when payment intent event for a payment request > when payment belongs to a payment_request from another organization > "returns an empty result" | tenant |
| 166 | when payment intent event for a payment request > when payment belongs to a payment_request from another organization > "does not update the payment_status of the payment" | tenant |
| 171 | when payment intent event for a payment request > when payment belongs to a payment_request from another organization > "does not enqueue a payment receipt job" | tenant |

### services/payment_requests/payments/create_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 98 | with adyen payment provider > "creates a payment and calls the adyen service" | provider-agnostic |
| 120 | with adyen payment provider > "updates invoice payment status to succeeded" | provider-agnostic |
| 127 | with adyen payment provider > "does not send a payment requested email" | provider-agnostic |
| 147 | with adyen payment provider > when the payment fails > "sends a payment requested email" | provider-agnostic |
| 163 | with gocardless payment provider > "creates a payment and calls the gocardless service" | provider-agnostic |
| 185 | with gocardless payment provider > "updates invoice payment status to succeeded" | provider-agnostic |
| 192 | with gocardless payment provider > "does not send a payment requested email" | provider-agnostic |
| 204 | with gocardless payment provider > when the payment fails > "sends a payment requested email" | provider-agnostic |
| 453 | when the provider raises AlreadyPaidError > "skips silently and drops the unused pending payment" | paid-race |
| 461 | when the provider raises AlreadyPaidError > "does not send the payment request email" | paid-race |
| 467 | when a concurrent attempt already advanced the shared payment (regression) > "does not destroy a payment that now has a provider reference" | paid-race |
| 486 | when a concurrent attempt already advanced the shared payment (regression) > "does not destroy a payment that was already marked succeeded" | paid-race |
| 660 | with adyen payment provider > "enqueues a job to create a adyen payment" | async |
| 670 | with gocardless payment provider > "enqueues a job to create a gocardless payment" | async |
| 680 | with strip payment provider > "enqueues a job to create a stripe payment" | async |
