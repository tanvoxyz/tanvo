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
| 1. Pure libraries (L0/L1) | `libs/money`, `libs/period`, `libs/expression` (Rust → WASM, Go host), `libs/pricing`, `libs/metering` (with the in-memory store and `storetest`), `libs/tax` | Every pure spec of their areas ported or listed as skipped with a reason | In progress: `money` done |
| 2. Domain core (L2) | `libs/core` (entities, repository interfaces, services' contracts), `libs/billing` (invoice generation, fees, credits, coupons, wallets, progressive billing, credit notes, commitments, thresholds, prorations, upgrades and downgrades) | Billing specs ported as pure decisions over in-memory repositories | Not started |
| 3. Infrastructure (L3) | `libs/proto` (API contracts), `libs/store` (PostgreSQL schema, sqlc, RLS), `libs/eventstore` (PostgreSQL, ClickHouse), `libs/ingest` (HTTP batch, Kafka/Redpanda), `libs/jobs` (River, schedules), `libs/webhooks`, `libs/documents` (Typst, Factur-X/UBL), `libs/mail`, `libs/auth` | Conformance suites pass on every implementation; documents' golden tests pass | Not started |
| 4. Server (L4) | `apps/server`: Connect + REST (Vanguard), OpenAPI, roles api/worker/consumer/all, configuration, health, metrics, migrations | End-to-end tests of every API resource and of a full billing cycle | Not started |
| 5. Providers | Payment providers (Stripe, Adyen, GoCardless, Cashfree, Flutterwave, Moneyhash), accounting/CRM/tax integrations, streaming destinations | Recorded-fixture tests per provider; webhooks end to end | Not started |
| 6. Console | `apps/web`: the administration console on the Connect API, customer portal | Feature coverage matching the API | Not started |
| 7. Enterprise and Cloud | `ee/*` services (analytics, assistant, MCP, revenue recognition, …), cells, RLS hardening, Helm charts | Cloud deployment runs the end-to-end suite | Not started |

## 3. Current Status

* Phase 0: scaffold, Nx plugins, licenses, `architecture`, `development`, `testing` and this document are in place; `domain`, `parity`, CI and the translations are being written.
* Phase 1: `libs/money` is complete (exact decimals, ISO 4217 table, rounding modes, minor units, allocation). `libs/period`, `libs/expression`, `libs/pricing` and `libs/metering` are in progress.
