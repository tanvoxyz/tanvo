# Unported spec cases

Spec cases of the payment connections, billing object connections and payment methods that are not ported, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case of the files in `testdata/specs.json` (checked by `go run ./tools/speccov`).

Cases: 144. Ported: 110. Unported: 34.

## Reasons

- **codes** (8): Connection codes: a connection is named by its integration's code (unique per organization), so there is no per-connection code to set, derive, rename or keep unique. The rule that does exist, one live connection per vendor and customer (base_customer.rb:32), is checked by UpsertConnection.
- **customer-provider** (6): The customer's payment_provider and payment_provider_code fields: Tanvo keeps no denormalized copy of the customer's provider. The connection in effect is the only one, else the default one (payment.DefaultProviderCustomer), which the is_default cases cover.
- **lock** (4): The customer's payment method lock (port.Locker, a transaction-scoped advisory lock) blocks until it is held instead of failing after a timeout, so there is no lock acquisition failure; a failure to lock is a store error that fails the transaction.
- **batch** (2): No batch replacement of a customer's connections: they are created or updated one at a time (UpsertCustomerConnection) and removed explicitly (DeleteCustomerConnection), so there is no array to omit, leave nil or match by ID.
- **details-delete** (2): Deleting detail keys: the only caller inserts (moneyhash_service.rb:89-94), and the details are typed fields (payment.MethodDetails), not an open hash.
- **multi** (2): Kept deviation: a customer has several payment connections, one per vendor, with one default, as CreateConnectionService allows (create_connection_service_spec.rb:135-152); the batch path of v1.55.0 still caps the array at one, which is the transitional limit of the same feature, not a rule.
- **typed** (2): The connections field is a typed map of ConnectionChoice messages in the API (libs/proto, protovalidate), so a choice, or the field itself, cannot be anything but an object.
- **customer** (1): A payment method is created for a provider customer, which always belongs to a customer; there is no call without one.
- **lock-mock** (1): A mock assertion that Customers::LockService is called. setDefault takes the customer's payment method lock (libs/billing/payments/methods.go), and the lock's serialization is the store's, covered by the porttest locks case.
- **manual** (1): The reserved lago_manual connection: manual collection is a payment method type in Tanvo, not a pseudo-connection (payment.Method, parity §1.33).
- **query-count** (1): An ActiveRecord query-count assertion on the routing of a preloaded collection. Tanvo routes one billing object per call (kernel.ConnectionRouting reads its choices and its customer's connections), so there is no collection preload to count.
- **race** (1): Two interleaved transactions are not reproducible in a service test. The live (provider customer, provider method) key of payment_methods (libs/store/schema/15_payment.sql) makes the losing insert a conflict, after which findOrCreateMethod reads the winner back (libs/billing/payments/events.go); the key is covered by the porttest payment methods case.
- **rails** (1): Rails and ActiveRecord mechanics with no Go counterpart: associations and preloading. The domain types and their repositories are tested in libs/core and the store's porttest suites.
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
