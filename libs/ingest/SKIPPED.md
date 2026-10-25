# Unported spec cases

Spec cases of the ingestion pipeline (re-enrichment after catalog changes) that are not ported, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case of the files in `testdata/specs.json` (checked by `go run ./tools/speccov`).

Cases: 23. Ported: 14. Unported: 9.

## Reasons

- **history-repair** (7): Rules repairing enrichments of lago's history: recurring metrics before 2025-11-25 and pricing_group_keys before 2026-03-06 (pre_enrichment_check_service.rb:9-13, 40-59). Tanvo's enriched rows carry no subscription, charge or grouping column (architecture §7.2), so there is nothing of that kind to repair.
- **current-copy** (1): Both readers (the PostgreSQL raw log and the ClickHouse ledger) return one current copy per deduplication key; covered by the libs/eventstore conformance tests.
- **in-process** (1): Tanvo re-enriches in process and stores the events with eventstore.Replacer instead of producing raw messages to Kafka (its writer keeps the first copy of a key, so a produced copy would be dropped), so the `reprocess` source_metadata flag of the message does not exist.

## Files skipped whole

The other lago-api (`spec/`) and events-processor (`events-processor/`) cases libs/ingest does not port, by file, with the reason. Everything else in the files cited by the ported `source:` comments is ported.

### lago-api

| Source | Reason |
| :--- | :--- |
| `spec/services/events/create_service_spec.rb:55` (enqueues a post-processing job) | No post-processing job: ModePostgres enriches and routes inside the ingestion transaction. |
| `spec/services/events/create_service_spec.rb:72` (no Kafka produce when the job fails) | Same: there is no job to fail; a routing failure rolls the transaction back (ported as "rolls back when routing fails"). |
| `spec/services/events/create_service_spec.rb:88` (job cannot be serialized) | Rails/ActiveJob specific. |
| `spec/services/events/create_service_spec.rb:190` (produces on Kafka in postgres orgs) | Tanvo writes an organization's events in one mode only: ModeKafka produces, ModePostgres writes. |
| `spec/services/events/create_batch_service_spec.rb:83`, `:92`, `:98`, `:103` (post-processing jobs) | No post-processing job (see above). |
| `spec/services/events/create_batch_service_spec.rb:395` (`api_post_processed: false`) | No `source_metadata`: the raw message carries `expression_evaluated`, and in-advance routing in ModeKafka always runs in the in-advance consumer. |
| `spec/services/events/kafka_producer_service_spec.rb:49`, `:62` (single event wrapping; no Kafka config) | Ruby call shape; a missing producer is a configuration error of ModeKafka. |
| `spec/services/events/enrich_service_spec.rb:81`, `:88` (enriched_at database round trip) | Enriched rows are stored by libs/eventstore, whose tests cover precision. |
| `spec/services/events/enrich_service_spec.rb:321`–`:414` (target wallet code) | `events_targeting_wallets` is wallet allocation (domain §15), routed by billing, not stored on enriched rows. |
| `spec/services/events/enrich_service_spec.rb:415`, `:437` (charge filter match), `:490` (one row per charge), `:504` (persist flag) | Tanvo stores one enriched row per event with no charge or filter column (architecture §7.2); filters are applied at query time. |
| `spec/services/events/post_process_service_spec.rb:42` (tracks subscription activity) | Covered by the usage refresh signal (ported as "signals usage"); activity tracking itself belongs to usage monitoring. |
| `spec/services/events/post_process_service_spec.rb:52`–`:60` (postgres enriched events flag) | Feature flag of lago; Tanvo always writes enriched rows. |
| `spec/services/events/post_process_service_spec.rb:130`–`:225` (product catalog rate cards and contracts) | Contract and rate card effectivity is resolved by libs/billing behind `InAdvanceBiller`. |
| `spec/services/events/post_process_service_spec.rb:231`–`:294` (targeted wallets) | Wallet allocation (domain §15), not ingestion. |
| `spec/services/events/billing_period_filter*`, `billing_period_filters/**` | Billing-period filtering is part of libs/billing. |
| `spec/services/events/stores/**` | Event stores are libs/eventstore. |
| `spec/services/events/pay_in_advance*` | In-advance fee computation is libs/billing (behind `InAdvanceBiller`). |
| `spec/services/events/delete_for_metric_service_spec.rb`, `re_enrich_all_service_spec.rb` | Metric deletion and re-enrichment are catalog jobs (libs/jobs) over `eventstore.Replacer`. |
| `spec/services/events/post_validation_service_spec.rb` | Post-validation (the `events.errors` webhook) is a periodic job over stored events. |
| `spec/services/events/validate_creation_service_spec.rb` | Validation of billing estimates (`/events/estimate_fees`), part of libs/billing. |
| `spec/services/events/common_factory_spec.rb` | Ruby factory of event representations; no Go equivalent. |

### events-processor

| Source | Reason |
| :--- | :--- |
| `processors/events_processor/enrichment_service_test.go:87` (invalid timestamp) | Timestamps are parsed at intake; a raw message with an invalid timestamp is undecodable (ported as processor_test.go:261). |
| `processors/events_processor/enrichment_service_test.go:309` (in advance without billable metric) | Covered by "without billable metric": no metric, no charges. |
| `models/event_test.go`, `utils/time_test.go` | Timestamp conversion lives in core/event (`ParseTimestamp`, tested there); its float and string cases are ported at intake (create_service_spec timestamps). |
| `models/billable_metrics_test.go`, `models/subscriptions_test.go`, `models/stores_test.go`, `cache/**` | SQL models and the LMDB-style cache of the processor; Tanvo reads libs/core repositories through `Catalog`/`Cache` (tested in catalog_test.go). |
| `utils/env_test.go`, `utils/string_test.go`, `utils/json_test.go`, `utils/result_test.go`, `utils/logger_test.go`, `config/database/database_test.go` | Helpers with no Tanvo counterpart. |

## Cases

### services/events/stores/clickhouse/pre_enrichment_check_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 40 | with recurring BM subscriptions > when subscription started before cutoff and has active subscription with same external_id > "includes the subscription" | history-repair |
| 51 | with recurring BM subscriptions > when subscription started after cutoff > "excludes the subscription" | history-repair |
| 64 | with recurring BM subscriptions > when subscription external_id has no active subscription > "excludes the subscription" | history-repair |
| 90 | with pricing_group_keys subscriptions > when active subscription started before cutoff > "includes the subscription" | history-repair |
| 103 | with pricing_group_keys subscriptions > when subscription is terminated > "excludes the subscription" | history-repair |
| 114 | with pricing_group_keys subscriptions > when subscription started after cutoff > "excludes the subscription" | history-repair |
| 134 | with pricing_group_keys subscriptions > when pricing_group_keys is on charge_filter but not on charge > "includes the subscription" | history-repair |

### services/events/stores/clickhouse/re_enrich_subscription_events_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 144 | with reprocess set to false > "reflects reprocess flag in source_metadata" | in-process |
| 186 | with duplicated events > "only produces one message for the most recently ingested event" | current-copy |
