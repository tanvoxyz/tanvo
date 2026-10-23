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
| 0. Foundations | Monorepo, Nx plugins, mise, lint rules, licenses, documents, CI | `pnpm nx run-many -t build test lint` passes in CI | In progress |
| 1. Pure libraries (L0/L1) | `libs/money`, `libs/period`, `libs/expression` (Rust → WASM, Go host), `libs/pricing`, `libs/metering` (with the in-memory store and `storetest`), `libs/tax` | Every pure spec of their areas ported or listed as skipped with a reason | Done |
| 2. Domain core (L2) | `libs/core` (entities, repository interfaces, services' contracts), `libs/billing` (invoice generation, fees, credits, coupons, wallets, progressive billing, credit notes, commitments, thresholds, prorations, upgrades and downgrades) | Billing specs ported as pure decisions over in-memory repositories | In progress: `core` done, `billing` done in logic |
| 3. Infrastructure (L3) | `libs/proto` (API contracts), `libs/store` (PostgreSQL schema, sqlc, RLS), `libs/eventstore` (PostgreSQL, ClickHouse), `libs/ingest` (HTTP batch, Kafka/Redpanda), `libs/jobs` (River, schedules), `libs/webhooks`, `libs/documents` (Typst, Factur-X/UBL), `libs/mail`, `libs/auth` | Conformance suites pass on every implementation; documents' golden tests pass | In progress |
| 4. Server (L4) | `apps/server`: Connect + REST (Vanguard), OpenAPI, roles api/worker/consumer/all, configuration, health, metrics, migrations | End-to-end tests of every API resource and of a full billing cycle | In progress |
| 5. Providers | Payment providers (Stripe, Adyen, GoCardless, Cashfree, Flutterwave, Moneyhash), accounting/CRM/tax integrations, streaming destinations | Recorded-fixture tests per provider; webhooks end to end | Not started |
| 6. Console | `apps/web`: the administration console on the Connect API, customer portal | Feature coverage matching the API | Not started |
| 7. Enterprise and Cloud | `ee/*` services (analytics, assistant, MCP, revenue recognition, …), cells, RLS hardening, Helm charts | Cloud deployment runs the end-to-end suite | Not started |
| 8. Beyond parity | The improvements of §4, learned from OpenMeter and Flexprice | Each one has its own design decision, tests and Parity row (marked as a Tanvo addition) | Not started; begins after phases 1–5 are done and tested |

## 3. Current Status

* Phase 0: scaffold, Nx plugins, licenses, CI and every document (with translations) are in place.
* Phase 1 is done: `libs/money`, `libs/period` (623 ported cases), `libs/expression` (151 cases, Rust → WASM with input limits), `libs/pricing` (433 cases), `libs/metering` (478 cases, with the `storetest` conformance suite) and `libs/tax` (264 cases). [Parity](parity.md) marks their features `logic done`.
* Phase 2: `libs/core` is done; `libs/billing` is done in logic (about 1,480 ported lago-api cases, scenario specs included, with store-backed PostgreSQL tests for its locks; the cases left out are listed in its `SKIPPED.md` files), and [Parity](parity.md) marks its implemented features `logic done`; `apps/server` runs its jobs and its periodic jobs, and ingestion feeds it in-advance charges and usage signals; usage, lifetime and wallet alerts are evaluated from subscription activity and wallet changes; every v1.55.0 clock job runs except those of modules still to come (contracts, billing segments, scheduled orders, dunning campaigns, payments, inbound webhooks), and the catalog cache is invalidated on commit through PostgreSQL notifications. Still to wire: payments and documents. Until the payments orchestration (phase 5) replaces it, `payments.create_invoice_payment` completes without a payment (`apps/server/internal/app/billing.go`, `registerPending`), and its payment-status update is to call `wallets.Service.HandleCreditInvoicePayment` for a credit invoice whose payment succeeded or failed, which no caller does yet; the document jobs are cancelled until `libs/documents` is wired to a blob store.
* Phase 3: `libs/proto`, `libs/eventstore`, `libs/store` (PostgreSQL schema, migrations, RLS, transactional jobs, and the repository conformance suite passing on memstore and PostgreSQL) and `libs/jobs` (River, the periodic schedule, tenant fairness) are done; `libs/webhooks`, `libs/ingest`, `libs/auth` and `libs/mail` and `libs/documents` (Typst PDF/A-3b, Factur-X, UBL and CII, ten locales including zh-CN) are done in logic; the 15-minute usage buckets are maintained incrementally on both event stores, with a periodic repair job on ClickHouse. The core pass aligned the domain model with the v1.55.0 baseline: sessions, sign-in state, devices and multi-role memberships; purchase order numbers and x402 columns; invoice credits, cached aggregations and fee periods; plan, feature and entitlement soft deletion; and list filters with their ordering.
* Phase 4: `apps/server` runs `tanvo serve` (roles api, worker, consumer, all) and `tanvo migrate`, with typed configuration, OpenTelemetry, health and readiness, the job registry and periodic schedule, and event ingestion over Connect and REST, covered by end-to-end tests; billing runs end to end (events ingested over the EventService are billed in advance and at the period end by the billing clock job); the other API resources are to come.

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

Not adopted: Ent with automatic migrations, uber/fx, Temporal, CEL expressions, a single encryption key, roles defined in a static file, an `environment_id` column on every row, webhook payloads built at delivery time, routing events by customer.
