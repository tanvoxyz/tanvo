# Unported spec cases

Spec cases of the NetSuite integration that are not ported, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case of the files in `testdata/specs.json` (checked by `go run ./tools/speccov`).

Cases: 47. Ported: 43. Unported: 4.

## Reasons

- **legacy** (4): Asserts the payload of a NetSuite integration whose legacy_script setting is true. No service, mutation or API of v1.55.0 sets it (it is only declared, app/models/integrations/netsuite_integration.rb:8, and read, app/services/integrations/aggregator/contacts/payloads/netsuite.rb:72), so every integration has it false and Tanvo has no such setting; the false cases are ported.

## Cases

### services/integrations/aggregator/contacts/payloads/netsuite_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 226 | when legacy script is true > when shipping address is present > when shipping address is not the same as billing address > "returns the payload body without lines" | legacy |
| 257 | when legacy script is true > when shipping address is present > when shipping address is the same as billing address > "returns the payload body without lines" | legacy |
| 290 | when legacy script is true > when shipping address is not present > when billing address is present > "returns the payload body without lines" | legacy |
| 310 | when legacy script is true > when shipping address is not present > when billing address is not present > "returns the payload body without lines" | legacy |
