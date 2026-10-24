# Unported spec cases

Spec cases of the tax, accounting and CRM integrations orchestration that are not ported, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case of the files in `testdata/specs.json` (checked by `go run ./tools/speccov`).

Cases: 1198. Ported: 841. Unported: 357.

## Reasons

- **graphql** (141): GraphQL plumbing: the shared authentication and permission examples (requires current user/organization/permission) and the GraphQL type definitions. Tanvo serves Connect RPCs; authentication and permissions are libs/auth's, tested there, and the request and response shapes are the protos of libs/proto.
- **rails** (115): Rails and ActiveRecord mechanics with no Go counterpart: associations, paper_trail, the settings and secrets JSON accessors (Tanvo stores typed columns, tested through the services' stored values), single-table-inheritance scopes and customer_type, memoization, class factories (Tanvo dispatches on the provider with a typed interface, so an unsupported provider cannot be built), job lock keys, and a stubbed update! raising RecordInvalid (the company ID write has no model validation to fail).
- **nango** (35): Behavior of the Nango aggregator that Tanvo does not have: Tanvo calls each vendor's API directly, so there is no connection_id, Nango sync, account information, restlet endpoint, cursor parameter or Nango task and memory error (parity §1.37): connection_id is Nango's (models/integrations/*_integration.rb validates it), PerformSyncJob and SendRestletEndpointJob drive Nango (netsuite/create_service.rb:38-42), and the out of memory, task and orchestrator errors are Nango function failures (aggregator/base_service.rb:8-21).
- **premium** (23): Premium license and premium integration gates. Tanvo has no premium gates: every integration is available (parity §1.50).
- **planned** (16): The connection code of integration customers and billing objects routed to a connection are planned parity rows owned outside this module (parity §1.37 connection codes); their cases are ported with that work.
- **unreachable** (13): Code with no caller in the upstream application: the query paths without an integration (the resolvers require integration_id), HubSpot call_async services, the HubSpot companies and contacts object type ID helpers (hubspot_integration.rb:14-20) and the fee.tax_provider_error webhook (nothing enqueues it; send_webhook_job.rb:35 is only the mapping).
- **contention** (3): Kept deviation: a 5xx from the vendor is treated as server contention and retried by the job (apiclient retries, then the job's backoff), where the upstream returns the action_script_runtime_error failure of a Nango script. With a direct API a 5xx is the vendor's transient failure, not a script error.
- **email** (3): The document job carries no notify flag: libs/mail decides from the billing entity's email settings (libs/mail/documents.go), with no premium gate.
- **auth** (2): Okta integrations belong to libs/auth and are tested there.
- **one-api** (2): Kept deviation: one Connect RPC (UpsertConnection) serves the REST and dashboard clients, keyed by customer and integration, so there is no missing connection to report and the provider sync is enqueued as in create_or_update_batch_service.rb:48-60 rather than called synchronously as in netsuite_service.rb:22-23.
- **ids** (1): Tanvo fees always carry UUIDv7 IDs, previews included (fees/service.go), so there is no fee without an ID and the ID order is the generation order.
- **legacy_script** (1): Accessor of NetSuite's legacy_script setting, which no service, mutation or API of v1.55.0 sets (declared at app/models/integrations/netsuite_integration.rb:8 and read only at app/services/integrations/aggregator/contacts/payloads/netsuite.rb:72), so every integration has it false; Tanvo has no such setting and behaves as false (providers/netsuite ports the false cases).
- **map** (1): The proto map of currency mappings cannot hold a duplicate currency, so the duplicate error cannot be produced.
- **segment** (1): Segment product analytics (SegmentTrackJob): the core sends no product analytics, as for the other Segment calls (docs/parity.md, catalog cache row).

Kept deviations ported with their own expectations: HubSpot and Xero requests are throttled per connection, as both vendors limit per account or tenant (the upstream throttles them globally, config/initializers/throttling.rb), and Xero's daily limit is left to the 429 retry; payload IDs drop the lago_ prefix (customer_id, integration_id).

## Cases

### graphql/mutations/integration_collection_mappings/create_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 49 | it_behaves_like requires current user | graphql |
| 50 | it_behaves_like requires current organization | graphql |
| 51 | it_behaves_like requires permission | graphql |

### graphql/mutations/integration_collection_mappings/destroy_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 22 | it_behaves_like requires current user | graphql |
| 23 | it_behaves_like requires current organization | graphql |
| 24 | it_behaves_like requires permission | graphql |

### graphql/mutations/integration_collection_mappings/update_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 49 | it_behaves_like requires current user | graphql |
| 50 | it_behaves_like requires current organization | graphql |
| 51 | it_behaves_like requires permission | graphql |
| 91 | when currencies > when currency_code is duplicated > "returns a graphql error" | map |

### graphql/mutations/integration_customers/create_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 34 | it_behaves_like requires current user | graphql |
| 35 | it_behaves_like requires current organization | graphql |
| 36 | it_behaves_like requires permission | graphql |
| 38 | "creates an integration customer connection" | planned |

### graphql/mutations/integration_customers/destroy_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 23 | it_behaves_like requires current user | graphql |
| 24 | it_behaves_like requires current organization | graphql |
| 25 | it_behaves_like requires permission | graphql |

### graphql/mutations/integration_customers/set_as_default_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 44 | it_behaves_like requires current user | graphql |
| 45 | it_behaves_like requires current organization | graphql |
| 46 | it_behaves_like requires permission | graphql |

### graphql/mutations/integration_customers/update_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 35 | it_behaves_like requires current user | graphql |
| 36 | it_behaves_like requires current organization | graphql |
| 37 | it_behaves_like requires permission | graphql |
| 39 | "updates the code of the connection" | planned |

### graphql/mutations/integration_items/fetch_accounts_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 40 | it_behaves_like requires current user | graphql |
| 41 | it_behaves_like requires current organization | graphql |
| 42 | it_behaves_like requires permission | graphql |

### graphql/mutations/integration_items/fetch_items_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 36 | it_behaves_like requires current user | graphql |
| 37 | it_behaves_like requires current organization | graphql |
| 38 | it_behaves_like requires permission | graphql |

### graphql/mutations/integration_mappings/create_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 48 | it_behaves_like requires current user | graphql |
| 49 | it_behaves_like requires current organization | graphql |
| 50 | it_behaves_like requires permission | graphql |

### graphql/mutations/integration_mappings/destroy_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 36 | it_behaves_like requires current user | graphql |
| 37 | it_behaves_like requires current organization | graphql |
| 38 | it_behaves_like requires permission | graphql |

### graphql/mutations/integration_mappings/update_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 32 | it_behaves_like requires current user | graphql |
| 33 | it_behaves_like requires current organization | graphql |
| 34 | it_behaves_like requires permission | graphql |

### graphql/mutations/integrations/anrok/create_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 27 | it_behaves_like requires current user | graphql |
| 28 | it_behaves_like requires current organization | graphql |
| 29 | it_behaves_like requires permission | graphql |

### graphql/mutations/integrations/anrok/update_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 33 | it_behaves_like requires current user | graphql |
| 34 | it_behaves_like requires current organization | graphql |
| 35 | it_behaves_like requires permission | graphql |

### graphql/mutations/integrations/avalara/create_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 30 | it_behaves_like requires current user | graphql |
| 31 | it_behaves_like requires current organization | graphql |
| 32 | it_behaves_like requires permission | graphql |

### graphql/mutations/integrations/avalara/update_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 36 | it_behaves_like requires current user | graphql |
| 37 | it_behaves_like requires current organization | graphql |
| 38 | it_behaves_like requires permission | graphql |

### graphql/mutations/integrations/destroy_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 33 | it_behaves_like requires current user | graphql |
| 34 | it_behaves_like requires current organization | graphql |
| 35 | it_behaves_like requires permission | graphql |
| 54 | when okta integration > "deletes calling the okta destroy service" | auth |
| 59 | when okta integration > it_behaves_like produces a security log | auth |

### graphql/mutations/integrations/fetch_draft_invoice_taxes_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 96 | it_behaves_like requires current user | graphql |
| 97 | it_behaves_like requires current organization | graphql |
| 98 | it_behaves_like requires permission | graphql |

### graphql/mutations/integrations/hubspot/create_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 32 | it_behaves_like requires current user | graphql |
| 33 | it_behaves_like requires current organization | graphql |
| 34 | it_behaves_like requires permission | graphql |

### graphql/mutations/integrations/hubspot/sync_invoice_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 49 | it_behaves_like requires current user | graphql |
| 50 | it_behaves_like requires current organization | graphql |
| 51 | it_behaves_like requires permission | graphql |

### graphql/mutations/integrations/hubspot/update_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 36 | it_behaves_like requires current user | graphql |
| 37 | it_behaves_like requires current organization | graphql |
| 38 | it_behaves_like requires permission | graphql |

### graphql/mutations/integrations/netsuite/create_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 36 | it_behaves_like requires current user | graphql |
| 37 | it_behaves_like requires current organization | graphql |
| 38 | it_behaves_like requires permission | graphql |

### graphql/mutations/integrations/netsuite/update_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 39 | it_behaves_like requires current user | graphql |
| 40 | it_behaves_like requires current organization | graphql |
| 41 | it_behaves_like requires permission | graphql |

### graphql/mutations/integrations/salesforce/create_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 29 | it_behaves_like requires current user | graphql |
| 30 | it_behaves_like requires current organization | graphql |
| 31 | it_behaves_like requires permission | graphql |

### graphql/mutations/integrations/salesforce/sync_invoice_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 34 | it_behaves_like requires current user | graphql |
| 35 | it_behaves_like requires current organization | graphql |
| 36 | it_behaves_like requires permission | graphql |

### graphql/mutations/integrations/salesforce/update_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 34 | it_behaves_like requires current user | graphql |
| 35 | it_behaves_like requires current organization | graphql |
| 36 | it_behaves_like requires permission | graphql |

### graphql/mutations/integrations/sync_credit_note_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 49 | it_behaves_like requires current user | graphql |
| 50 | it_behaves_like requires current organization | graphql |
| 51 | it_behaves_like requires permission | graphql |

### graphql/mutations/integrations/sync_invoice_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 49 | it_behaves_like requires current user | graphql |
| 50 | it_behaves_like requires current organization | graphql |
| 51 | it_behaves_like requires permission | graphql |

### graphql/mutations/integrations/xero/create_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 31 | it_behaves_like requires current user | graphql |
| 32 | it_behaves_like requires current organization | graphql |
| 33 | it_behaves_like requires permission | graphql |

### graphql/mutations/integrations/xero/update_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 35 | it_behaves_like requires current user | graphql |
| 36 | it_behaves_like requires current organization | graphql |
| 37 | it_behaves_like requires permission | graphql |

### graphql/resolvers/integration_collection_mappings_resolver_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 25 | it_behaves_like requires current user | graphql |
| 26 | it_behaves_like requires current organization | graphql |
| 27 | it_behaves_like requires permission | graphql |

### graphql/resolvers/integration_items_resolver_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 29 | it_behaves_like requires current user | graphql |
| 30 | it_behaves_like requires current organization | graphql |
| 31 | it_behaves_like requires permission | graphql |

### graphql/resolvers/integration_resolver_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 33 | it_behaves_like requires current user | graphql |
| 34 | it_behaves_like requires current organization | graphql |
| 35 | it_behaves_like requires permission | graphql |

### graphql/resolvers/integrations/subsidiaries_resolver_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 45 | it_behaves_like requires current user | graphql |
| 46 | it_behaves_like requires current organization | graphql |
| 47 | it_behaves_like requires permission | graphql |

### graphql/resolvers/integrations_resolver_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 34 | it_behaves_like requires current user | graphql |
| 35 | it_behaves_like requires current organization | graphql |
| 36 | it_behaves_like requires permission | graphql |

### graphql/types/integration_collection_mappings/create_input_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integration_collection_mappings/currency_mapping_item_input_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integration_collection_mappings/currency_mapping_item_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integration_collection_mappings/object_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integration_collection_mappings/update_input_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integration_customers/anrok_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integration_customers/avalara_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integration_customers/hubspot_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integration_customers/input_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integration_customers/netsuite_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integration_customers/salesforce_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integration_customers/xero_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integration_items/object_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integration_mappings/create_input_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integration_mappings/object_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integration_mappings/update_input_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integrations/accounts/object_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integrations/anrok_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integrations/avalara/create_input_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integrations/avalara/update_input_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integrations/avalara_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integrations/hubspot/create_input_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integrations/hubspot/targeted_objects_enum_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 6 | "enumerizes the correct values" | graphql |

### graphql/types/integrations/hubspot/update_input_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integrations/hubspot_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integrations/netsuite_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integrations/premium_integration_type_enum_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 41 | "enumerizes the correct values" | graphql |

### graphql/types/integrations/salesforce/create_input_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integrations/salesforce/sync_invoice_input_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integrations/salesforce/update_input_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integrations/salesforce_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integrations/subsidiaries/object_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integrations/sync_credit_note_input_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integrations/sync_hubspot_invoice_input_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integrations/sync_invoice_input_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### graphql/types/integrations/xero_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it do | graphql |

### jobs/integration_customers/create_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 29 | "returns customer and integration for the lock key" | rails |

### jobs/integrations/aggregator/perform_sync_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 21 | when sync_items is true > "calls the aggregator sync service" | nango |
| 33 | when sync_items is false > "calls the aggregator sync service" | nango |

### jobs/integrations/aggregator/send_restlet_endpoint_job_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 15 | "sends restlet url to the aggregator" | nango |

### models/integration_collection_mappings/base_collection_mapping_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 12 | it_behaves_like paper_trail traceable | rails |
| 14 | it { is_expected.to belong_to(:integration) } | rails |
| 15 | it { is_expected.to belong_to(:organization) } | rails |
| 16 | it { is_expected.to belong_to(:billing_entity).optional } | rails |
| 126 | "push the value into settings" | rails |

### models/integration_collection_mappings/netsuite_collection_mapping_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 11 | "assigns and retrieve a setting" | rails |
| 20 | "assigns and retrieve a setting" | rails |
| 29 | "assigns and retrieve a setting" | rails |
| 38 | "assigns and retrieve a setting" | rails |
| 47 | "assigns and retrieve a setting" | rails |
| 56 | "assigns and retrieve a setting" | rails |
| 65 | "assigns and retrieve a setting" | rails |

### models/integration_collection_mappings/xero_collection_mapping_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 11 | "assigns and retrieve a setting" | rails |
| 20 | "assigns and retrieve a setting" | rails |
| 29 | "assigns and retrieve a setting" | rails |

### models/integration_customers/base_customer_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 14 | it { is_expected.to belong_to(:integration) } | rails |
| 15 | it { is_expected.to belong_to(:customer) } | rails |
| 16 | it { is_expected.to belong_to(:organization) } | rails |
| 35 | "derives the code from the integration when absent" | planned |
| 43 | "keeps an explicit code" | planned |
| 76 | "returns only accounting kind customers" | rails |
| 94 | "returns only tax kind customers" | rails |
| 114 | "returns only hubspot kind customers" | rails |
| 118 | "returns only salesforce kind customers" | rails |
| 130 | when type is netsuite > "returns customer type" | rails |
| 139 | when type is okta > "returns customer type" | rails |
| 148 | when type is anrok > "returns customer type" | rails |
| 157 | when type is xero > "returns customer type" | rails |
| 166 | when type is hubspot > "returns customer type" | rails |
| 175 | when type is salesforce > "returns customer type" | rails |
| 183 | when type is not supported > "raises an error" | rails |
| 190 | "push the value into settings" | rails |
| 204 | it { expect(integration_customer.get_from_settings("key1")).to eq("val1") } | rails |
| 206 | it { expect(integration_customer.get_from_settings(nil)).to be_nil } | rails |
| 207 | it { expect(integration_customer.get_from_settings("foo")).to be_nil } | rails |
| 211 | "assigns and retrieve a setting" | rails |
| 238 | it { is_expected.to validate_uniqueness_of(:code).scoped_to(:customer_id, :category).allow_nil } | planned |

### models/integration_customers/hubspot_customer_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 11 | "assigns and retrieve a setting" | rails |
| 20 | "assigns and retrieve a setting" | rails |

### models/integration_customers/netsuite_customer_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 11 | "assigns and retrieve a setting" | rails |

### models/integration_item_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it_behaves_like paper_trail traceable | rails |
| 10 | it { is_expected.to belong_to(:integration) } | rails |
| 11 | it { is_expected.to belong_to(:organization) } | rails |

### models/integration_mappings/base_mapping_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it_behaves_like paper_trail traceable | rails |
| 11 | it { is_expected.to belong_to(:integration) } | rails |
| 12 | it { is_expected.to belong_to(:organization) } | rails |
| 13 | it { is_expected.to belong_to(:mappable) } | rails |
| 14 | it { is_expected.to belong_to(:billing_entity).optional } | rails |
| 148 | "push the value into settings" | rails |
| 162 | it { expect(mapping.get_from_settings("key1")).to eq("val1") } | rails |
| 164 | it { expect(mapping.get_from_settings(nil)).to be_nil } | rails |
| 165 | it { expect(mapping.get_from_settings("foo")).to be_nil } | rails |

### models/integration_mappings/netsuite_mapping_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 11 | "assigns and retrieve a setting" | rails |
| 20 | "assigns and retrieve a setting" | rails |
| 29 | "assigns and retrieve a setting" | rails |

### models/integration_mappings/xero_mapping_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 11 | "assigns and retrieve a setting" | rails |
| 20 | "assigns and retrieve a setting" | rails |
| 29 | "assigns and retrieve a setting" | rails |

### models/integration_resource_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 12 | it_behaves_like paper_trail traceable | rails |
| 14 | it { is_expected.to belong_to(:syncable) } | rails |
| 15 | it { is_expected.to belong_to(:integration) } | rails |
| 16 | it { is_expected.to belong_to(:organization) } | rails |

### models/integrations/anrok_integration_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 9 | it { is_expected.to validate_presence_of(:connection_id) } | nango |
| 10 | it { is_expected.to have_many(:error_details) } | rails |
| 26 | "assigns and retrieve a secret pair" | rails |

### models/integrations/avalara_integration_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 9 | it { is_expected.to validate_presence_of(:connection_id) } | nango |
| 13 | it { is_expected.to have_many(:error_details) } | rails |
| 29 | "assigns and retrieve a secret pair" | rails |

### models/integrations/base_integration_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it_behaves_like paper_trail traceable | rails |
| 20 | it { is_expected.to have_many(:integration_mappings).dependent(:destroy) } | rails |
| 21 | it { is_expected.to have_many(:integration_collection_mappings).dependent(:destroy) } | rails |
| 22 | it { is_expected.to have_many(:integration_customers).dependent(:destroy) } | rails |
| 23 | it { is_expected.to have_many(:integration_items).dependent(:destroy) } | rails |
| 24 | it { is_expected.to have_many(:integration_resources).dependent(:destroy) } | rails |
| 27 | it { expect(integration.secrets_json).to eq(secrets) } | rails |
| 31 | "push the value into the secrets" | rails |
| 44 | it { expect(integration.get_from_secrets("api_secret")).to eq(api_secret) } | rails |
| 46 | it { expect(integration.get_from_secrets(nil)).to be_nil } | rails |
| 47 | it { expect(integration.get_from_secrets("foo")).to be_nil } | rails |
| 51 | "push the value into the secrets" | rails |

### models/integrations/hubspot_integration_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 9 | it { is_expected.to validate_presence_of(:connection_id) } | nango |
| 19 | "assigns and retrieve a secret pair" | rails |
| 96 | "returns the correct object type id for companies" | unreachable |
| 102 | "returns the correct object type id for contacts" | unreachable |

### models/integrations/netsuite_integration_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 17 | "assigns and retrieve a secret pair" | rails |
| 82 | "assigns and retrieve a setting" | legacy_script |

### models/integrations/salesforce_integration_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 26 | "returns salesforce" | rails |

### models/integrations/xero_integration_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 9 | it { is_expected.to validate_presence_of(:connection_id) } | nango |
| 18 | "assigns and retrieve a secret pair" | rails |

### queries/integration_collection_mappings_query_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 40 | when filters are empty > "returns all mappings" | unreachable |

### queries/integration_items_query_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 33 | "returns all integration items of an organization" | unreachable |

### serializers/v1/integrations/taxes/fee_error_serializer_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 20 | "serializes the object" | unreachable |

### services/integration_customers/create_connection_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 125 | when no code is given > "derives the code from the integration" | planned |
| 176 | when the code is already used in the category > "returns a validation failure" | planned |
| 181 | when the code is already used in the category > "does not leave a connection behind" | planned |
| 231 | when the provider sync fails > "returns the provider error" | one-api |

### services/integration_customers/create_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 79 | with netsuite premium integration present > when sync with provider is true > when customer external id is present > when a code is provided > "persists the explicit code and defaults the first connection of the category" | planned |

### services/integration_customers/destroy_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 52 | when billing objects route to the connection > "destroys the override rows along with the connection" | planned |
| 56 | when billing objects route to the connection > "does not raise on the foreign key" | planned |

### services/integration_customers/factory_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 19 | when the integration is NetsuiteIntegration > "returns an instance of IntegrationCustomers::NetsuiteService" | rails |
| 27 | when the integration is AnrokIntegration > "returns an instance of IntegrationCustomers::AnrokService" | rails |
| 35 | when the integration is XeroIntegration > "returns an instance of IntegrationCustomers::XeroService" | rails |
| 43 | when the integration is HubspotIntegration > "returns an instance of IntegrationCustomers::HubspotService" | rails |
| 51 | when the integration is SalesforceIntegration > "returns an instance of IntegrationCustomers::SalesforceService" | rails |
| 59 | when integration is nil > "raises a NotImplementedError" | rails |

### services/integration_customers/update_connection_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 21 | when integration customer is not found > "returns an error" | one-api |
| 30 | when only the code is updated > "updates the code" | planned |
| 55 | when the external customer id is updated > "updates the code" | planned |
| 113 | with a salesforce connection > "updates the code" | planned |
| 128 | with an anrok connection > "updates the code" | planned |
| 142 | when the customer is a partner account > "updates the code" | planned |

### services/integrations/aggregator/account_information_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 34 | "successfully fetches account information" | nango |
| 43 | it_behaves_like throttles! | nango |

### services/integrations/aggregator/accounts_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 59 | when cursor is nil > "returns params without cursor" | nango |
| 68 | when cursor is blank > "returns params without cursor" | nango |
| 77 | when cursor is present > "returns params with cursor" | nango |

### services/integrations/aggregator/companies/payloads/factory_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 20 | when customer is a hubspot customer > "returns payload" | rails |

### services/integrations/aggregator/contacts/payloads/factory_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 18 | when customer is a netsuite customer > "returns payload" | rails |
| 26 | when customer is a xero customer > "returns payload" | rails |
| 34 | when customer is an anrok customer > "returns payload" | rails |
| 44 | when customer is a hubspot customer > "returns payload" | rails |
| 58 | when customer is a netsuite customer > "returns payload body" | rails |
| 66 | when customer is a xero customer > "returns payload body" | rails |
| 74 | when customer is an anrok customer > "returns payload body" | rails |
| 88 | when customer is a netsuite customer > "returns payload body" | rails |
| 98 | when customer is a xero customer > "returns payload body" | rails |
| 107 | when customer is an anrok customer > "returns payload body" | rails |

### services/integrations/aggregator/credit_notes/payloads/factory_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 14 | when customer is a netsuite customer > "returns payload" | rails |
| 22 | when customer is a xero customer > "returns payload" | rails |
| 30 | when customer is an anrok customer > "returns payload" | rails |

### services/integrations/aggregator/invoices/hubspot/base_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 29 | "memoizes the integration customer" | rails |

### services/integrations/aggregator/invoices/hubspot/update_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 63 | when invoice exists > "enqueues invoice update job" | unreachable |
| 71 | when invoice does not exist > "returns an error" | unreachable |

### services/integrations/aggregator/invoices/payloads/factory_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 14 | when customer is a netsuite customer > "returns payload" | rails |
| 22 | when customer is a xero customer > "returns payload" | rails |
| 30 | when customer is an anrok customer > "returns payload" | rails |
| 38 | when customer is a hubspot customer > "returns payload" | rails |

### services/integrations/aggregator/items_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 163 | "returns the path" | nango |

### services/integrations/aggregator/payments/create_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 77 | when payment does not exist > "returns an error" | unreachable |

### services/integrations/aggregator/payments/payloads/factory_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 14 | when integration is a netsuite integration > "returns payload" | rails |
| 22 | when integration is a xero integration > "returns payload" | rails |
| 30 | when integration is an anrok integration > "raises NotImplemented" | rails |

### services/integrations/aggregator/send_restlet_endpoint_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 24 | "successfully sends restlet endpoint" | nango |

### services/integrations/aggregator/subscriptions/hubspot/base_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 30 | "memoizes the integration customer" | rails |

### services/integrations/aggregator/subscriptions/hubspot/create_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 49 | when subscription exists > "enqueues subscription create job" | unreachable |
| 57 | when subscription does not exist > "returns an error" | unreachable |

### services/integrations/aggregator/subscriptions/hubspot/update_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 51 | when subscription exists > "enqueues subscription update job" | unreachable |
| 59 | when subscription does not exist > "returns an error" | unreachable |

### services/integrations/aggregator/subscriptions/payloads/factory_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 14 | when customer is a hubspot customer > "returns payload" | rails |
| 22 | when customer is an anrok customer > "raises NotImplemented" | rails |
| 30 | when customer is an netsuite customer > "raises NotImplemented" | rails |
| 38 | when customer is an xero customer > "raises NotImplemented" | rails |

### services/integrations/aggregator/sync_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 26 | "successfully calls sync endpoint" | nango |

### services/integrations/aggregator/taxes/invoices/base_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 146 | when fees are not persisted > "preserves generation order without requiring IDs or timestamps" | ids |

### services/integrations/aggregator/taxes/invoices/create_draft_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 355 | when service call is successful > when taxes are not successfully fetched > when it is an out of memory error > "raises OutOfMemoryError" | nango |
| 419 | when service call is not successful > when it is another server error > "returns an error" | contention |
| 433 | when service call is not successful > when it is a task in progress error > "raises TaskInProgressError" | nango |
| 442 | when service call is not successful > when it is a task expired error > "raises TaskExpiredError" | nango |
| 451 | when service call is not successful > when it is an orchestrator failure error > "raises OrchestratorFailureError" | nango |

### services/integrations/aggregator/taxes/invoices/create_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 393 | when service call is successful > when taxes are not successfully fetched for finalized invoice > when it is an out of memory error > "raises OutOfMemoryError" | nango |
| 441 | when service call is not successful > when it is a task in progress error > "raises TaskInProgressError" | nango |
| 450 | when service call is not successful > when it is a task expired error > "raises TaskExpiredError" | nango |
| 459 | when service call is not successful > when it is an orchestrator failure error > "raises OrchestratorFailureError" | nango |

### services/integrations/aggregator/taxes/invoices/negate_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 115 | when service call is successful > when negate invoice sync is NOT successful > when the response contains an out of memory error > "raises OutOfMemoryError" | nango |
| 147 | when service call is not successful > when it is a server error > "returns an error" | contention |
| 161 | when service call is not successful > when it is a task in progress error > "raises TaskInProgressError" | nango |
| 170 | when service call is not successful > when it is a task expired error > "raises TaskExpiredError" | nango |
| 179 | when service call is not successful > when it is an orchestrator failure error > "raises OrchestratorFailureError" | nango |

### services/integrations/aggregator/taxes/invoices/void_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 211 | when service call is successful > when void invoice sync is NOT successful > when the response contains an out of memory error > "raises OutOfMemoryError" | nango |
| 243 | when service call is not successful > when it is a server error > "returns an error" | contention |
| 257 | when service call is not successful > when it is a task in progress error > "raises TaskInProgressError" | nango |
| 266 | when service call is not successful > when it is a task expired error > "raises TaskExpiredError" | nango |
| 275 | when service call is not successful > when it is an orchestrator failure error > "raises OrchestratorFailureError" | nango |

### services/integrations/anrok/create_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 28 | without premium license > "does not create an integration" | premium |
| 32 | without premium license > "returns an error" | premium |

### services/integrations/anrok/update_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 28 | without premium license > "returns an error" | premium |

### services/integrations/avalara/create_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 29 | without premium license > "does not create an integration" | premium |
| 33 | without premium license > "returns an error" | premium |
| 43 | with premium license > when avalara premium integration is not present > "returns an error" | premium |

### services/integrations/avalara/fetch_company_id_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 38 | when the service fails > "does not change the company id" | rails |
| 42 | when the service fails > "returns an error message" | rails |

### services/integrations/avalara/update_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 29 | without premium license > "returns an error" | premium |
| 39 | with premium license > without avalara premium integration > "returns an error" | premium |

### services/integrations/netsuite/create_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 33 | without premium license > "does not create an integration" | premium |
| 37 | without premium license > "returns an error" | premium |
| 47 | with premium license > with netsuite premium integration not present > "returns an error" | premium |
| 77 | with premium license > with netsuite premium integration present > without validation errors > "calls Integrations::Aggregator::SendRestletEndpointJob" | nango |
| 84 | with premium license > with netsuite premium integration present > without validation errors > "calls Integrations::Aggregator::PerformSyncJob" | nango |

### services/integrations/netsuite/update_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 29 | without premium license > "returns an error" | premium |
| 39 | with premium license > with netsuite premium integration not present > "returns an error" | premium |
| 71 | with premium license > with netsuite premium integration present > without validation errors > "calls Integrations::Aggregator::SendRestletEndpointJob" | nango |
| 77 | with premium license > with netsuite premium integration present > without validation errors > "calls Integrations::Aggregator::PerformSyncJob" | nango |

### services/integrations/salesforce/create_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 26 | without premium license > "does not create an integration" | premium |
| 30 | without premium license > "returns an error" | premium |
| 40 | with premium license > with salesforce premium integration not present > "returns an error" | premium |

### services/integrations/salesforce/update_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 21 | without premium license > "returns an error" | premium |
| 31 | with premium license > with salesforce premium integration not present > "returns an error" | premium |

### services/integrations/xero/create_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 27 | without premium license > "does not create an integration" | premium |
| 31 | without premium license > "returns an error" | premium |
| 41 | with premium license > when xero premium integration is not present > "returns an error" | premium |

### services/integrations/xero/update_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 27 | without premium license > "returns an error" | premium |
| 37 | with premium license > when xero premium integration is not present > "returns an error" | premium |

### services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 264 | when taxes are fetched successfully > "enqueues GenerateDocumentsJob with email false" | email |
| 271 | when taxes are fetched successfully > with lago_premium > "enqueues GenerateDocumentsJob with email true" | email |
| 280 | when taxes are fetched successfully > with lago_premium > when billing entity does not have right email settings > "enqueues GenerateDocumentsJob with email false" | email |
| 288 | when taxes are fetched successfully > "calls SegmentTrackJob" | segment |

### services/webhooks/integrations/taxes/fee_error_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 19 | it_behaves_like creates webhook | unreachable |
