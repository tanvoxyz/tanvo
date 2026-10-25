# Unported spec cases

Spec cases of the event store, the attributed usage query and the ClickHouse log store that are not ported, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case of the files in `testdata/specs.json` (checked by `go run ./tools/speccov`).

Cases: 81. Ported: 59. Unported: 22.

## Reasons

- **associations** (9): ActiveRecord associations: the logs keep the IDs of their organization, user, API key and resource, read by libs/auth (user emails) and the API.
- **available** (7): Tanvo serves the logs in every installation, from PostgreSQL without ClickHouse (parity §1.45), and with no premium or security_logs gate (parity §1.45, §19), so there is no feature_unavailable or forbidden failure.
- **log-id** (3): ActiveRecord callback generating a missing ID: Tanvo's log writers always set the ID (a UUIDv7 for security and activity logs, the request ID for API logs) before the log is stored.
- **null-period** (2): audit_logs_period is never null in Tanvo (default 30 days, organization.DefaultAuditLogsRetentionDays), so every list applies a retention.
- **no-final** (1): Reading events_enriched without FINAL when the organization has no ClickHouse deduplication: Tanvo's ClickHouse store always reads the current copy of each event (FINAL, parity §1.42 and the event store row).

## Cases

### models/clickhouse/activity_log_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it { is_expected.to belong_to(:organization) } | associations |
| 9 | it { is_expected.to belong_to(:resource) } | associations |
| 10 | it { is_expected.to belong_to(:customer).optional } | associations |
| 11 | it { is_expected.to belong_to(:subscription).optional } | associations |
| 12 | it { is_expected.to belong_to(:user).optional } | associations |
| 13 | it { is_expected.to belong_to(:api_key).optional } | associations |
| 16 | "sets the activity_id if it is not set" | log-id |

### models/clickhouse/api_log_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | it { is_expected.to belong_to(:organization) } | associations |
| 9 | it { is_expected.to belong_to(:api_key) } | associations |
| 12 | "sets the request_id if it is not set" | log-id |

### models/clickhouse/security_log_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 9 | it do | associations |
| 17 | "sets the log_id if it is not set" | log-id |

### queries/activity_logs_query_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 49 | with old activity logs > without audit_logs_period value > "returns all" | null-period |
| 178 | when activty logs are not available > it { expect(result).not_to be_success } | available |
| 179 | when activty logs are not available > it { expect(result).to be_failure } | available |
| 180 | when activty logs are not available > it { expect(result.error).to be_a(BaseService::ForbiddenFailure) } | available |

### queries/api_logs_query_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 50 | with old api logs > without audit_logs_period value > "returns all" | null-period |
| 195 | when api logs are not available > it { expect(result).not_to be_success } | available |
| 196 | when api logs are not available > it { expect(result).to be_failure } | available |
| 197 | when api logs are not available > it { expect(result.error).to be_a(BaseService::ForbiddenFailure) } | available |
| 198 | when api logs are not available > it { expect(result.error.code).to eq("feature_unavailable") } | available |

### services/events/stores/clickhouse/attributed_usage_query_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 119 | "reads the events without FINAL" | no-final |
