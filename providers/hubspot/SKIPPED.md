# Unported spec cases

Spec cases of the HubSpot integration that are not ported, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case of the files in `testdata/specs.json` (checked by `go run ./tools/speccov`).

Cases: 53. Ported: 46. Unported: 7.

## Reasons

- **premium** (5): Premium license and premium integration gates. Tanvo has no premium gates: every integration is available (parity §1.50).
- **rails** (2): A stubbed update! raising ActiveRecord::RecordInvalid: saving the portal ID has no model validation to fail in Tanvo.

## Cases

### services/integrations/hubspot/create_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 31 | without premium license > "does not create an integration" | premium |
| 35 | without premium license > "returns an error" | premium |
| 45 | with premium license > with hubspot premium integration not present > "returns an error" | premium |

### services/integrations/hubspot/save_portal_id_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 34 | when the service fails > "does not change the portal ID" | rails |
| 38 | when the service fails > "returns an error message" | rails |

### services/integrations/hubspot/update_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 26 | without premium license > "returns an error" | premium |
| 36 | with premium license > with hubspot premium integration not present > "returns an error" | premium |
