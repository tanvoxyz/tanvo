# Tanvo Roadmap

> English | [简体中文](roadmap.zh-CN.md)

> Related: [Architecture](architecture.md) · [Domain Model](domain.md) · [Parity](parity.md) · [Development](development.md) · [Testing](testing.md) · [AGENTS.md](../AGENTS.md)

---

## 1. Principles

* **Bottom-up.** Pure libraries first, so the lago-api specs with the most cases are ported against code without I/O; each layer is complete and tested before the next one builds on it.
* **Complete parity.** Every non-deprecated lago-api feature is implemented ([Parity](parity.md)); a phase is done when its rows in Parity are marked done, not when a subset works.
* **Always runnable.** From phase 3 on, `apps/server` starts and the end-to-end tests exercise what exists.

## 2. Phases

| Phase | Contents | Completion criteria | Status |
| :--- | :--- | :--- | :--- |
| 0. Foundations | Monorepo, Nx plugins, mise, lint rules, licenses, documents, CI | `pnpm nx run-many -t build test lint` passes in CI | Done |
| 1. Pure libraries (L0/L1) | `libs/money`, `libs/period`, `libs/expression` (Rust → WASM, Go host), `libs/pricing`, `libs/metering` (with the in-memory store and `storetest`), `libs/tax` | Every pure spec of their areas ported or listed as skipped with a reason | Done |
| 2. Domain core (L2) | `libs/core` (entities, repository interfaces, services' contracts), `libs/billing` (invoice generation, fees, credits, coupons, wallets, progressive billing, credit notes, commitments, thresholds, prorations, upgrades and downgrades) | Billing specs ported as pure decisions over in-memory repositories | Done |
| 3. Infrastructure (L3) | `libs/proto` (API contracts), `libs/store` (PostgreSQL schema, sqlc, RLS), `libs/eventstore` (PostgreSQL, ClickHouse), `libs/ingest` (HTTP batch, Kafka/Redpanda), `libs/jobs` (River, schedules), `libs/webhooks`, `libs/documents` (Typst, Factur-X/UBL), `libs/mail`, `libs/auth` | Conformance suites pass on every implementation; documents' golden tests pass | Done |
| 4. Server (L4) | `apps/server`: Connect + REST (Vanguard), OpenAPI, roles api/worker/consumer/all, configuration, health, metrics, migrations | End-to-end tests of every API resource and of a full billing cycle | Done |
| 5. Providers | Payment providers (Stripe, Adyen, GoCardless, Cashfree, Flutterwave, Moneyhash), accounting/CRM/tax integrations, streaming destinations | Recorded-fixture tests per provider; webhooks end to end | Done |
| 6. Console | `apps/web`: the administration console on the Connect API, customer portal | Feature coverage matching the API | Not started |
| 7. Enterprise and Cloud | `ee/*` services (analytics, assistant, MCP, revenue recognition, …), cells, RLS hardening, Helm charts | Cloud deployment runs the end-to-end suite | Not started |
| 8. Beyond parity | The improvements of §4, learned from OpenMeter and Flexprice | Each one has its own design decision, tests and Parity row (marked as a Tanvo addition) | In progress: the items found necessary are done (§4) |

## 3. Current Status

* Phases 1–5, the backend port of lago-api v1.55.0, are done. Every non-deprecated feature has its Tanvo module and is marked `done` in [Parity](parity.md), except the three x402 rows that v1.55.0 does not wire either (`logic done`), and the console (`apps/web`) and Enterprise (`ee/*`) rows of phases 6 and 7.
* Every upstream spec case of the ported areas is either ported with its `source:` citation or skipped with a reason, as `tools/speccov` checks (0 uncovered).
* `apps/server` runs every API resource over Connect and REST, the jobs and periodic schedule, ingestion in PostgreSQL and Kafka modes, documents, email, webhooks, payment providers, integrations and streaming destinations. End-to-end scenarios against PostgreSQL, ClickHouse and Redpanda containers cover billing cycles, charge models, progressive billing, prorations, credit notes, dunning, documents, ingestion, expressions, usage buckets and the providers.
* `pnpm nx run-many -t buf-lint lint tidy-check build test` passes on all 38 projects in CI, and the generated code and WASM module match their sources.
* Phase 8: the items of §4 found necessary are done (Kafka operations, migration safety, the ingestion barrier before periodic billing); the others are deferred or not adopted, with the reasons in §4.
* Phases 6 (console) and 7 (Enterprise and Cloud) have not started.

## 4. After Parity: Lessons from OpenMeter and Flexprice

Porting lago-api comes first. The items below come from a study of OpenMeter (`openmeterio/openmeter`, Apache-2.0) and Flexprice (`flexprice/flexprice`) in October 2026, and are implemented in phase 8, once the port is complete and tested. Only ideas are taken: OpenMeter's code relies on `float64`, Ent and a CGO Kafka client, and Flexprice's billing logic (`internal/ee/`) is under a proprietary license whose code must not be copied.

Two lessons were applied at once because they are lago-api's own current design: one enriched row per event with charge filters matched at query time, and 15-minute usage buckets ([Architecture §7.2, §7.4](architecture.md#72-enrichment)).

| # | Improvement | Source | Lands in |
| :--- | :--- | :--- | :--- |
| 1 | Every billing usage query takes an ingestion-time cutoff stored on the invoice, so refreshes, progressive invoices and regenerations are reproducible and late events are picked up predictably | OpenMeter | `eventstore`, `billing` |
| 2 | Delta rating: bill the period-to-date amount minus what was already billed per price component, with explicit reversal lines when a tier changes | OpenMeter | `pricing`, `billing` |
| 3 | A deterministic key per fee with a unique index; billing runs compare the target state with the stored one, so retries and reconciliation are safe | OpenMeter | `billing`, `store` |
| 4 | Preview and execution share one code path and one request type | Flexprice | `billing` |
| 5 | Retryable internal sub-states per external invoice step (tax, rendering, sync, payment) and validation issues stored on the invoice | OpenMeter | `core`, `billing` |
| 6 | Balance checkpoints (lifetime usage, wallet ongoing balance, alerts) with a grace window, invalidated from the time a late event affects | OpenMeter, Flexprice | `billing` |
| 7 | Credits consumed in expiry order, and released by expiry in the same order | OpenMeter, Flexprice | `billing` |
| 8 | Kafka operations: a dead-letter topic per consumer group, separate lanes for backfills and noisy tenants, declared topics, consumer lag metrics, partition key equal to the dedup key | OpenMeter, Flexprice | `ingest` |
| 9 | An event deletion API that refuses invoiced periods and is audited, and an event trace explaining why an event was or was not billed | Flexprice | `ingest`, `apps/server` |
| 10 | Read replicas with writer pinning: after a request, message or job writes, its reads go to the primary | Flexprice | `store` |
| 11 | Atlas migration lint in CI, test databases cloned from a template, webhook endpoints disabled after prolonged failure | OpenMeter | `store`, `webhooks` |
| 12 | Tax-inclusive prices, customer tax exemption, windowed aggregations (maximum or sum per window, e.g. GPU hours or peak concurrency) | Flexprice | `pricing`, `tax`, `metering` |

Decisions of October 2026. An item is done only where measurements or a concrete failure showed it necessary; the others wait for such evidence. Each addition is recorded in [Parity §5](parity.md#5-tanvo-additions) with what to check when porting newer lago releases.

| # | Decision |
| :--- | :--- |
| 1 | **Partly done.** The gap that matters is a Kafka backlog at a billing boundary: the periodic billing of Kafka-mode organizations now waits for an ingestion barrier ([Architecture §7.1](architecture.md#71-ingestion-paths)). A cutoff stored on each invoice is deferred: lago's draft refreshes take late events on purpose |
| 2 | **Not adopted.** It would change amounts that lago computes over the whole period |
| 3 | **Not needed.** Invoices already have unique keys and fees are replaced atomically; lago's unique indexes of in-advance fees were added instead, scoped by subscription ([Parity §4](parity.md)) |
| 4 | **Already the case.** A preview runs the billing code in a transaction that is rolled back |
| 5 | **Deferred** to the console (phase 6), which would show the sub-states |
| 6 | **Not needed now.** If profiling shows these balances slow, the usage buckets gain weighted-sum and unique-count states first |
| 7 | **Not needed.** Lago expires whole wallets, and its consumption order is ported |
| 8 | **Done:** consumer metrics (records by outcome, lag, retry age), isolation of rejected events and dead letter replay. **Already the case:** declared topics, partition key equal to the deduplication key. **Not adopted:** a dead-letter topic per consumer group (the dead letter records its source topic). **Deferred:** lanes for backfills and noisy tenants, per-topic configurations |
| 9 | **Deferred** |
| 10 | **Deferred** until a deployment needs read replicas |
| 11 | **Done:** migration lint (Atlas analyzers and PostgreSQL lock checks) and the migration and query code checks in CI, a lock timeout for migrations, files without a transaction for concurrent indexes ([Development §4.1](development.md)). **Already the case:** test databases cloned from a template. **Deferred:** disabling failing webhook endpoints, which lago never does |
| 12 | **Not adopted.** Customer tax exemption is already at parity; tax-inclusive prices and windowed aggregations would add API and amounts lago does not have |

A measurement outside the list also led to a change: River's fetch cooldown capped each queue shard at 50 jobs a second, so it is now 25 ms and tunable ([Benchmarks §1](benchmarks.md#1-job-queue-river-and-a-redis-queue)).

Not adopted: Ent with automatic migrations, uber/fx, Temporal, CEL expressions, a single encryption key, roles defined in a static file, an `environment_id` column on every row, webhook payloads built at delivery time, routing events by customer.

## 5. Phase 7 Plan: What Lago Keeps Closed

A study of lago-api v1.55.0 and its front end in October 2026 sorted Lago's non-open features into three kinds. Phase 7 starts only when it is scheduled; until then this section is the plan.

**Gated but open.** The 30 `Organization::PREMIUM_INTEGRATIONS`, `License.premium?` and the feature flags guard code that is in lago-api itself: progressive billing, lifetime usage, projected usage, previews, auto dunning, manual payments, receipts, payment authorization, NetSuite, Xero, HubSpot, Salesforce, Avalara, Okta, Entra ID, custom roles, API key permissions, security logs, multiple billing entities, revenue share, order forms, wallet-targeted events, the sender address and the watermark. All of it is ported to the AGPL core with no gate ([Parity §1.50](parity.md#150-lago-gates-and-their-tanvo-placement)). `forecasted_usage` guards nothing in v1.55.0 and is not ported ([Parity §2](parity.md#2-deprecated-and-not-ported)).

**Replaced by self-contained designs.** These are services Lago hosts that a self-hosted Tanvo must not need: the license server (`LAGO_LICENSE_URL`), Nango, the OAuth proxy, Segment tracking, and CS admin with its Slack notices. They are already replaced: no license checks in the core, direct provider clients, callbacks served by `apps/server`, no telemetry from installations, and `tanvo admin` ([Parity §3.2](parity.md#32-components)).

**Logic outside lago-api.** Only the client side is public: request filters, response fields, permissions and environment variables. These become enterprise services under `ee/`. Each runs as its own process and imports only `libs/proto` ([Architecture §14](architecture.md#14-enterprise-edition-ee)). Lago's computations cannot be read, so their definitions are Tanvo's own. Each is documented in [Domain](domain.md) and tested with Tanvo's own cases, since no lago spec exists to port.

| Order | Service | Replaces in Lago | Plan |
| :--- | :--- | :--- | :--- |
| 1 | `ee/license` (library) | `LAGO_LICENSE` and the license server | An Ed25519-signed key, verified offline. It holds the licensee, the expiry and the services it enables. Each `ee/*` service checks it at start-up and once a day. Tanvo Cloud signs a key per plan |
| 2 | Contracts in `libs/proto` | The Data API's REST interface, `FinanceAssistant*` and `AiConversation*` in GraphQL | Packages `tanvo.analytics.v1` and `tanvo.assistant.v1`, under Apache-2.0. The core issues a short-lived signed token, as portal tokens are. It names the organization, the member and the member's permissions (`data_api:view`, `ai_conversations:create`). The services verify it with the core's public key, and never receive an API key |
| 3 | `ee/mcp` | The MCP server at `LAGO_MCP_SERVER_URL` | Generated from the `libs/proto` descriptors: one tool per public RPC, its input schema taken from the request message and its description from the doc comments. Tools that write are hidden unless the token grants writes. It speaks Streamable HTTP and stdio, and calls the core's Connect API with the caller's token |
| 4 | `ee/analytics` | The Data API (`LAGO_DATA_API_URL`), Superset and the revenue recognition dashboard | It reads the core's export views (`exports_*`, [Parity §1.43](parity.md#143-data-exports)) through a read-only PostgreSQL role, or a ClickHouse copy when one is configured. It answers the eight Data API reports with lago's filters and granularities: MRR and its movements, MRR by plan, revenue streams by customer and by plan, prepaid credits, usage, invoiced usage and aggregated amounts. A revenue recognition report adds recognized and deferred revenue by period: a fee is spread over its service period and usage counts when it is incurred. Amounts stay exact (`money.Decimal`). The console draws the charts (phase 6); nothing embeds Superset. Golden tests run over a seeded organization |
| 5 | `ee/assistant` | The finance assistant (`LAGO_FINANCE_ASSISTANT_URL`) and AI conversations (Mistral and `lib/lago_mcp_client`) | An agent loop over a pluggable model provider: Anthropic, OpenAI, Mistral, or any OpenAI-compatible endpoint. Its tools come from `ee/mcp`, and `ee/analytics` answers its numeric questions. It stores conversations and messages per member in its own schema and streams answers over Connect server streaming. A result exports to CSV in the blob store behind a 7-day link, as in lago. Its tests replay recorded model responses |
| 6 | `ee/pipeline` | Lago's data pipeline | It copies the export views and their deletion records incrementally to customer warehouses: S3 as Parquet, BigQuery, Snowflake and ClickHouse ([Parity §1.43](parity.md#143-data-exports)) |

The core needs only items 1 and 2: the token issuer and the contracts. Services 3 to 6 are independent of each other, apart from the assistant using the MCP server. They can be built by separate agents in that order.
