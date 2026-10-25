# Unported spec cases

Spec cases of the streaming destinations and their delivery logs that are not ported, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case of the files in `testdata/specs.json` (checked by `go run ./tools/speccov`).

Cases: 37. Ported: 31. Unported: 6.

## Reasons

- **rails** (3): ActiveRecord mechanics with no Go counterpart: the paper_trail change history, the organization association and find_by on an array column. Destinations are configured by the operator in SQL (libs/store/schema/29_streaming.sql); their repository is tested by the store's porttest streaming case.
- **interface** (2): The producer is not built by the destination: apps/server injects a port.StreamProducer (libs/webhooks/kinesis for Kinesis, the only type), so there is no abstract producer method or per-type producer class to assert.
- **secrets** (1): No destination type stores secrets: Kinesis authenticates with an assumed role and an optional external ID, which are settings, so there is no secrets column to round-trip.

## Cases

### models/streaming_destinations/base_destination_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it_behaves_like paper_trail traceable | rails |
| 17 | it do | rails |
| 77 | "is the subtype's responsibility" | interface |
| 144 | "is the only lookup that works, find_by on the array column raises" | rails |
| 151 | "round-trips through SecretsStorable" | secrets |

### models/streaming_destinations/kinesis_destination_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 25 | "builds the producer for its own transport" | interface |
