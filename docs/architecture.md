# Tanvo Architecture Blueprint

> English | [简体中文](architecture.zh-CN.md)

> Related: [Domain Model](domain.md) · [Feature Parity](parity.md) · [Development](development.md) · [Testing](testing.md) · [Roadmap](roadmap.md) · [AGENTS.md](../AGENTS.md)

> Tanvo is an open-source usage-based billing and metering platform with a Go server at its core. It reimplements the functionality of [lago-api](https://github.com/getlago/lago-api) — metering, pricing, subscriptions, invoicing, credits, payments, integrations — on a modern stack. It is not compatible with Lago's API or data; wherever Lago carries legacy designs, Tanvo takes the best design available today.

---

## 1. Positioning and Principles

### 1.1 Goals
* **Feature parity with lago-api**: every lago-api feature that is not deprecated is implemented; [Feature Parity](parity.md) tracks each one.
* **One codebase, two deployments**: the same binary runs a single-tenant self-hosted installation (Tanvo plus PostgreSQL) and the multi-tenant Tanvo Cloud on Kubernetes; the difference lies only in configuration and in which implementations are wired behind the ports (§13).
* **Correct money**: amounts are exact decimals end to end; every state change and the work it triggers commit atomically (§6, §9).
* **Strongly typed contracts**: Protobuf + Connect for every API, with a resource-style REST/JSON surface transcoded from the same contracts (§4).
* **Scale by design**: high-volume event ingestion through Kafka/Redpanda into ClickHouse, horizontal workers, and cells for Tanvo Cloud (§7, §13).
* **Small operational footprint**: the minimal installation is one `CGO_ENABLED=0` binary, PostgreSQL and the `typst` executable; Redis, Kafka, ClickHouse and object storage are optional.

### 1.2 Non-goals
* API, webhook or database compatibility with Lago, or migration tooling from Lago (may come later as a separate tool).
* Legacy Lago features that Lago itself deprecated (listed in [Feature Parity §2](parity.md)).
* Databases other than PostgreSQL for business data.

### 1.3 Engineering Principles
1. **Bottom-up**: pure-function libraries (money, periods, expressions, pricing, metering, tax) come first, each complete with tests ported from lago-api before the next layer is built on top.
2. **No CGO**: native capabilities come from child processes (`typst`) or WASM modules on wazero (`libs/expression`).
3. **Libraries are usable on their own**: every Go library is an independent module that `go test`s without Nx; Nx only orchestrates and caches.
4. **Contract first**: `libs/proto` is the single source of truth for every API and cross-process boundary.
5. **Ports and adapters**: `libs/core` defines the domain and the ports; infrastructure (`store`, `eventstore`, `jobs`, providers) implements them; only `apps/server` wires.
6. **Tenant scoping everywhere**: every business row carries `organization_id`, and every query is scoped by it (§5.4).

### 1.4 License
Tanvo is derived from lago-api and lago-expression, both distributed under the **GNU AGPL-3.0**; the repository's code is therefore licensed under **AGPL-3.0-or-later**, with these exceptions:

| Path | License | Why |
| :--- | :--- | :--- |
| everything not listed below | AGPL-3.0-or-later | Derived from Lago |
| `libs/proto` (contracts, generated code) and published client SDKs | Apache-2.0 | Designed from scratch; clients and enterprise services link them |
| `ee/` (enterprise services) | Elastic License 2.0 | Source-available enterprise features (§14) |

Rules that follow from this:
* Code under `ee/` runs as **separate processes** and talks to the core only through `libs/proto` contracts and read-only data views; it **never imports an AGPL Go package or crate** (enforced by depguard, §3.2).
* `libs/proto` contains only contracts designed for Tanvo, never code ported from Lago.
* Third-party dependencies must be compatible with AGPL-3.0: MIT, BSD, ISC, Apache-2.0, MPL-2.0, LGPL, GPL-3.0, AGPL-3.0. GPL-2.0-only and proprietary libraries (River Pro, Sidekiq Pro, …) are not allowed.
* Contributions are accepted under a CLA so that the project can keep its licensing structure.
* `NOTICE` records the upstream copyrights and the changes.

---

## 2. Technology Choices

| Area | Choice | Notes |
| :--- | :--- | :--- |
| Server language | **Go (latest stable, currently 1.27)** | `iter.Seq`, `log/slog`, `testing/synctest`, `testing.B.Loop`, go.mod `tool` directive |
| Contracts and RPC | **Protobuf (edition 2023) + buf + Connect** (`connect-go`; `protobuf-es` / `connect-es` for TypeScript) | `buf lint` + `buf breaking`; **protovalidate** for request validation |
| REST | **[Vanguard](https://github.com/connectrpc/vanguard-go)** transcoding from `google.api.http` annotations | One implementation serves Connect, gRPC, gRPC-Web and REST/JSON (§4.2) |
| API reference | **protoc-gen-connect-openapi** + Scalar | OpenAPI 3.1 from the same annotations |
| HTTP | Standard library `net/http` (routing patterns, `http.Protocols` for h2c) | Provider webhooks and file downloads are plain HTTP handlers |
| Database | **PostgreSQL 17+** | `pgx/v5` native driver, numeric for decimals, partitioned tables, advisory locks |
| Queries and migrations | **sqlc** (pgx) + **Atlas** (declarative schema → versioned migrations) | Explicit SQL, generated type-safe Go; no ORM (§5) |
| Identifiers | **UUIDv7** | Time-ordered, index friendly |
| Decimals | **`cockroachdb/apd/v3`** wrapped by `libs/money` | Arbitrary precision, explicit rounding (§6) |
| Background jobs | **River** (PostgreSQL) | Transactional enqueue, unique and periodic jobs, leader election; Redis backend is a later option (§9) |
| Event streaming | **Kafka API (Redpanda / Kafka)** with **franz-go** | Optional high-throughput ingestion path (§7) |
| Event analytics store | **ClickHouse** (`clickhouse-go/v2`) | Optional; PostgreSQL partitions are the default (§7.4) |
| Cache, rate limits | In-process / PostgreSQL by default; **Redis or Valkey** optional (`go-redis/v9`) | Behind the `Cache` and `RateLimiter` ports |
| Expressions | **lago-expression ported to Rust** → WASM, run on **wazero** in Go and in the browser | Exact decimal semantics, one implementation for every host (§7.3) |
| Documents | **Typst** (child process) | PDF/A-3 with embedded e-invoice XML (Factur-X / ZUGFeRD); deterministic output (§10) |
| E-invoicing | UBL 2.1 and UN/CEFACT CII generated in Go | Peppol BIS, Factur-X, XRechnung profiles |
| Email | SMTP (`net/smtp` via `wneessen/go-mail`), `html/template` | Provider APIs (SES, …) behind the `Mailer` port |
| Outbound webhooks | **Standard Webhooks** signatures (HMAC-SHA256, `webhook-id` / `webhook-timestamp` / `webhook-signature`) | Replaces Lago's JWT/HMAC variants |
| Authentication | API keys (hashed), OIDC (Google, Okta, Entra ID, any OIDC IdP), email + password (argon2id), sessions | `coreos/go-oidc`, `golang.org/x/oauth2` |
| Authorization | Permission strings per role and per API key | Same permission model for users and keys (§4.4) |
| Encryption at rest | AES-256-GCM envelope encryption for secrets (provider credentials, tokens) | Keys from configuration or a KMS |
| Observability | `log/slog` + OpenTelemetry (traces, metrics, logs), Prometheus endpoint | Sentry optional through OTel |
| Command line and settings | **Cobra** + **koanf** | Flags, `TANVO_*` environment variables, `tanvo.toml` |
| Web UI | React + Vite + TanStack Router/Query + connect-es (`apps/web`, later phase) | Uses the same Connect contracts |

Engineering tooling — Nx, mise, buf, golangci-lint, CI — is described in [Development](development.md).

---

## 3. Repository Structure

### 3.1 Directory Structure

```text
tanvo/
├── go.work                       # Aggregates all Go modules
├── nx.json  package.json  pnpm-workspace.yaml  mise.toml
├── LICENSE  NOTICE
│
├── apps/
│   ├── server/                   # [Go] cmd/tanvo: wiring, configuration, roles, Connect services, REST, provider webhooks
│   └── web/                      # [TS] Admin UI and customer portal (later phase)
│
├── libs/
│   ├── proto/                    # [Go + TS] Contracts (Apache-2.0): *.proto, buf config, generated code
│   ├── money/                    # [Go] Decimals, currencies, amounts, rounding, allocation
│   ├── period/                   # [Go] Billing intervals, billing periods, anniversaries, time zones, proration ratios
│   ├── expression/               # [Rust → WASM] Event expressions; Go host package on wazero (go/)
│   ├── pricing/                  # [Go] Charge models: standard, graduated, graduated %, package, percentage, volume, dynamic; filters, grouping
│   ├── metering/                 # [Go] Aggregations: count, sum, max, unique count, weighted sum, latest; prorated and recurring variants
│   ├── tax/                      # [Go] Tax application, VAT identifiers, VIES client
│   ├── core/                     # [Go] Domain model and ports; no infrastructure dependencies
│   ├── billing/                  # [Go] Billing engine: fees, invoices, coupons, credits, wallets, credit notes, progressive billing, commitments
│   ├── store/                    # [Go] PostgreSQL schema (Atlas), sqlc queries, repository implementations
│   ├── eventstore/               # [Go] Event stores: PostgreSQL partitions and ClickHouse; aggregation queries
│   ├── ingest/                   # [Go] Event ingestion: validation, deduplication, enrichment, Kafka producer and consumers
│   ├── jobs/                     # [Go] River setup, job kinds, periodic schedules, per-tenant fairness
│   ├── webhooks/                 # [Go] Outbound webhook delivery and signing; inbound webhook storage and retries
│   ├── documents/                # [Go] Typst rendering, document templates, e-invoice XML
│   ├── mail/                     # [Go] Email templates and the Mailer port's SMTP implementation
│   └── auth/                     # [Go] API keys, sessions, OIDC, password hashing, permissions
│
├── providers/
│   ├── stripe/ adyen/ gocardless/ cashfree/ flutterwave/ moneyhash/   # [Go] Payment providers
│   ├── anrok/ avalara/                                                 # [Go] Tax providers
│   └── netsuite/ xero/ salesforce/ hubspot/                            # [Go] Accounting and CRM integrations
│
├── ee/                           # Enterprise services (ELv2), separate processes (§14)
│
├── tools/
│   ├── nx-go/  nx-cargo/         # Local Nx plugins
│   └── testport/                 # Extracts lago-api rspec cases into testdata
│
└── docs/
```

Directories are created when their step of the [roadmap](roadmap.md) starts.

### 3.2 Dependency Direction

```text
apps/server ──▶ billing ──▶ pricing / metering / tax / period / money
     │             │
     │             └──▶ core ──▶ money / period
     ├──▶ ingest ──▶ expression(go) / metering / core
     ├──▶ store / eventstore / jobs / webhooks / documents / mail / auth ──▶ core
     ├──▶ providers/* ──▶ core
     └──▶ proto

ee/* ──▶ proto        (never any other in-repo module)
```

* `money`, `period`, `expression`, `pricing`, `metering` and `tax` are pure libraries: they depend only on each other in the order shown and on the standard library or vetted third-party packages, never on `core`, storage or I/O (except `tax`'s VIES client behind an interface).
* `core` depends only on `money` and `period`.
* `billing` reaches storage, events, documents and providers only through ports in `core`.
* Infrastructure libraries depend on `core` but not on each other, with one exception: `ingest` uses `expression` (a pure library, like `metering`, whose event type it writes). Integration tests may use another library's backends; the depguard rules exempt test files.
* Only `apps/server` wires implementations to ports.
* The rules are enforced by depguard in `.golangci.yml`; changing them starts with this section.

---

## 4. Contracts and API (libs/proto, apps/server)

### 4.1 Contracts
* Sources live in `libs/proto/tanvo/<domain>/v1/` with `edition = "2023"`; packages are `tanvo.<domain>.v1`.
* Go code uses the Opaque API; Connect handlers use simple signatures.
* Every field that must be present is marked `(buf.validate.field).required = true`; protovalidate runs in an interceptor.
* Read-only RPCs carry `idempotency_level = NO_SIDE_EFFECTS`.
* Money is a message `{ string currency; string amount; }` with the amount as a decimal string; quantities are decimal strings; timestamps are `google.protobuf.Timestamp`; dates are `google.type.Date`.

### 4.2 REST through Vanguard
* Every public RPC has a `google.api.http` annotation following Google AIP resource conventions: `GET /v1/customers/{customer_id}`, `POST /v1/customers`, `PATCH /v1/customers/{customer.id}`, custom methods for lifecycle actions (`POST /v1/subscriptions/{id}:terminate`, `POST /v1/invoices/{id}:void`).
* Vanguard serves REST/JSON, Connect, gRPC and gRPC-Web from one `http.Handler`.
* JSON uses the proto field names (**snake_case**) on every protocol: the Connect JSON codec and Vanguard's codec are configured with `UseProtoNames`.
* Resources are addressed by Tanvo IDs; where Lago used customer-provided identifiers (`external_id`, `code`), Tanvo keeps them as unique, filterable fields and accepts them in place of an ID through an `{id}` that starts with a prefix (`external_id:acme-42`, `code:api_calls`).
* Lists use cursor pagination (`page_size`, `page_token`, `next_page_token`) and AIP-160 style filters.
* Errors are Connect error codes with `google.rpc.ErrorInfo` / `BadRequest` details; REST maps them to HTTP status codes with a JSON body.
* Writes accept an `Idempotency-Key` header; responses are stored for 24 hours per organization and key.

### 4.3 Non-RPC HTTP
Provider webhooks (`POST /webhooks/{provider}/{connection_id}`), document downloads (`GET /documents/{id}`), health (`/healthz`, `/readyz`) and metrics (`/metrics`) are plain `net/http` handlers.

### 4.4 Authentication and Authorization
* **Principals**: an API key (organization-scoped, hashed with SHA-256, shown once, with optional expiry, rotation with overlap, last-used tracking) or a user session (organization membership).
* **Permissions**: strings such as `customers:read`, `invoices:write`. Roles (built-in and custom) are sets of permissions; an API key carries its own set. Every RPC declares the permission it needs in a proto option; an interceptor checks it.
* **Users** sign in with email and password (argon2id) or OIDC (Google, Okta, Entra ID, generic). Sessions are opaque tokens stored hashed in PostgreSQL.
* **Customer portal**: short-lived signed tokens scoped to one customer.

---

## 5. Storage (libs/store)

### 5.1 PostgreSQL Only
Billing relies on exact `numeric` arithmetic, transactions spanning many tables, advisory locks and partitioning; PostgreSQL is the only supported database for business data.

### 5.2 Schema and Migrations
* The schema is declared in SQL under `libs/store/schema/` (one file per domain) and is the single source of truth.
* Atlas computes versioned migrations from it into `libs/store/migrations/`; merged migrations are never modified.
* Migrations run with `tanvo migrate` (a Kubernetes Job in Tanvo Cloud) and, for self-hosted installations, automatically at startup unless disabled.

### 5.3 Queries
* sqlc generates type-safe Go from SQL in `libs/store/queries/` against the pgx/v5 driver.
* Optional list filters use `sqlc.narg` parameters; full-text search uses PostgreSQL `tsvector` columns.
* `Store.InTx` runs a function in one transaction and gives it repositories and the River client bound to the same `pgx.Tx`.
* sqlc and pgx types never leave `libs/store`; upward it exposes the repository ports of `libs/core`.

### 5.4 Tenancy
* Every business table has `organization_id` as the first column of its primary key or of a unique index, and every query filters by it.
* The one exception is the x402 settlement's payment digest and transaction hash, which are unique across all organizations so that two organizations sharing a payout address are never both credited for one transfer ([Domain §24](domain.md#24-design-decisions), item 14). Reads of settlements still filter by organization.
* Tanvo Cloud additionally enables row-level security policies bound to `SET LOCAL tanvo.organization_id`, as defense in depth.

### 5.5 Conventions
* IDs are UUIDv7; customer-facing identifiers (`external_id`, `code`) are unique per organization.
* Amounts in minor units are `bigint`; precise amounts, unit prices and quantities are `numeric`.
* Enumerations are `text` with `CHECK` constraints, mirrored by Go string types.
* Deletion is explicit: resources with a lifecycle have a status (`terminated`, `archived`, `voided`); hard deletes are used where nothing references the row. There is no blanket soft delete.
* Changes that clients audit are recorded as domain events in an `audit_events` table in the same transaction, which also feeds webhooks and activity logs.

---

## 6. Money and Numbers (libs/money)

* `money.Decimal` is an immutable decimal on `apd.Decimal`, with a fixed operation context (precision 40, explicit rounding modes: half-up, half-even, up, down, ceiling, floor).
* `money.Currency` covers ISO 4217 (code, minor unit exponent) plus custom pricing units (§ Pricing units in [Domain](domain.md)).
* `money.Amount` pairs a currency with a decimal; conversion to minor units rounds explicitly.
* Allocation (splitting an amount across lines without losing a cent) and proportional distribution (coupon and credit distribution across fees) live here.
* Every rounding in the billing engine names its mode and its step; nothing rounds implicitly.

---

## 7. Events and Metering

### 7.1 Ingestion Paths
```text
POST /v1/events, /v1/events:batch
        │ validate, normalize, deduplicate (organization_id, transaction_id)
        ├── mode "postgres": insert raw + enriched rows in one transaction
        └── mode "kafka":    produce to events.raw
                               └─ ingest consumers: enrich → events.enriched ─┬→ ClickHouse writer (batched)
                                                                              ├→ in-advance charges → billing
                                                                              └→ usage refresh signals
```
* The mode is a per-installation setting with per-organization overrides in Tanvo Cloud.
* Connectors for SQS, Kinesis, HTTP and Kafka sources produce to `events.raw` (Redpanda Connect configurations or a built-in consumer).
* **Usage refresh signals** carry the customer, the subscriptions and the event watermark. The wallet refresh consumer collapses a poll batch into one refresh per customer with wallets, waits up to 5 seconds for the usage buckets to reach the watermark, and leaves signals older than 30 seconds to the 5-minute sweep, which stays authoritative ([Domain §14.1](domain.md#141-wallet)).

### 7.2 Enrichment
Enrichment resolves an event to its subscription(s) by `external_subscription_id` / `external_customer_id` and timestamp and to the billable metric by `code`, evaluates the metric's expression, extracts the aggregated value, the grouping keys and the precise amount, and writes **one enriched row per event**, with no charge or filter column. It looks up the metric's charges only to decide whether the event goes to in-advance billing. Charge filters are matched from the raw properties when usage is queried and when usage buckets are maintained, not by expanding the event into one row per filter: lago-api dropped its expanded table (`events_enriched_expanded`) in September 2026 for this reason, since a catalog change would otherwise require rewriting stored events and the row count grew with the number of filters. The ingest consumers keep an in-memory cache of the catalog, refreshed through change notifications.

### 7.3 Expressions (libs/expression)
* The language is lago-expression's: decimal arithmetic, `event.timestamp`, `event.code`, `event.properties.<name>`, strings, `concat`, `round`, `ceil`, `floor`, `least`, `greatest`.
* It is implemented once in Rust and compiled to `wasm32-unknown-unknown`. The Go package `libs/expression/go` runs the module on wazero with a batch interface (many events per call), and the web UI loads the same module for live previews.

### 7.4 Event Stores (libs/eventstore)
* The `EventStore` port offers the aggregation queries the metering library needs: counts, sums, max, latest, unique counts with add/remove semantics, weighted sums over time, grouped variants, prorated variants and breakdowns, all bounded by a billing period and filters.
* **PostgreSQL**: enriched events in a table partitioned by month and hash of organization.
* **ClickHouse**: `events_raw` and `events_enriched` as `ReplacingMergeTree` tables deduplicated by transaction, written in batches by the ingest consumers; activity, API and security logs live there too. Aggregated values are read as `Decimal(76, 26)`, so ingestion rejects a numeric property with more than 50 integer digits rather than let ClickHouse read it as zero, and the Kafka partition key is (organization, transaction) so that one consumer deduplicates each transaction. Lago produces raw events without a key (fec71c637); the key is a deliberate difference.
* Pre-aggregated usage buckets per (subscription, charge, charge filter, grouping key, **15-minute** UTC bucket) back current-usage queries in both stores for the aggregations that can be summed across buckets (count, sum, max, latest) and the charge models that only need their totals (standard, graduated, package, volume, graduated percentage, and dynamic); other aggregations read the events. Each bucket stores `units`, `events_count`, `precise_total_amount` (summed on sum rows, which serves dynamic charges), `last_event_at`, `last_ingested_at` and `target_wallet_code`. In-advance, prorated and recurring charges, and charges that accept a target wallet, are not served from buckets yet. Fifteen minutes is the granularity on which every real UTC offset (such as +05:30 or +05:45) puts a day boundary on a bucket boundary, so a billing period in any time zone is a whole number of buckets (as lago-api's `usage_buckets_15m`).

### 7.5 Metering (libs/metering)
`metering` implements the aggregation semantics (including proration, recurring metrics carried over periods, rounding) over an `iter.Seq` of events or over aggregates returned by an `EventStore`, so that the same rules are tested in memory with ported lago-api cases and run in the database in production.

---

## 8. Billing Engine (libs/billing)

* **Periods** (`libs/period`): calendar and anniversary billing, weekly to yearly intervals, trial periods, time zones of the organization, billing entity or customer, proration ratios, in-advance and in-arrears boundaries.
* **Pricing** (`libs/pricing`): every charge model computes an amount and its details from an aggregation result; fixed charges and add-ons use the same machinery.
* **Fees → invoices**: the engine builds fees for subscriptions (base fee, fixed charges, usage charges, commitments' true-ups), applies taxes (internal or provider), coupons, prepaid credits, credit notes and wallet credits in a defined order, and produces invoices with exact totals.
* **Scheduling**: each subscription's next billing instant is computed and a River job is scheduled for it; an hourly reconciliation job catches anything missed. This replaces Lago's hourly scans of every subscription.
* **Lifecycle**: draft invoices with a grace period, finalization, numbering, voiding, regeneration, progressive billing, usage thresholds and one-off invoices; see [Domain](domain.md).
* All writes of one billing run happen in a transaction with the jobs they trigger (documents, webhooks, payments).

---

## 9. Jobs and Scheduling (libs/jobs)

* **River** runs every background job; jobs are enqueued in the same transaction as the state change that requires them.
* `libs/store` inserts jobs through River's `InsertTx` on its own `pgx.Tx` (`store.WithJobInserter`), so a rollback leaves no job; without an inserter they go to the `job_outbox` table for a relay. River's tables live in their own `river` schema, migrated by River (they hold every organization's jobs and carry no row-level security). A unique key is scoped by organization and kind.
* **Retries**: an error that no rule of its kind classifies is not retried, since a job may have had effects outside its transaction (a payment, an email, a provider call) that a second run would repeat. Each kind declares the errors safe to retry, with their attempts and backoff (`ErrRetriable` 20 times, lock and sequence conflicts, provider rate limits). A job that stops retrying with an error is left `cancelled` in River, kept for inspection and manual retry, and counts as a failure in monitoring.
* Queues separate workloads (`billing`, `payments`, `webhooks`, `documents`, `integrations`, `exports`, `default`), each with its own concurrency, so that a deployment can run workers per queue.
* Periodic jobs (reconciliation, expirations, dunning, cleanups) are River periodic jobs inserted by its elected leader; no separate clock process is needed. They run on their own `clock` queue, unsharded and without token buckets, worked by every process that carries the schedule. A periodic job only scans and dispatches: it enqueues the due work as jobs of the organizations concerned on the workload queues, with the tick's time as their "now" and a unique key so that ticks over a backlog add no duplicates. A backlog on a workload queue therefore does not delay the schedule, and a failure in one organization does not stop the others. Retention cleanups, which have no per-organization work, run their batched deletes in the periodic job itself.
* **Tenant fairness**: jobs carry the organization; per-organization concurrency is bounded by sharding queues by organization hash and by token buckets at enqueue time, so that one tenant cannot starve others. The token buckets are per process, so with several API replicas an organization's effective rate is the sum of theirs.
* Per-event work (enrichment, in-advance charges at high volume) runs in the Kafka consumers, not in River.
* A Redis-backed queue is a later option behind the same narrow interface (transactional enqueue through an outbox, delays, periodic and unique jobs); it is added only if measurements show PostgreSQL as the bottleneck.

---

## 10. Documents (libs/documents)

* Invoices, credit notes, payment receipts and payment requests are rendered by **Typst** run as a child process with the document's data as JSON input; templates and fonts are embedded in the binary and written to a private directory at startup.
* Output is PDF/A-3b; e-invoices embed their XML (Factur-X / ZUGFeRD CII) as an attachment. UBL 2.1 and CII XML are also served on their own.
* Documents are stored through the `BlobStore` port (local disk, S3-compatible, GCS).
* Rendering runs in the `documents` queue; in Tanvo Cloud a dedicated Deployment runs that queue.

---

## 11. Webhooks (libs/webhooks)

* **Outbound**: each organization has webhook endpoints; every domain event that is subscribed is delivered as a River job with retries and exponential backoff, signed per the Standard Webhooks specification, with delivery attempts recorded and replayable.
* **Inbound**: provider webhooks are verified, stored and processed by jobs; failures are retried and kept for inspection.
* Private network targets are refused unless explicitly allowed (SSRF protection).

---

## 12. Providers (providers/*)

Each provider is a Go module implementing ports of `libs/core`:

| Port | Providers |
| :--- | :--- |
| `PaymentProvider` (customers, payment methods, checkout, charges, refunds, webhooks) | Stripe, Adyen, GoCardless, Cashfree, Flutterwave, Moneyhash |
| `TaxProvider` (quotes, commits, voids) | Anrok, Avalara |
| `AccountingSync` / `CRMSync` (customers, invoices, credit notes, payments, mappings) | NetSuite, Xero, Salesforce, HubSpot |
| `IdentityProvider` (OIDC) | Google, Okta, Entra ID, generic OIDC |

Integrations call the vendors' APIs directly; Tanvo does not depend on an integration broker.

---

## 13. Deployment

### 13.1 Roles
The single binary `tanvo serve` runs any combination of roles:

| Role | Runs |
| :--- | :--- |
| `api` | Connect/REST server, provider webhooks, document downloads |
| `worker` | River workers for the queues it is given (`--queues`) |
| `consumer` | Kafka consumers: enrichment, ClickHouse writer, in-advance charges |
| `all` | Everything (default for self-hosted installations) |

`tanvo migrate`, `tanvo admin …` and `tanvo version` are separate commands.

### 13.2 Self-hosted
* Minimal: `tanvo serve` + PostgreSQL. Typst is included in the container image.
* Optional: Redpanda/Kafka + ClickHouse for high event volumes, Redis/Valkey for caching, S3-compatible storage.
* Distributed as a container image and a Docker Compose file; a Helm chart serves both self-hosted Kubernetes and Tanvo Cloud.

### 13.3 Tanvo Cloud
* Kubernetes: one Deployment per role (and per heavy queue), migrations as a Job, horizontal autoscaling of `api` and `consumer`.
* **Cells**: a cell is a PostgreSQL cluster with its River workers, Kafka topics and ClickHouse database, serving a set of organizations. A thin control plane maps organizations to cells; growth adds cells. A large customer can get a dedicated cell.
* Tanvo Cloud is billed by Tanvo itself.

---

## 14. Enterprise Edition (ee/)

Features behind Lago's premium license (progressive billing, wallets' advanced rules, integrations, e-invoicing, …) are part of the AGPL core with no gate. Only what Lago runs as private cloud services is implemented as enterprise services under ELv2, each a separate process that uses `libs/proto` contracts and read-only data (PostgreSQL views or ClickHouse):

* Analytics and dashboards (revenue, MRR, usage, collections, forecasts), replacing Lago's private Data API and Superset.
* The billing assistant: an AI agent with an MCP server exposing Tanvo's API.
* Warehouse sync: copying the core's read-only export views, with their deletion tombstones, to customer data warehouses, replacing Lago's private data pipeline ([Feature Parity §1.43](parity.md#143-data-exports)).
* Customer-success tooling and the other private services listed in [Feature Parity §1.50](parity.md#150-lago-gates-and-their-tanvo-placement).

Self-hosted installations enable them with an offline-verifiable license key (Ed25519-signed); Tanvo Cloud enables them per plan. The AGPL core contains no license checks.

---

## 15. Security and Observability

* Secrets are encrypted with AES-256-GCM envelopes; data keys are encrypted by a key from configuration or a KMS.
* Security-relevant actions (sign-ins, key creation, role changes) are written to the security log.
* Every request and job carries a trace; logs are structured `slog` records with the organization and request IDs; metrics are exported through OpenTelemetry and `/metrics`.
