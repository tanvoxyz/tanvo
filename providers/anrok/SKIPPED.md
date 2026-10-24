# Unported spec cases

Spec cases of the Anrok integration that are not ported, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case of the files in `testdata/specs.json` (checked by `go run ./tools/speccov`).

Cases: 19. Ported: 15. Unported: 4.

## Reasons

- **unreachable** (4): The payload is never built in v1.55.0, so there is no behavior to port. Anrok contacts are not synced: IntegrationCustomers::AnrokService#create stores the link without calling the provider (app/services/integration_customers/anrok_service.rb:16-27) and IntegrationCustomers::UpdateService returns before the contact update for Anrok (app/services/integration_customers/update_service.rb:18). The Anrok invoice and credit note accounting payloads are selected only for an accounting integration customer, and accounting_kind is NetSuite and Xero only (app/models/integration_customers/base_customer.rb:46-48).

## Cases

### services/integrations/aggregator/contacts/payloads/anrok_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 29 | "returns the payload body" | unreachable |
| 52 | "returns the payload body" | unreachable |

### services/integrations/aggregator/credit_notes/payloads/anrok_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 9 | it_behaves_like an integration payload | unreachable |

### services/integrations/aggregator/invoices/payloads/anrok_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 9 | it_behaves_like an integration payload | unreachable |
